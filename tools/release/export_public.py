#!/usr/bin/env python3
"""Export committed Agent-Host sources for a separate public-source review."""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import posixpath
import re
import subprocess
from urllib.parse import unquote, urlsplit


ROOT_FILES = {
    ".gitignore", ".gitattributes", "AGENTS.md", "README.md", "Makefile",
    "go.mod", "go.sum", "LICENSE", "NOTICE",
}
ROOT_DIRECTORIES = {".github", "cmd", "internal", "docs", "evals", "examples", "experiments", "tools"}
PRIVATE_RUNNERS = {f"experiments/{name}/run.py" for name in ("jev", "mvp", "experience")}
RUNTIME_DIRECTORIES = {".agent-host", "production", "logs", "credentials", ".cache", ".claude"}
RUNTIME_NAMES = {".env", "credentials.json", "cookies.json", "auth.json", "tokens.json", "environment.json", "settings.local.json", "dingtalk.secret"}
RUNTIME_SUFFIXES = (".db", ".db-wal", ".db-shm", ".sqlite", ".sqlite3", ".log", ".ndjson", ".jsonl", ".pem", ".key", ".secret", ".token", ".env")
MANIFEST_NAME = "public-export-manifest.json"
TARGETS = {
    "docs/jev-evaluation-detail.md", "docs/service-install-detail.md",
    "experiments/mvp/README.md", "docs/runtime-assembly-detail.md",
}


def git(source, *args):
    result = subprocess.run(["git", "-C", str(source), *args], capture_output=True)
    if result.returncode:
        raise ValueError("could not inspect the committed Git source")
    return result.stdout


def safe_path(name):
    path = PurePosixPath(name)
    if not path.parts or path.is_absolute() or any(part in ("", ".", "..", ".git") for part in path.parts) or "\\" in name:
        raise ValueError("committed path cannot be exported safely")
    return path


def omitted(name):
    path = safe_path(name)
    parts = path.parts
    if "results" in parts or any(part in RUNTIME_DIRECTORIES for part in parts):
        return True
    if name in PRIVATE_RUNNERS or path.name in RUNTIME_NAMES or path.name.endswith(RUNTIME_SUFFIXES):
        return True
    if parts[0] == "experiments" and path.name.startswith("installation") and path.suffix == ".json":
        return True
    if parts[0] == "config":
        return not path.name.endswith(".example.json")
    return name not in ROOT_FILES and parts[0] not in ROOT_DIRECTORIES


def private_values(files):
    """Discover selected deployment values by document structure, never literals."""
    values = {}
    for name, pattern in (
        ("docs/jev-evaluation-detail.md", r"(?:Runtime|Validated) endpoint:\s*`([^`]+)`"),
        ("docs/service-install-detail.md", r"current selection is\s*`([^`]+)`"),
    ):
        if name not in files:
            continue
        text = files[name].decode("utf-8")
        match = re.search(pattern, text, re.I)
        if not match:
            raise ValueError("deployment document structure needs export review")
        selected = match.group(1)
        host = urlsplit(selected).hostname
        if not host:
            raise ValueError("deployment endpoint structure needs export review")
        values[host] = "gateway.example.invalid"
    name = "docs/runtime-assembly-detail.md"
    if name in files:
        text = files[name].decode("utf-8")
        match = re.search(r"authorized private-chat validation against\s*`([^`]+)`", text, re.I)
        if not match:
            raise ValueError("platform identity document structure needs export review")
        label = match.group(1)
        values[label] = "the configured application bot"
        for identity in re.findall(r"[（(]([^）)]+)[）)]", label):
            if len(identity) >= 2:
                values[identity] = "the validation owner"
    return values


def private_link_target(document, target):
    target = target.strip()
    if target.startswith("<"):
        target = target[1:].split(">", 1)[0]
    else:
        target = target.split(None, 1)[0] if target else ""
    parsed = urlsplit(target)
    if parsed.scheme or parsed.netloc or not parsed.path:
        return False
    raw = unquote(parsed.path)
    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(document), raw))
    if resolved == "." or resolved.startswith("../") or resolved.startswith("/"):
        return False
    return omitted(resolved)


def sanitize_links(document, text):
    references = set()

    def definition(match):
        if private_link_target(document, match.group(2)):
            references.add(match.group(1).casefold())
            return ""
        return match.group(0)

    text = re.sub(r"^\s*\[([^\]]+)\]:\s*(.+)$", definition, text, flags=re.M)

    def inline(match):
        if private_link_target(document, match.group(2)):
            return match.group(1) + " (private validation evidence)"
        return match.group(0)

    text = re.sub(r"!?\[([^\]]+)\]\(([^)]+)\)", inline, text)

    def reference(match):
        label, key = match.group(1), match.group(2) or match.group(1)
        if key.casefold() in references:
            return label + " (private validation evidence)"
        return match.group(0)

    text = re.sub(r"!?\[([^\]]+)\]\[([^\]]*)\]", reference, text)
    for key in references:
        text = re.sub(r"(?<!!)\[" + re.escape(key) + r"\](?![\[(])", key + " (private validation evidence)", text, flags=re.I)
    return text


def sanitize(name, payload, values):
    try:
        text = payload.decode("utf-8")
    except UnicodeDecodeError:
        return payload
    original = text
    if name == "experiments/mvp/README.md":
        section = re.search(r"^##\s", text, re.M)
        if not section:
            raise ValueError("historical experiment document structure needs export review")
        text = (
            "# Historical CLI memory validation\n\n"
            "This document summarizes private historical validation. Its gateway\n"
            "runner and raw reports are omitted from the public source export.\n"
            "Use the public CLI and synthetic evaluation fixtures for local checks;\n"
            "the observations below do not establish general accuracy.\n\n"
            + text[section.start():]
        )
    for value in sorted(values, key=len, reverse=True):
        text = re.sub(re.escape(value), lambda _: values[value], text, flags=re.I)
    if name.endswith(".md"):
        # Explicit identity fields in documentation retain their structural keys.
        text = re.sub(
            r'("(?:client_id|robot_code|corp_id|owner_user_id|account_id|sender_id|message_id|conversation_id)"\s*:\s*)"[^"\n]*"',
            r'\1"EXAMPLE_ID"', text,
        )
        text = sanitize_links(name, text)
    if PurePosixPath(name).parts[0] == "evals" and name.endswith(".json"):
        data = json.loads(text)
        if isinstance(data, dict):
            for case in data.get("cases", []):
                origin = case.get("origin", {})
                if origin.get("reference"):
                    origin["reference"] = (
                        "Private historical validation evidence is omitted; this suite retains synthetic regression inputs."
                        if origin.get("kind") == "incident"
                        else "Public synthetic fixture provenance in this evaluation suite."
                    )
            text = json.dumps(data, ensure_ascii=False, indent=2) + "\n"
    return text.encode("utf-8") if text != original else payload


def export_public(source, destination):
    source = Path(source).resolve(strict=True)
    if git(source, "rev-parse", "--show-toplevel").decode().strip() != str(source):
        raise ValueError("source must be the Git repository root")
    if git(source, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("public export requires a clean Git source")
    revision = git(source, "rev-parse", "HEAD").decode().strip()
    destination = Path(os.path.abspath(destination))
    for parent in (destination, *destination.parents):
        if parent.is_symlink():
            raise ValueError("public export destination cannot use symlink paths")
    if destination.exists():
        raise ValueError("public export destination already exists")
    if not destination.parent.is_dir():
        raise ValueError("public export destination parent must already exist")
    if destination == source or source in destination.parents:
        raise ValueError("public export destination must be outside the source repository")
    files, modes, omitted_paths = {}, {}, []
    for record in git(source, "ls-tree", "-rz", "--full-tree", revision).split(b"\0"):
        if not record:
            continue
        metadata, raw_name = record.split(b"\t", 1)
        mode, kind, object_id = metadata.decode().split()
        name = raw_name.decode("utf-8")
        safe_path(name)
        if omitted(name):
            omitted_paths.append(name)
            continue
        if kind != "blob" or mode not in ("100644", "100755"):
            raise ValueError("public export rejects tracked symlinks and submodules")
        files[name] = git(source, "cat-file", "blob", object_id)
        modes[name] = 0o755 if mode == "100755" else 0o644
    if MANIFEST_NAME in files:
        raise ValueError("public export manifest conflicts with a committed file")
    values = private_values(files)
    sanitized_files = []
    for name, payload in list(files.items()):
        next_payload = sanitize(name, payload, values)
        if next_payload != payload:
            sanitized_files.append(name)
        files[name] = next_payload
    # Recheck after reading blobs so a concurrently dirtied checkout is refused.
    if git(source, "status", "--porcelain=v1", "--untracked-files=all") or git(source, "rev-parse", "HEAD").decode().strip() != revision:
        raise ValueError("Git source changed during public export")
    manifest = {
        "schema_version": 1, "source_commit": revision,
        "exported_file_count": len(files), "omitted_paths": sorted(omitted_paths),
        "sanitized_files": sorted(sanitized_files),
    }
    destination.mkdir(mode=0o700)
    for name, payload in files.items():
        target = destination.joinpath(*safe_path(name).parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("xb") as output:
            output.write(payload)
        target.chmod(modes[name])
    with (destination / MANIFEST_NAME).open("x", encoding="utf-8") as output:
        json.dump(manifest, output, indent=2)
        output.write("\n")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--output", type=Path, required=True, help="entirely new directory outside the source repository")
    args = parser.parse_args()
    try:
        manifest = export_public(args.source, args.output)
    except (ValueError, OSError, UnicodeError, json.JSONDecodeError) as error:
        parser.exit(1, str(error) + "\n")
    print(json.dumps(manifest, indent=2))


if __name__ == "__main__":
    main()
