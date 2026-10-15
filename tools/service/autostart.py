"""Manage a source-checkout user service on macOS or Linux with systemd."""

import argparse
import json
import os
from pathlib import Path
import plistlib
import sys
import time

from . import install as legacy

LABEL = "com.agent-host.source"
UNIT = "agent-host.service"


def checked(run, args):
    if run(args).returncode:
        raise RuntimeError(f"service command failed: {' '.join(args[:3])}; inspect private service logs")


def systemd_quote(value, command=False):
    # systemd has its own quoting, specifier and environment expansion rules.
    value = str(value)
    if any(c in value for c in "\n\r\x00"):
        raise ValueError("service paths and PATH must not contain control characters")
    value = value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%")
    if command:
        value = value.replace("$", "$$")
    return '"' + value + '"'


def definition(repo, config, home, env, platform):
    args = [str(repo / "bin/agent-host"), "service", "run", "--restart",
            "--config-dir", str(config), "--viewer-source", str(repo / "tools/run-viewer"),
            "--viewer-python", sys.executable, "--no-browser"]
    logs = config.parent / "logs"
    # Preserve executable discovery, never the shell's provider tokens or secrets.
    path = env.get("PATH", "/usr/local/bin:/usr/bin:/bin")
    if platform == "darwin":
        target = home / "Library/LaunchAgents" / (LABEL + ".plist")
        content = plistlib.dumps({
            "Label": LABEL, "AgentHostCheckout": str(repo), "ProgramArguments": args,
            "WorkingDirectory": str(repo), "RunAtLoad": True,
            "KeepAlive": {"SuccessfulExit": False}, "ThrottleInterval": 30,
            "ExitTimeOut": 45, "Umask": 0o077,
            "EnvironmentVariables": {"PATH": path, "HOME": str(home)},
            "StandardOutPath": str(logs / "autostart.stdout.log"),
            "StandardErrorPath": str(logs / "autostart.stderr.log"),
        })
    elif platform == "linux":
        config_home = Path(env.get("XDG_CONFIG_HOME") or home / ".config")
        if not config_home.is_absolute():
            raise ValueError("XDG_CONFIG_HOME must be absolute")
        target = config_home / "systemd/user" / UNIT
        content = ("# Agent-Host checkout: " + str(repo) + "\n"
                   "[Unit]\nDescription=Agent-Host channels\n"
                   "[Service]\nType=simple\n"
                   "WorkingDirectory=" + systemd_quote(repo) + "\n"
                   "ExecStart=" + " ".join(systemd_quote(a, command=True) for a in args) + "\n"
                   "Environment=" + systemd_quote("PATH=" + path) + "\n"
                   "Restart=on-failure\nRestartSec=30\nTimeoutStopSec=45\nUMask=0077\n"
                   "[Install]\nWantedBy=default.target\n").encode()
    else:
        raise ValueError("automatic startup supports macOS and Linux with user systemd; use --no-autostart here")
    return target, content


def validate_owner(target, repo, platform):
    legacy.validate_target(target)
    if not target.exists():
        return
    if platform == "darwin":
        owned = plistlib.loads(target.read_bytes()).get("AgentHostCheckout") == str(repo)
    else:
        owned = target.read_text().splitlines()[:1] == ["# Agent-Host checkout: " + str(repo)]
    if not owned:
        raise ValueError(f"existing service belongs to another installation: {target}")


def running(platform, run):
    if platform == "darwin":
        return legacy.launch_state(f"gui/{os.getuid()}/{LABEL}", run)["running"]
    return run(["systemctl", "--user", "is-active", "--quiet", UNIT]).returncode == 0


def await_channels(config, offsets, platform, run, timeout=60):
    deadline = time.monotonic() + timeout
    connected = set()
    pending = {name: b"" for name in offsets}
    positions = dict(offsets)
    stable_since = None
    while time.monotonic() < deadline:
        for name in offsets:
            log = config.parent / "channels" / name / "logs/service.ndjson"
            if not log.exists():
                continue
            with log.open("rb") as stream:
                stream.seek(positions[name])
                data = stream.read(1024 * 1024)
                positions[name] = stream.tell()
            lines = (pending[name] + data).split(b"\n")
            pending[name] = lines.pop()
            if len(pending[name]) > 1024 * 1024:
                raise RuntimeError("service startup event exceeds its size limit")
            for line in lines:
                try:
                    event = json.loads(line)
                except (ValueError, UnicodeDecodeError):
                    continue  # Diagnostics share the channel's structured-event log.
                if isinstance(event, dict) and event.get("type") == "channel.connected":
                    connected.add(name)
                if isinstance(event, dict) and event.get("type") == "channel.error":
                    raise RuntimeError(f"channel {name} failed; inspect its private logs")
        if connected == set(offsets) and running(platform, run):
            stable_since = stable_since or time.monotonic()
            if time.monotonic() - stable_since >= 1:
                return
        else:
            stable_since = None
        time.sleep(0.2)
    missing = ", ".join(sorted(set(offsets) - connected)) or "service process"
    raise RuntimeError(f"startup not confirmed for {missing}; inspect private logs")


def install(repo, config, home, env, platform=sys.platform, run=legacy.command):
    repo, config = repo.resolve(strict=True), config.resolve(strict=True)
    target, content = definition(repo, config, home, env, platform)
    validate_owner(target, repo, platform)
    # Preflight before stopping a working service or replacing its definition.
    checked(run, [str(repo / "bin/agent-host"), "service", "check", "--config-dir", str(config)])
    names = []
    for file in sorted(config.glob("channels.*.json")):
        if json.loads(file.read_text()).get("enabled") is True:
            names.append(file.name[len("channels."):-len(".json")])
    if not names:
        raise ValueError("no enabled channels to start")
    legacy.private_directory(target.parent)
    logs = config.parent / "logs"
    legacy.private_directory(logs)
    for name in ("autostart.stdout.log", "autostart.stderr.log"):
        legacy.private_log(logs / name)
    if platform == "darwin":
        legacy.stop_existing(f"gui/{os.getuid()}/{LABEL}", run)
    elif target.exists():
        checked(run, ["systemctl", "--user", "stop", UNIT])
    offsets = {}
    for name in names:
        log = config.parent / "channels" / name / "logs/service.ndjson"
        offsets[name] = log.stat().st_size if log.exists() else 0
    legacy.atomic_write(target, content)
    if platform == "darwin":
        checked(run, ["/bin/launchctl", "enable", f"gui/{os.getuid()}/{LABEL}"])
        checked(run, ["/bin/launchctl", "bootstrap", f"gui/{os.getuid()}", str(target)])
    else:
        checked(run, ["systemctl", "--user", "daemon-reload"])
        checked(run, ["systemctl", "--user", "enable", UNIT])
        checked(run, ["systemctl", "--user", "restart", UNIT])
    await_channels(config, offsets, platform, run)
    return target


def uninstall(repo, config, home, env, platform=sys.platform, run=legacy.command):
    repo = repo.resolve(strict=True)
    target, _ = definition(repo, config, home, env, platform)
    validate_owner(target, repo, platform)
    if not target.exists():
        return target
    if platform == "darwin":
        legacy.stop_existing(f"gui/{os.getuid()}/{LABEL}", run)
    else:
        checked(run, ["systemctl", "--user", "disable", "--now", UNIT])
    target.unlink()
    if platform == "linux":
        checked(run, ["systemctl", "--user", "daemon-reload"])
    return target


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "uninstall"))
    parser.add_argument("--config-dir", type=Path)
    parser.add_argument("--runtime-dir", type=Path, help="Independent bundle containing bin, config and tools/run-viewer")
    args = parser.parse_args(argv)
    repo = args.runtime_dir or Path(__file__).resolve().parents[2]
    if not repo.is_absolute():
        parser.error("--runtime-dir must be absolute")
    config = args.config_dir or (repo / "config" if args.runtime_dir else repo / ".agent-host/config")
    if not config.is_absolute():
        parser.error("--config-dir must be absolute")
    try:
        action = install if args.action == "install" else uninstall
        target = action(repo, config, Path.home(), os.environ)
    except (OSError, ValueError, RuntimeError) as error:
        print(f"Autostart failed: {error}", file=sys.stderr)
        return 1
    print(f"Autostart {args.action} complete: {target}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
