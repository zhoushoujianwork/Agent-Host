#!/usr/bin/env python3
"""Run a private project-local Host runtime in the foreground with visible logs."""
import argparse
from contextlib import contextmanager
import fcntl
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

VIEWER_FILES = ("server.py", "memory_view.py", "index.html", "app.js", "style.css",
                "memory.html", "memory.js", "memory.css")


def private_directory(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError("runtime directories must not be symlinks")
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if not path.is_dir() or path.stat().st_uid != os.getuid():
        raise ValueError("runtime directory ownership mismatch")
    path.chmod(0o700)


def private_file(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_uid != os.getuid():
        raise ValueError("runtime files must be regular and owned by the current user")
    if path.stat().st_mode & 0o077:
        raise ValueError("runtime files must deny group and other access")


def prepare(root, binary, viewer_source):
    root = Path(root).absolute()
    if root.resolve() != root:
        raise ValueError("runtime root must have a canonical path without symlinks")
    if not any((root / "config" / name).exists() for name in ("component-service.json", "service.json")):
        raise ValueError("missing runtime config/component-service.json; prepare private component and channel configuration using config/*.example.json")
    for folder in ("", "config", "state", "workspace", "native", "logs", "viewer"):
        private_directory(root / folder)
    for filename in VIEWER_FILES:
        target = root / "viewer" / filename
        if target.exists() or target.is_symlink():
            private_file(target)
        fd, temporary = tempfile.mkstemp(prefix=".viewer-", dir=target.parent)
        try:
            with os.fdopen(fd, "wb") as output:
                output.write((Path(viewer_source) / filename).read_bytes())
            os.replace(temporary, target)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    checked = subprocess.run([str(binary), "service", "check", "--root", str(root)],
                             capture_output=True, text=True)
    if checked.returncode:
        raise ValueError("runtime configuration check failed: " + checked.stderr.strip())
    return root


@contextmanager
def runtime_lock(root):
    path = root / "run.lock"
    if path.exists() or path.is_symlink():
        private_file(path)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("runtime already running; stop its make run command before restarting")
        yield
    finally:
        os.close(fd)


def run(root, binary, viewer_python, no_browser):
    log_path = root / "logs/service.ndjson"
    pid_path = root / "service.pid"
    for path in (log_path, pid_path):
        if path.exists() or path.is_symlink():
            private_file(path)
    command = [str(binary), "service", "run", "--root", str(root),
               "--viewer-python", viewer_python]
    if no_browser:
        command.append("--no-browser")
    fd = os.open(log_path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb", buffering=0) as log:
        child = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                 start_new_session=True)
        old_handlers = {}
        stop_requested = False
        try:
            with os.fdopen(os.open(pid_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), "w") as output:
                output.write(str(child.pid) + "\n")
            def stop(signum, _frame):
                nonlocal stop_requested
                if child.poll() is None:
                    stop_requested = True
                    child.send_signal(signum)
            old_handlers = {sig: signal.signal(sig, stop) for sig in (signal.SIGINT, signal.SIGTERM)}
            print("Agent-Host runtime: " + str(root), flush=True)
            print("Logs: " + str(log_path), flush=True)
            for line in child.stdout:
                log.write(line)
                sys.stdout.buffer.write(line)
                sys.stdout.buffer.flush()
            code = child.wait()
            # Host cancellation exits 1; an operator-requested stop is normal.
            if stop_requested and code in (0, 1, -signal.SIGINT, -signal.SIGTERM):
                return 0
            return code if code >= 0 else 128 - code
        finally:
            for sig, handler in old_handlers.items():
                signal.signal(sig, handler)
            if child.poll() is None:
                child.terminate()
                child.wait(timeout=15)
            child.stdout.close()
            if pid_path.exists() and pid_path.read_text().strip() == str(child.pid):
                pid_path.unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--viewer-source", type=Path, required=True)
    parser.add_argument("--viewer-python", default=sys.executable)
    parser.add_argument("--no-browser", action="store_true")
    args = parser.parse_args()
    try:
        root = prepare(args.root, args.binary.absolute(), args.viewer_source)
        with runtime_lock(root):
            return run(root, args.binary.absolute(), args.viewer_python, args.no_browser)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print("Agent-Host startup failed: " + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
