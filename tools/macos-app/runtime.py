#!/usr/bin/env python3
"""Prepare the private macOS app runtime without selecting an execution backend.

The app seeds only Host-owned state, an explicit Agent catalog and a Chat
handoff. The handoff deliberately omits --offload-config so Agent-Host inherits
the current Offload default. It never creates an ACP profile, native runtime
profile, model selection or provider credentials.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import secrets
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile

MARKER = ".agh-app-runtime.json"
OWNER = "agent-host-macos-app"
DIRECTORIES = ("config", "cli-state", "workspace")
PROJECT_ID = "local"
AGENT_ID, AGENT_NAME = "default", "Local assistant"
DEFAULT_FLAGS = ("--home", "--agents-config", "--agent", "--workspace")
ALLOWED_FLAGS = set(DEFAULT_FLAGS) | {"--memory-scope", "--timeout"}
MAX_JSON = 64 * 1024


class SetupError(Exception):
    """A bounded message safe to show to the local user."""


def executable(value, label):
    path = Path(value) if isinstance(value, str) else None
    if path is None or not path.is_absolute() or "\x00" in value or "\n" in value or not path.is_file() or not os.access(path, os.X_OK):
        raise SetupError(f"{label} must be an absolute path to an executable file.")
    return str(path)


def owned(info):
    return info.st_uid == os.getuid()


def private_directory(path, label):
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        os.mkdir(path)
        os.chmod(path, 0o700)
        return
    if not stat.S_ISDIR(info.st_mode) or not owned(info):
        raise SetupError(f"{label} must be a real directory owned by the current user.")
    if info.st_mode & 0o077:
        os.chmod(path, 0o700)


def read_private(path, label):
    try:
        info = os.lstat(path)
        if not stat.S_ISREG(info.st_mode) or not owned(info) or info.st_size > MAX_JSON:
            raise SetupError(f"{label} must be a private regular file owned by the current user.")
        if info.st_mode & 0o077:
            os.chmod(path, 0o600)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as stream:
            return stream.read(MAX_JSON + 1)
    except OSError:
        raise SetupError(f"{label} cannot be read.") from None


def read_json(path, label):
    try:
        value = json.loads(read_private(path, label))
    except ValueError:
        raise SetupError(f"{label} is not valid JSON; it was left unchanged.") from None
    if not isinstance(value, dict):
        raise SetupError(f"{label} must be a JSON object; it was left unchanged.")
    return value


def atomic_write(path, data):
    temporary = Path(path).with_name(f".{Path(path).name}.{secrets.token_hex(4)}.tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def dump(value):
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


def has_marker(root):
    path = Path(root) / MARKER
    try:
        info = os.lstat(path)
        value = json.loads(path.read_bytes())
    except (OSError, ValueError):
        return False
    return stat.S_ISREG(info.st_mode) and owned(info) and isinstance(value, dict) and value.get("owner") == OWNER and value.get("schema") == 1


def ensure_root(requested):
    requested = Path(requested)
    if not requested.is_absolute():
        raise SetupError("The runtime root must be an absolute path.")
    requested.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    parent = requested.parent.resolve(strict=True)
    info = os.lstat(parent)
    if not stat.S_ISDIR(info.st_mode) or not owned(info):
        raise SetupError("The app support directory must be owned by the current user.")
    root = parent / requested.name
    try:
        info = os.lstat(root)
    except FileNotFoundError:
        stage = Path(tempfile.mkdtemp(prefix=".runtime-init-", dir=parent))
        try:
            os.chmod(stage, 0o700)
            atomic_write(stage / MARKER, dump({"schema": 1, "owner": OWNER, "created": datetime.now(timezone.utc).isoformat()}))
            for name in DIRECTORIES:
                os.mkdir(stage / name)
                os.chmod(stage / name, 0o700)
            os.rename(stage, root)
        except OSError:
            shutil.rmtree(stage, ignore_errors=True)
            if not (root.is_dir() and not root.is_symlink() and has_marker(root)):
                raise
        except BaseException:
            shutil.rmtree(stage, ignore_errors=True)
            raise
        return root
    if not stat.S_ISDIR(info.st_mode) or not owned(info):
        raise SetupError("The runtime root is a symbolic link or is not owned by the current user; it was not used.")
    if not has_marker(root):
        raise SetupError("A directory already exists at the app runtime location and is not an Agent-Host app runtime; it was not used.")
    if info.st_mode & 0o077:
        os.chmod(root, 0o700)
    return root


def ensure_layout(root):
    for name in DIRECTORIES:
        private_directory(root / name, name)


def registered_workspace(home):
    database = home / "state.db"
    if not database.exists():
        return None
    try:
        connection = sqlite3.connect(f"file:{database}?mode=ro", uri=True, timeout=2)
        try:
            row = connection.execute("SELECT workspace FROM projects WHERE id=?", (PROJECT_ID,)).fetchone()
        finally:
            connection.close()
    except sqlite3.Error:
        return None
    return row[0] if row else None


def register_project(root, binary, run):
    home, workspace = root / "cli-state", root / "workspace"
    recorded = registered_workspace(home)
    if recorded is not None and os.path.realpath(recorded) == os.path.realpath(workspace):
        return
    command = "add" if recorded is None else "update"
    done = run([binary, "project", command, "--home", str(home), "--id", PROJECT_ID,
                "--name", "Local workspace", "--workspace", str(workspace)],
               capture_output=True, text=True, timeout=30, stdin=subprocess.DEVNULL)
    if done.returncode != 0:
        detail = (done.stderr.strip().splitlines() or ["unknown error"])[-1][:200]
        raise SetupError("Local task state could not be initialized: " + detail)


def option(args, flag):
    values = [args[i + 1] for i, item in enumerate(args[:-1]) if item == flag]
    return values[0] if len(values) == 1 else None


def validate_args(args):
    if not isinstance(args, list) or len(args) % 2 or not all(isinstance(item, str) for item in args):
        raise SetupError("web-chat.json has no valid option/value args list.")
    flags = args[::2]
    if len(set(flags)) != len(flags) or any(flag not in ALLOWED_FLAGS for flag in flags):
        raise SetupError("web-chat.json contains unsupported or duplicate execution options.")
    for flag in DEFAULT_FLAGS:
        if option(args, flag) is None:
            raise SetupError("web-chat.json is missing a required Host option.")
    return args


def current(root):
    web = root / "config/web-chat.json"
    if not os.path.lexists(web):
        return None
    handoff = read_json(web, "web-chat.json")
    args = validate_args(handoff.get("args"))
    return {"agent": option(args, "--agent"), "inherits_offload_default": True}


def write_if_missing(path, value):
    if not os.path.lexists(path):
        atomic_write(path, dump(value))


def seed(root, binary):
    config = root / "config"
    agents, web = config / "agents.cli.json", config / "web-chat.json"
    write_if_missing(agents, {"default_agent": AGENT_ID, "agents": [{
        "id": AGENT_ID, "name": AGENT_NAME, "projects": [],
        "knowledge_sources": [], "native_skills": []}]})
    if not os.path.lexists(web):
        atomic_write(web, dump({
            "binary": binary,
            "cwd": str(root / "workspace"),
            "args": ["--home", str(root / "cli-state"), "--agents-config", str(agents),
                     "--agent", AGENT_ID, "--workspace", str(root / "workspace")]}))
        return
    handoff = read_json(web, "web-chat.json")
    validate_args(handoff.get("args"))
    if handoff.get("binary") != binary:
        handoff["binary"] = binary
        atomic_write(web, dump(handoff))


def viewer_arguments(root, settings, config):
    agent = (settings or {}).get("agent") or AGENT_ID
    result = ["--home", str(root / "cli-state"), "--backup-root", str(root),
              "--agent", agent, "--all-chats"]
    if config:
        result += ["--chat-config", str(config)]
    return result


def initialize(requested, binary, run=None, home=None):
    del home
    binary = executable(binary, "The bundled Host executable")
    root = ensure_root(requested)
    ensure_layout(root)
    register_project(root, binary, run or subprocess.run)
    web = root / "config/web-chat.json"
    problems = []
    settings = None
    try:
        seed(root, binary)
        settings = current(root)
    except SetupError as error:
        problems = [str(error)]
    return {"status": "ready" if settings and not problems else "needs_attention",
            "root": str(root), "home": str(root / "cli-state"),
            "config": str(web) if os.path.lexists(web) else None,
            "settings": settings, "problems": problems,
            "viewer_arguments": viewer_arguments(root, settings, web if os.path.lexists(web) else None)}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("init",))
    parser.add_argument("--root", required=True)
    parser.add_argument("--binary", required=True)
    options = parser.parse_args(argv)
    try:
        result = initialize(options.root, options.binary)
    except SetupError as error:
        print(json.dumps({"status": "error", "message": str(error)}))
        return 1
    except Exception as error:
        print(json.dumps({"status": "error", "message": f"Setup failed unexpectedly ({type(error).__name__}). Check local permissions and retry."}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
