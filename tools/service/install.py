#!/usr/bin/env python3
"""Install and start the current user's production macOS Agent-Host service."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time

LABEL = "com.agent-host.production"
CONNECT_TIMEOUT = 35
VIEWER_FILES = ("server.py", "memory_view.py", "index.html", "app.js", "style.css",
                "memory.html", "memory.js", "memory.css", "settings_store.py",
                "settings.html", "settings.js", "settings.css",
                "agent_profiles.py", "agent-profiles.js", "agent-profiles.css",
                "chat_runtime.py", "chat.js", "workspace.js", "workspace.css", "theme.js", "theme.css", "pages.css", "embedded.css", "logo.svg", "shortcuts.js", "settings-fields.js", "settings-simple.css")


def private_directory(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"runtime directory must not be a symlink: {path}")
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid():
        raise ValueError(f"runtime directory must belong to the current user: {path}")
    path.chmod(0o700)


def private_log(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"log must not be a symlink: {path}")
    if path.exists():
        info = path.stat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError(f"log must be a regular file owned by the current user: {path}")
    fd = os.open(path, os.O_CREAT | os.O_WRONLY | os.O_APPEND, 0o600)
    try:
        os.fchmod(fd, 0o600)
    finally:
        os.close(fd)


def atomic_write(path, content, mode=0o600):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"installation target must not be a symlink: {path}")
    fd, temporary = tempfile.mkstemp(prefix=".install-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            os.fchmod(output.fileno(), mode)
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def validate_target(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"installation target must not be a symlink: {path}")
    if path.exists():
        info = path.stat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError(f"installation target must be a regular file owned by the current user: {path}")


def command(args, timeout=45):
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout)


def launch_state(target, run=command):
    result = run(["/bin/launchctl", "print", target])
    if result.returncode:
        # Only launchd's explicit missing-service result is an absent service.
        # Permission errors and other failures must not be treated as absence.
        if "Could not find service" in result.stderr:
            return {"loaded": False, "running": False, "pid": None}
        raise RuntimeError("cannot inspect launchd service")
    state_match = re.search(r"^\s*state = ([^\n]+)$", result.stdout, re.M)
    state = state_match[1].strip() if state_match else None
    running = state == "running"
    pid = re.search(r"^\s*pid = (\d+)\s*$", result.stdout, re.M)
    exit_status = re.search(r"^\s*last exit code = (-?\d+)\s*$", result.stdout, re.M)
    return {"loaded": True, "running": running, "state": state, "pid": int(pid[1]) if pid else None,
            "exit_status": int(exit_status[1]) if exit_status else None}


def process_alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def stop_existing(target, run=command, alive=process_alive, sleep=time.sleep):
    previous = launch_state(target, run)
    if not previous["loaded"]:
        return
    result = run(["/bin/launchctl", "bootout", target])
    if result.returncode:
        raise RuntimeError("cannot stop the installed Agent-Host service")
    deadline = time.monotonic() + 35
    while previous["pid"] and alive(previous["pid"]):
        if time.monotonic() >= deadline:
            raise RuntimeError("previous Agent-Host process did not stop; installation aborted")
        sleep(0.1)
    if launch_state(target, run)["loaded"]:
        raise RuntimeError("previous Agent-Host launchd job remains loaded")


def launch_description(root):
    root = Path(root)
    return {
        "Label": LABEL,
        "ProgramArguments": [str(root / "bin/agent-host"), "service", "run", "--root", str(root),
                             "--viewer-python", sys.executable],
        "WorkingDirectory": str(root / "workspace"),
        "RunAtLoad": True,
        "KeepAlive": {"SuccessfulExit": False},
        "ThrottleInterval": 30,
        "ExitTimeOut": 30,
        "ProcessType": "Background",
        "Umask": 0o077,
        "EnvironmentVariables": {"PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
        "StandardOutPath": str(root / "logs/service.ndjson"),
        "StandardErrorPath": str(root / "logs/service.stderr.log"),
    }


def await_connection(target, log, offset, run=command, sleep=time.sleep, timeout=CONNECT_TIMEOUT):
    deadline = time.monotonic() + timeout
    pending = b""
    connected_at = None
    observed_pid = None
    with Path(log).open("rb") as stream:
        stream.seek(offset)
        while time.monotonic() < deadline:
            current = launch_state(target, run)
            if not current["loaded"]:
                raise RuntimeError("Agent-Host launchd job disappeared before connection")
            if current["pid"]:
                if observed_pid and observed_pid != current["pid"]:
                    raise RuntimeError("Agent-Host process changed during startup")
                observed_pid = current["pid"]
            chunk = stream.read(1024 * 1024)
            pending += chunk
            lines = pending.split(b"\n")
            pending = lines.pop()
            if len(pending) > 1024 * 1024:
                raise RuntimeError("service startup log exceeds its event limit")
            for line in lines:
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except (ValueError, UnicodeDecodeError):
                    raise RuntimeError("service did not produce valid structured startup events") from None
                if not isinstance(event, dict):
                    raise RuntimeError("service produced an invalid startup event")
                if event.get("type") == "channel.error":
                    raise RuntimeError("DingTalk connection failed; inspect the private service logs")
                if event.get("type") == "channel.connected":
                    if connected_at is None:
                        connected_at = time.monotonic()
            if connected_at is not None and time.monotonic() - connected_at >= 1 and current["running"] and current["pid"]:
                return current["pid"]
            # launchd can assign a PID while still spawning/xpcproxy-execing.
            # A non-running transitional state is not evidence of process exit.
            if not current["running"] and (current.get("state") in {"not running", "exited"} or current.get("exit_status") is not None):
                raise RuntimeError("Agent-Host stopped before confirming its connection")
            sleep(0.2)
    raise RuntimeError("DingTalk connection was not confirmed before the startup deadline; inspect private service logs")


def install(binary, root, launch_agents, run=command):
    root, binary = Path(root), Path(binary).resolve(strict=True)
    if not root.is_absolute() or not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("installation requires an absolute root and an executable binary")
    private_directory(root.parent)
    private_directory(root)
    for name in ("bin", "config", "state", "workspace", "native", "logs", "viewer"):
        private_directory(root / name)
    required = [root / "config" / name for name in ("service.json", "environment.json", "dingtalk.json")]
    if any(not path.exists() for path in required):
        raise ValueError("prepare private production config/service.json, environment.json and dingtalk.json before make install; see docs/service-install.md")
    candidate_fd, candidate_name = tempfile.mkstemp(prefix=".agent-host-", dir=root / "bin")
    os.close(candidate_fd)
    candidate = Path(candidate_name)
    try:
        shutil.copyfile(binary, candidate)
        candidate.chmod(0o700)
        checked = run([str(candidate), "service", "check", "--root", str(root)])
        if checked.returncode:
            # The service checker emits sanitized configuration errors. Do not
            # render subprocess output here: private files remain the authority.
            raise RuntimeError("production configuration check failed; run the built binary's service check with this root for details")
        try:
            report = json.loads(checked.stdout)
        except (ValueError, UnicodeDecodeError):
            raise RuntimeError("production configuration check did not return valid JSON") from None
        if not isinstance(report, dict) or report.get("status") != "local_configuration_valid":
            raise RuntimeError("production configuration check did not confirm local validity")
        stdout, stderr = root / "logs/service.ndjson", root / "logs/service.stderr.log"
        private_log(stdout)
        private_log(stderr)
        installed = root / "bin/agent-host"
        validate_target(installed)
        validate_target(root / "installation.json")
        viewer_source = Path(__file__).resolve().parent.parent / "run-viewer"
        viewer_assets = {name: (viewer_source / name).read_bytes() for name in VIEWER_FILES}
        for name in viewer_assets:
            validate_target(root / "viewer" / name)
        private_log(root / "logs/viewer.log")
        launch_agents = Path(launch_agents)
        if launch_agents.is_symlink():
            raise ValueError("LaunchAgents directory must not be a symlink")
        launch_agents.mkdir(mode=0o700, parents=True, exist_ok=True)
        if not launch_agents.is_dir() or launch_agents.stat().st_uid != os.getuid():
            raise ValueError("LaunchAgents directory must belong to the current user")
        plist = launch_agents / f"{LABEL}.plist"
        validate_target(plist)
        domain = f"gui/{os.getuid()}"
        target = f"{domain}/{LABEL}"
        stop_existing(target, run)
        # Take the boundary after the old writer has stopped. Historical
        # connected events must never prove a new installation is running.
        offset = stdout.stat().st_size
        for name, content in viewer_assets.items():
            atomic_write(root / "viewer" / name, content)
        os.replace(candidate, installed)
        atomic_write(plist, plistlib.dumps(launch_description(root)))
        enabled = run(["/bin/launchctl", "enable", target])
        if enabled.returncode:
            raise RuntimeError("cannot enable Agent-Host launchd job")
        launched = run(["/bin/launchctl", "bootstrap", domain, str(plist)])
        if launched.returncode:
            raise RuntimeError("cannot bootstrap Agent-Host launchd job; installation did not start")
        pid = await_connection(target, stdout, offset, run)
        receipt = {"status": "connected", "label": LABEL, "pid": pid, "root": str(root),
                   "binary_sha256": hashlib.sha256(installed.read_bytes()).hexdigest(),
                   "installed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   "platform_round_trip_verified": False}
        atomic_write(root / "installation.json", (json.dumps(receipt, indent=2) + "\n").encode())
        return receipt
    finally:
        candidate.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("make install currently supports macOS user LaunchAgents only")
    try:
        result = install(args.binary, Path.home() / ".agent-host/production", Path.home() / "Library/LaunchAgents")
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
        print(f"Agent-Host installation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
