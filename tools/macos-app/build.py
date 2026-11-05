#!/usr/bin/env python3
"""Build a local, source-derived macOS bundle; never install or restart services."""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import platform
import re
import shutil
import subprocess
import sys
import tempfile

OFFICIAL_REPOSITORY = "https://github.com/zhoushoujianwork/Agent-Host.git"
STABLE_VERSION = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
DEV_VERSION = re.compile(r"^[0-9A-Za-z._+-]{1,64}$")
HOST_VERSION = re.compile(r"^agent-host (\S+) \(commit (\S+), dirty=(true|false|unknown), ")
VIEWER_OPTIONS = {"--home", "--channel-home", "--backup-root", "--chat-config", "--agent", "--audience", "--project", "--workspace"}
MIN_OS = "13.0"
DEVELOPMENT_REF = "main"


def parse_arguments(argv):
    """Split updater metadata flags from the viewer option/value pairs, which stay verbatim."""
    parser = argparse.ArgumentParser(add_help=False, allow_abbrev=False)
    parser.add_argument("--output")
    parser.add_argument("--version")
    parser.add_argument("--channel", choices=("stable", "dev"))
    parser.add_argument("--revision")
    options, arguments = parser.parse_known_args(argv)
    if arguments and not any(flag in arguments for flag in ("--home", "--channel-home")):
        sys.exit("Usage: python3 tools/macos-app/build.py [--output /absolute/Name.app] [--version V --channel stable|dev --revision SHA] "
                 "[--home /absolute/state [viewer options]]\n"
                 "Without viewer options the app creates and owns its local runtime on first launch. "
                 "Explicit options select advanced development or existing Host state; this does not start channel services.")
    # Accept explicit options supported by the bundled viewer without starting it.
    if len(arguments) % 2:
        sys.exit("Expected explicit viewer option/value pairs")
    for offset in range(0, len(arguments), 2):
        if arguments[offset] not in VIEWER_OPTIONS or offset + 1 >= len(arguments):
            sys.exit("Expected explicit viewer option/value pairs; unsupported option: " + arguments[offset])
        if arguments[offset] in {"--home", "--backup-root", "--chat-config"} and not Path(arguments[offset + 1]).is_absolute():
            sys.exit("State/config paths must be absolute")
        if arguments[offset] == "--channel-home":
            name, sep, path = arguments[offset + 1].partition("=")
            if not name or not sep or not Path(path).is_absolute():
                sys.exit("Channel sources require NAME=/absolute/state")
    if options.output is not None:
        output = Path(options.output)
        if not output.is_absolute() or output.suffix != ".app" or output.exists() or output.is_symlink():
            sys.exit("--output must be an absolute, non-existing path ending in .app")
    if options.channel and not options.version or options.version and not options.channel:
        sys.exit("--version and --channel must be given together")
    if options.channel == "stable" and not STABLE_VERSION.match(options.version):
        sys.exit("Stable versions must be MAJOR.MINOR.PATCH")
    if options.channel == "dev" and not DEV_VERSION.match(options.version):
        sys.exit("Invalid development version")
    if options.revision is not None and not re.match(r"^[0-9a-f]{40}$", options.revision):
        sys.exit("--revision must be a full lowercase commit")
    return options, arguments


def git(root, *args, run=subprocess.run):
    try:
        done = run(["git", "-C", str(root)] + list(args), capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.SubprocessError):
        return None
    return done.stdout.strip() if done.returncode == 0 else None


def safe_remote(url):
    return re.sub(r"(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@", r"\1", url or "")[:200]


def source_state(root, run=subprocess.run):
    revision = git(root, "rev-parse", "HEAD", run=run)
    status = git(root, "status", "--porcelain", run=run)
    tags = git(root, "tag", "--points-at", "HEAD", run=run) or ""
    exact = sorted(tag[1:] for tag in tags.split() if re.match(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$", tag))
    return {"revision": revision if revision and re.match(r"^[0-9a-f]{40}$", revision) else "unknown",
            "dirty": status is None or bool(status), "stable_tags": exact,
            "repository": safe_remote(git(root, "remote", "get-url", "origin", run=run))}


def host_identity(binary, run=subprocess.run):
    done = run([str(binary), "--version"], capture_output=True, text=True, timeout=30)
    match = HOST_VERSION.match(done.stdout.strip()) if done.returncode == 0 else None
    if not match:
        sys.exit("Cannot read the built Host version; run make build first")
    return {"version": match.group(1), "revision": match.group(2), "dirty": match.group(3)}


def build_metadata(options, state, host):
    """Derive AGHBuildMetadata; fail rather than record an app/Host mismatch."""
    revision = options.revision or state["revision"]
    if options.revision and state["revision"] not in ("unknown", options.revision):
        sys.exit("--revision differs from the source checkout HEAD")
    if options.channel:
        version, channel = options.version, options.channel
    else:
        # Stable only for a clean checkout at an exact tag whose Host was built with that version.
        stable = [tag for tag in state["stable_tags"] if tag == host["version"]]
        clean = not state["dirty"] and revision != "unknown"
        version, channel = (stable[0], "stable") if stable and clean else (host["version"], "dev")
    if channel == "stable" and (state["dirty"] or revision == "unknown"):
        sys.exit("Stable builds require a clean source checkout with a known revision")
    expected_dirty = "true" if state["dirty"] else "false"
    if host["version"] != version:
        sys.exit("Host version %s differs from app version %s; run make macos-app VERSION=%s" % (host["version"], version, version))
    if host["revision"] != ("unknown" if revision == "unknown" else revision) or host["dirty"] not in (expected_dirty, "unknown"):
        sys.exit("Host revision or dirty state differs from the source checkout; run make build again")
    return {"version": version, "revision": revision, "channel": channel, "dirty": state["dirty"],
            "architecture": platform.machine(), "min_os": MIN_OS, "repository": state["repository"],
            "development_ref": DEVELOPMENT_REF}


def main():
    root = Path(__file__).resolve().parents[2]
    options, arguments = parse_arguments(sys.argv[1:])
    host_binary = root / "bin/agent-host"
    if not host_binary.is_file():
        sys.exit("bin/agent-host is missing; run make build first")
    metadata = build_metadata(options, source_state(root), host_identity(host_binary))
    app = Path(options.output) if options.output else root / "dist/Agent-Host.app"
    bundle = app / "Contents"
    binary = bundle / "MacOS/Agent-Host"
    resources = bundle / "Resources"
    binary.parent.mkdir(parents=True, exist_ok=True)
    resources.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="agh-app-build-") as cache:
        sources = [str(path) for path in sorted((root / "tools/macos-app").glob("*.swift")) if not path.name.endswith("Tests.swift")]
        subprocess.run(["swiftc", "-target", platform.machine() + "-apple-macos13.0", "-module-cache-path", cache, "-swift-version", "5", "-O"] + sources + ["-o", str(binary)], check=True)
    shutil.copytree(root / "tools/run-viewer", resources / "tools/run-viewer", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__", "*_test.*", "fixtures"))
    shutil.copytree(root / "skills/agent-host-tasks", resources / "skills/agent-host-tasks", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__", "*_test.*"))
    (resources / "bin").mkdir(exist_ok=True)
    shutil.copy2(root / "bin/agent-host", resources / "bin/agent-host")
    (resources / "tools/macos-app").mkdir(parents=True, exist_ok=True)
    for name in ("update.py", "build.py", "connection.py", "runtime.py"):
        shutil.copy2(root / "tools/macos-app" / name, resources / "tools/macos-app" / name)
    journal_id = hashlib.sha256(json.dumps(arguments).encode()).hexdigest()[:16]
    with (bundle / "Info.plist").open("wb") as target:
        plistlib.dump({"CFBundleIdentifier": "local.agent-host.workspace", "CFBundleName": "Agent-Host",
                      "CFBundleExecutable": "Agent-Host", "CFBundlePackageType": "APPL",
                      "CFBundleShortVersionString": metadata["version"], "AGHBuildMetadata": metadata, "LSMinimumSystemVersion": MIN_OS,
                      "NSHighResolutionCapable": True, "AGHPython": sys.executable,
                      "NSAppTransportSecurity": {"NSAllowsLocalNetworking": True},
                      "AGHViewerArguments": arguments, "AGHJournalID": journal_id}, target)
    subprocess.run(["codesign", "--force", "--sign", "-", str(bundle.parent)], check=True)
    print(bundle.parent)


if __name__ == "__main__":
    main()
