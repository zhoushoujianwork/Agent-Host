"""Copy a stopped unified runtime to an independent, previously absent directory."""

import argparse
from contextlib import ExitStack
import datetime
import fcntl
import hashlib
import json
from pathlib import Path
import shutil
import sqlite3
import subprocess


def read(path):
    return json.loads(path.read_text())


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def raw_field(text, name):
    """Retain evidence JSON spelling, including escaped Unicode and numbers."""
    decoder = json.JSONDecoder()
    position = text.index("{") + 1
    while True:
        while text[position].isspace():
            position += 1
        if text[position] == "}":
            raise ValueError(f"missing JSON field: {name}")
        key, position = decoder.raw_decode(text, position)
        while text[position].isspace() or text[position] == ":":
            position += 1
        start = position
        _, position = decoder.raw_decode(text, position)
        if key == name:
            return text[start:position]
        while text[position].isspace():
            position += 1
        if text[position] == ",":
            position += 1


def write_preserving(path, value, field):
    if field not in value:
        write(path, value)
        return
    original = raw_field(path.read_text(), field)
    metadata = {k: v for k, v in value.items() if k != field}
    text = json.dumps(metadata, ensure_ascii=False, indent=2).rstrip()
    separator = "," if metadata else ""
    path.write_text(text[:-1].rstrip() + separator + "\n" + json.dumps(field) + ": " + original + "\n}\n")
    path.chmod(0o600)


def relocate(value, source, target):
    if isinstance(value, dict):
        # A memory scope is a stable logical identity, even when it looks like a path.
        return {k: v if k == "memory_scope" else relocate(v, source, target) for k, v in value.items()}
    if isinstance(value, list):
        return [relocate(v, source, target) for v in value]
    if isinstance(value, str) and (value == str(source) or value.startswith(str(source) + "/")):
        return str(target) + value[len(str(source)):]
    return value


def relocate_memory(source, target):
    """Change storage provenance and checksums without modifying any memory payload."""
    memory = target / "memory"
    hashes = {}
    for record in memory.glob("domains/*/*/*/revisions/*/record.json"):
        value = read(record)
        before = value["memory"]
        old_hash = digest(record)
        value["state_home"] = relocate(value["state_home"], source, target)
        write_preserving(record, value, "memory")
        if read(record)["memory"] != before:
            raise RuntimeError("memory payload changed during relocation")
        hashes[old_hash] = digest(record)
    for path in memory.rglob("*.json"):
        if path.name == "record.json":
            continue
        value = read(path)
        if not isinstance(value, dict):
            continue
        changed = False
        if "state_home" in value:
            value["state_home"] = relocate(value["state_home"], source, target)
            changed = True
        for domain in value.get("domains") or []:
            domain["state_home"] = relocate(domain["state_home"], source, target)
            changed = True
        for entry in value.get("entries") or []:
            for revision in entry.get("revisions") or []:
                old_hash = revision["record_sha256"]
                if old_hash not in hashes:
                    raise RuntimeError("memory manifest refers to a missing revision")
                revision["record_sha256"] = hashes[old_hash]
                changed = True
        if changed:
            write_preserving(path, value, "operations")
    for marker in target.glob("**/file-memory.json"):
        # Only current runtime homes; archived evidence and backups stay immutable.
        relative = marker.relative_to(target)
        if str(relative) == "cli-state/file-memory.json" or (
                len(relative.parts) == 4 and relative.parts[0] == "channels" and relative.parts[2] == "state"):
            write(marker, relocate(read(marker), source, target))


def memory_reports(binary, root):
    reports = {}
    # This is a channel-service relocation. Development CLI state is copied as
    # historical data, not opened/upgraded as an active production channel.
    homes = sorted((root / "channels").glob("*/state"))
    for home in homes:
        if not (home / "file-memory.json").exists():
            continue
        result = subprocess.run([str(binary), "memory", "verify", "--home", str(home)],
                                capture_output=True, text=True, timeout=60)
        if result.returncode:
            raise RuntimeError(f"memory verification failed for {home}; inspect it before starting")
        report = json.loads(result.stdout)
        report.pop("index_path", None)
        reports[str(home.relative_to(root))] = report
    return reports


def migrate(source, target, checkout):
    source, checkout = source.resolve(strict=True), checkout.resolve(strict=True)
    target = target.parent.resolve(strict=True) / target.name
    if target.exists() or target.is_symlink() or target.is_relative_to(source) or source.is_relative_to(target):
        raise ValueError("target must be absent and outside the source runtime")
    if not (source / "config/host.json").is_file():
        raise ValueError("source must contain unified configuration")
    artifacts = source if (source / "bin/agent-host").is_file() else checkout
    binary = artifacts / "bin/agent-host"
    with ExitStack() as stack:
        for channel in sorted((source / "channels").iterdir()):
            if channel.is_dir() and (channel / "state").is_dir():
                for lock_path in (channel / "run.lock", channel / "state/host.lock"):
                    lock = stack.enter_context(lock_path.open("a+"))
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if (source / "cli-state").is_dir():
            lock = stack.enter_context((source / "cli-state/host.lock").open("a+"))
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = memory_reports(binary, source)
        # Copy all history and private credentials, including native account state.
        # Do not follow symlinks into unrelated external files.
        shutil.copytree(source, target, symlinks=True)
        target.chmod(0o700)
        copied = 0
        for old in source.rglob("*"):
            if old.is_file() and not old.is_symlink():
                new = target / old.relative_to(source)
                if digest(old) != digest(new):
                    raise RuntimeError(f"copy checksum differs: {old.relative_to(source)}")
                copied += 1
        database_hashes = {}
        for path in target.rglob("*.db"):
            if path.is_symlink():
                continue
            with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
                if db.execute("pragma integrity_check").fetchone()[0] != "ok":
                    raise RuntimeError("copied database integrity check failed")
            database_hashes[str(path.relative_to(target))] = digest(path)
        for path in (target / "config").rglob("*.json"):
            value = read(path)
            relocated = relocate(value, source, target)
            if relocated != value:
                write(path, relocated)
        relocate_memory(source, target)
        (target / "bin").mkdir(mode=0o700, exist_ok=True)
        shutil.copy2(binary, target / "bin/agent-host")
        for name in ("tools/run-viewer", "skills/agent-host-memory"):
            if not (target / name).exists():
                shutil.copytree(artifacts / name, target / name)
        for name in ("tools/service/autostart.py", "tools/service/install.py", "tools/install.py"):
            destination = target / name
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            shutil.copy2(artifacts / name, destination)
        status = target / "backup-state/status.json"
        if status.exists():
            write(status, relocate(read(status), source, target))
        after = memory_reports(target / "bin/agent-host", target)
        if before != after:
            raise RuntimeError("memory identities, revisions, evidence or payload digest changed")
        result = subprocess.run([str(target / "bin/agent-host"), "service", "check", "--config-dir", str(target / "config")],
                                capture_output=True, text=True, timeout=60)
        if result.returncode:
            raise RuntimeError("copied service configuration failed preflight; source remains unchanged")
        receipt = {"version": 1, "source_root": str(source), "target_root": str(target),
                   "migrated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   "copied_files_verified": copied, "binary_sha256": digest(binary),
                   "database_sha256_before_verification": database_hashes, "memory": after,
                   "cli_state": "database copied unchanged as inactive history; schema not upgraded",
                   "status": "verified_stopped", "channels_started": False}
        write(target / "migration.json", receipt)
        return receipt


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--target", type=Path, required=True)
    args = parser.parse_args(argv)
    checkout = Path(__file__).resolve().parents[2]
    try:
        report = migrate(args.source, args.target, checkout)
    except (OSError, ValueError, RuntimeError, sqlite3.Error, subprocess.SubprocessError) as error:
        parser.exit(1, f"Migration failed: {error}\nSource retained; partial target, if present, is not activated.\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
