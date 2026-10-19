#!/usr/bin/env python3
"""Build a local native macOS beta archive from a reviewed public export's clean Git revision."""

import argparse
import datetime
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import platform
import posixpath
import re
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import quote, urlsplit


ROOT = Path(__file__).resolve().parents[2]
PUBLIC_REPOSITORY = "https://github.com/zhoushoujianwork/Agent-Host"
REQUIRED_FILES = (
    "LICENSE", "NOTICE", "README.md",
    "docs/beta-release.md", "docs/beta-release-detail.md",
    "docs/feedback.md", "docs/feedback-detail.md",
    "docs/service-install.md", "docs/service-install-detail.md",
    "config/component-service.example.json", "config/component-environment.example.json",
    "config/channel-dingtalk.example.json", "tools/service/install.py",
    "tools/run-viewer/server.py", "tools/run-viewer/memory_view.py",
    "tools/run-viewer/index.html", "tools/run-viewer/app.js", "tools/run-viewer/style.css",
    "tools/run-viewer/memory.html", "tools/run-viewer/memory.js", "tools/run-viewer/memory.css",
    "tools/run-viewer/settings_store.py", "tools/run-viewer/settings.html",
    "tools/run-viewer/settings.js", "tools/run-viewer/settings.css",
    "internal/knowledge/ATTRIBUTION.md", "internal/knowledge/memgov-LICENSE",
)
PUBLIC_DOCS = {
    "docs/beta-release.md", "docs/beta-release-detail.md",
    "docs/feedback.md", "docs/feedback-detail.md",
    "docs/service-install.md", "docs/service-install-detail.md",
}
VERSION_PATTERN = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?(?:\+[A-Za-z0-9][A-Za-z0-9.-]*)?")


def command(args, cwd):
    result = subprocess.run(args, cwd=cwd, check=False, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"{args[0]} command failed; inspect the local build before packaging")
    return result.stdout


def clean_revision(root):
    if command(["git", "status", "--porcelain", "--untracked-files=normal"], root).strip():
        raise ValueError("packaging requires a clean Git checkout; commit the intended release files first")
    return command(["git", "rev-parse", "HEAD"], root).strip()


def public_file(path):
    if path in {"LICENSE", "NOTICE", "README.md", "tools/service/install.py",
                "internal/knowledge/ATTRIBUTION.md", "internal/knowledge/memgov-LICENSE"}:
        return True
    name = PurePosixPath(path)
    if path in PUBLIC_DOCS:
        return True
    if len(name.parts) == 2 and name.parts[0] == "config":
        return name.name.endswith(".example.json")
    if path.startswith("tools/run-viewer/") and len(name.parts) == 3:
        return name.suffix in {".py", ".md", ".html", ".js", ".css"} and not name.name.endswith("_test.py")
    return False


def tracked_public_files(root, revision):
    result = subprocess.run(["git", "ls-tree", "-rz", revision], cwd=root, check=True, capture_output=True)
    files = {}
    for entry in result.stdout.split(b"\0"):
        if not entry:
            continue
        metadata, raw_path = entry.split(b"\t", 1)
        mode, kind, blob = metadata.decode().split()
        path = raw_path.decode()
        if not public_file(path):
            continue
        if kind != "blob" or mode not in {"100644", "100755"}:
            raise ValueError(f"public package file must be a regular tracked file: {path}")
        files[path] = blob
    missing = sorted(set(REQUIRED_FILES) - files.keys())
    if missing:
        raise ValueError("missing committed package files: " + ", ".join(missing))
    return files


def json_objects(text):
    decoder = json.JSONDecoder()
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        yield value
        text = text[end:]


def runtime_license_path(goroot):
    goroot = Path(goroot)
    path = goroot / "LICENSE"
    if path.is_file():
        return path
    # Homebrew installs GOROOT under libexec and retains Go's license beside it.
    if goroot.name == "libexec" and goroot.parent.parent.name == "go":
        path = goroot.parent / "LICENSE"
        if path.is_file():
            return path
    raise ValueError("Go runtime LICENSE is unavailable in GOROOT or the Homebrew Go package")


def module_licenses(root, goroot):
    modules = list(json_objects(command(["go", "list", "-mod=readonly", "-m", "-json", "all"], root)))
    files, notices = {}, []
    for module in modules:
        if module.get("Main"):
            continue
        name, version = module["Path"], module.get("Version", "")
        effective = module.get("Replace", module)
        if module.get("Replace") or not version or not effective.get("Dir"):
            raise ValueError(f"cannot identify a released dependency and its license: {name}")
        directory = Path(effective["Dir"])
        licenses = sorted(p for p in directory.iterdir()
                          if p.is_file() and not p.is_symlink()
                          and p.name.upper().startswith(("LICENSE", "NOTICE", "COPYING")))
        if not licenses:
            raise ValueError(f"dependency has no root LICENSE/NOTICE/COPYING file: {name}@{version}")
        prefix = PurePosixPath("licenses") / (name + "@" + version)
        if prefix.is_absolute() or ".." in prefix.parts or "\\" in str(prefix):
            raise ValueError(f"invalid dependency license path: {name}")
        paths = []
        for license_file in licenses:
            path = str(prefix / license_file.name)
            files[path] = license_file.read_bytes()
            paths.append(path)
        notices.append({"module": name, "version": version, "files": paths})
    runtime_license = runtime_license_path(goroot)
    files["licenses/go/LICENSE"] = runtime_license.read_bytes()
    return files, notices


def build_binary(root, destination, version, revision, goos, goarch):
    command(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=true",
             "-ldflags", "-X agent-host/internal/buildinfo.Version=" + version,
             "-o", str(destination), "./cmd/agent-host"], root)
    metadata = command(["go", "version", "-m", str(destination)], root)
    settings = {}
    for line in metadata.splitlines():
        line = line.strip()
        if line.startswith("build\t") and "=" in line:
            key, value = line.split("\t", 1)[1].split("=", 1)
            settings[key] = value
    if settings.get("vcs.revision") != revision or settings.get("vcs.modified") != "false":
        raise ValueError("built binary does not identify the clean selected Git revision")
    if settings.get("GOOS") != goos or settings.get("GOARCH") != goarch:
        raise ValueError("built binary does not match the native platform")
    shown = command([str(destination), "--version"], root).strip()
    if not shown.startswith("agent-host " + version + " ") or revision not in shown or "dirty=false" not in shown:
        raise ValueError("built binary --version does not expose the requested release identity")


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def package_documentation(files, revision, version):
    files["README.md"] = f"""# Agent-Host macOS beta {version}

This is a prebuilt runtime archive, not a source checkout.

- Start with [the beta guide](docs/beta-release.md).
- For the optional service, read [installation](docs/service-install.md).
- For local feedback reports, read [feedback](docs/feedback.md).

Source and complete documentation: [{PUBLIC_REPOSITORY}]({PUBLIC_REPOSITORY}).
The binary identifies commit {revision}. Guide links outside this archive open
the public repository at that commit. Install the included binary without rebuilding.
""".encode()
    for source in PUBLIC_DOCS | {"tools/run-viewer/README.md", "tools/run-viewer/README-detail.md"}:
        if source not in files:
            continue
        def replace(match):
            link = match[1]
            if link.startswith(("#", "//")) or urlsplit(link).scheme:
                return match[0]
            target, _, fragment = link.partition("#")
            target = posixpath.normpath(str(PurePosixPath(source).parent / target))
            if target in files:
                return match[0]
            if target.startswith(("/", "../")):
                raise ValueError(f"public guide links outside the source tree: {source}")
            url = PUBLIC_REPOSITORY + "/blob/" + revision + "/" + quote(target, safe="/")
            return "](" + url + ("#" + fragment if fragment else "") + ")"
        files[source] = re.sub(r"\]\(([^)\s]+)\)", replace, files[source].decode()).encode()


def create_package(root, output, version, build=build_binary):
    if not VERSION_PATTERN.fullmatch(version):
        raise ValueError("provide an explicit version, for example --version 0.2.0-beta.1")
    if sys.platform != "darwin":
        raise ValueError("beta packaging currently supports native macOS only")
    root, output = Path(root).resolve(), Path(output).resolve()
    revision = clean_revision(root)
    tracked = tracked_public_files(root, revision)
    environment = json.loads(command(["go", "env", "-json", "GOOS", "GOARCH", "GOROOT", "GOVERSION"], root))
    goos, goarch = environment["GOOS"], environment["GOARCH"]
    native_arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    if goos != "darwin" or goarch != native_arch:
        raise ValueError("Go target must match this Mac; cross-platform packages are not supported")
    stem = f"agent-host-{version}-{goos}-{goarch}"
    output.mkdir(parents=True, exist_ok=True)
    archive, manifest_file, checksum_file = [output / (stem + suffix) for suffix in (".tar.gz", ".manifest.json", ".sha256")]
    if any(path.exists() for path in (archive, manifest_file, checksum_file)):
        raise ValueError("package output already exists; choose a fresh directory or version")
    license_files, modules = module_licenses(root, environment["GOROOT"])
    with tempfile.TemporaryDirectory(prefix="agent-host-package-") as temporary:
        temporary = Path(temporary)
        binary = temporary / "agent-host"
        build(root, binary, version, revision, goos, goarch)
        if clean_revision(root) != revision:
            raise ValueError("Git revision changed during packaging")
        files = dict(license_files)
        for path, blob in tracked.items():
            files[path] = subprocess.run(["git", "cat-file", "blob", blob], cwd=root, check=True, capture_output=True).stdout
        files["bin/agent-host"] = binary.read_bytes()
        package_documentation(files, revision, version)
        manifest = {
            "manifest_version": 1, "product": "Agent-Host", "version": version,
            "revision": revision, "modified": False,
            "go_version": environment["GOVERSION"], "goos": goos, "goarch": goarch,
            "created_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "binary_sha256": sha256(files["bin/agent-host"]),
            "license_selection": "Go module graph from go list -m all, plus Go runtime and project notices",
            "go_runtime_license_layout": "GOROOT/LICENSE" if runtime_license_path(environment["GOROOT"]).parent == Path(environment["GOROOT"]) else "Homebrew Go package LICENSE",
            "dependency_licenses": modules,
            "files": [{"path": path, "sha256": sha256(data)} for path, data in sorted(files.items())],
        }
        manifest_bytes = (json.dumps(manifest, indent=2) + "\n").encode()
        files["manifest.json"] = manifest_bytes
        staged_archive = temporary / archive.name
        timestamp = int(command(["git", "show", "-s", "--format=%ct", revision], root).strip())
        with tarfile.open(staged_archive, "w:gz") as tar:
            for path, data in sorted(files.items()):
                info = tarfile.TarInfo(stem + "/" + path)
                info.size, info.mode = len(data), 0o755 if path == "bin/agent-host" else 0o644
                info.mtime = timestamp
                tar.addfile(info, io.BytesIO(data))
        if clean_revision(root) != revision:
            raise ValueError("Git revision changed before package completion")
        checksum_bytes = (sha256(staged_archive.read_bytes()) + "  " + archive.name + "\n"
                          + sha256(manifest_bytes) + "  " + manifest_file.name + "\n").encode()
        # Exclusive writes preserve an existing local release package.
        for path, data in ((archive, staged_archive.read_bytes()), (manifest_file, manifest_bytes), (checksum_file, checksum_bytes)):
            with path.open("xb") as stream:
                stream.write(data)
    return {"archive": str(archive), "manifest": str(manifest_file), "checksums": str(checksum_file),
            "version": version, "revision": revision, "binary_sha256": manifest["binary_sha256"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="Explicit beta/release version; no version is inferred")
    parser.add_argument("--output", type=Path, default=ROOT / "dist", help="Local package output directory")
    args = parser.parse_args()
    try:
        result = create_package(ROOT, args.output, args.version)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Agent-Host packaging failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
