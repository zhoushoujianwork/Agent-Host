#!/usr/bin/env python3
"""Run CLI handoffs using an explicit private configuration and provider environment."""

import argparse
import json
import os
from pathlib import Path
import stat
import sqlite3
import subprocess
import sys


PROVIDER_KEYS = {
    "TYPESAFE_API_KEY", "JEV_API_KEY", "ANTHROPIC_BASE_URL",
    "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
    "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
}


def private_json(path):
    path = Path(path)
    info = path.lstat()
    if not path.is_absolute() or not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("configuration must be an absolute, owner-private regular file")
    with path.open() as source:
        opened = os.fstat(source.fileno())
        if (info.st_dev, info.st_ino) != (opened.st_dev, opened.st_ino):
            raise ValueError("configuration changed while opening")
        value = json.load(source)
    if not isinstance(value, dict):
        raise ValueError("configuration must be a JSON object")
    return value


def configured_command(path):
    config = private_json(path)
    binary, cwd, args = config["binary"], config["cwd"], config["args"]
    if not Path(binary).is_absolute() or not os.access(binary, os.X_OK):
        raise ValueError("configured binary must be an absolute executable")
    if not Path(cwd).is_absolute() or not Path(cwd).is_dir():
        raise ValueError("configured workspace is unavailable")
    if not isinstance(args, list) or not all(isinstance(a, str) and a and "\x00" not in a for a in args):
        raise ValueError("args must contain nonempty strings")
    required = ("--home", "--agents-config", "--agent", "--workspace", "--native-profile")
    for flag in required:
        if args.count(flag) != 1 or args.index(flag) + 1 >= len(args):
            raise ValueError("handoff requires explicit state, agent, workspace and native profile")
    selected = {flag: args[args.index(flag) + 1] for flag in required}
    for flag in ("--home", "--agents-config", "--workspace", "--native-profile"):
        if not Path(selected[flag]).is_absolute():
            raise ValueError("handoff paths must be absolute")
    catalog = private_json(selected["--agents-config"])
    agents = catalog.get("agents", [])
    if not isinstance(agents, list) or not all(isinstance(agent, dict) for agent in agents) or not any(agent.get("id") == selected["--agent"] for agent in agents):
        raise ValueError("selected Agent is unavailable in the configured catalog")
    projects = [project for agent in agents for project in (agent.get("projects") or [])]
    if projects:
        database = Path(selected["--home"]) / "state.db"
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as connection:
            known = {row[0] for row in connection.execute("SELECT id FROM projects")}
        if not set(projects) <= known:
            raise ValueError("Agent catalog references projects unavailable in CLI state")
    if "-m" in args or "--message" in args or "--resume" in args or "--project" in args:
        raise ValueError("saved args must not include a task, session or project override")
    managed = private_json(config["environment_file"])
    if not managed.keys() <= PROVIDER_KEYS or not all(isinstance(v, str) and v.strip() == v and v and not any(c in v for c in "\x00\r\n") for v in managed.values()):
        raise ValueError("invalid managed provider environment")
    env = {k: v for k, v in os.environ.items() if k not in PROVIDER_KEYS and not k.startswith(("ANTHROPIC_", "CLAUDE_CODE_")) and k not in ("CLAUDE_CONFIG_DIR", "CLAUDECODE")}
    env.update(managed)
    return [binary, "chat", *args], cwd, env


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path.home() / ".config/agent-host/task-handoff.json")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("-m", "--message")
    parser.add_argument("--resume")
    parser.add_argument("--format", choices=("text", "json"), default="json")
    options = parser.parse_args(argv)
    try:
        command, cwd, env = configured_command(options.config)
        if options.check:
            print(json.dumps({"status": "configured", "command": command, "cwd": cwd, "credentials": "loaded; values hidden"}))
            return 0
        if not options.message:
            parser.error("provide -m MESSAGE, or --check for local configuration validation")
        command += ["--format", options.format, "-m", options.message]
        if options.resume:
            command += ["--resume", options.resume]
        result = subprocess.run(command, cwd=cwd, env=env, check=False)
        return result.returncode if result.returncode >= 0 else 128 - result.returncode
    except (OSError, ValueError, KeyError, TypeError, sqlite3.Error) as error:
        print(f"Handoff configuration failed ({type(error).__name__}); check the private configuration and referenced files.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
