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
import signal
import time

from results import destination_key, inspect_receipt, new_receipt, save_json


PROVIDER_KEYS = {
    "TYPESAFE_API_KEY", "JEV_API_KEY", "ANTHROPIC_BASE_URL",
    "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
    "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
}

FEEDBACK_FLAGS = ("--request-for", "--notify-to", "--notify-channel", "--feedback-config-dir")


class HandoffError(ValueError):
    """An actionable error containing no configuration values or credentials."""


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
    for arg in args:
        normalized = "--" + arg.lstrip("-").split("=", 1)[0]
        if arg.startswith("-") and normalized in required and arg != normalized:
            raise ValueError("use each destination flag once in its explicit long form")
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
    reserved = ("--m", "--message", "--resume", "--project", "--format")
    if any(a.startswith("-") and "--" + a.lstrip("-").split("=", 1)[0] in reserved for a in args):
        raise ValueError("saved args must not include a task, session or project override")
    managed = private_json(config["environment_file"])
    if not managed.keys() <= PROVIDER_KEYS or not all(isinstance(v, str) and v.strip() == v and v and not any(c in v for c in "\x00\r\n") for v in managed.values()):
        raise ValueError("invalid managed provider environment")
    env = {k: v for k, v in os.environ.items() if k not in PROVIDER_KEYS and not k.startswith(("ANTHROPIC_", "CLAUDE_CODE_")) and k not in ("CLAUDE_CONFIG_DIR", "CLAUDECODE")}
    env.update(managed)
    return [binary, "chat", *args], cwd, env


def run_result(command, cwd, env, config, output_dir, base_command, feedback_args=None):
    directory, receipt = new_receipt(config, base_command, cwd, output_dir)
    if feedback_args:
        receipt["feedback_args"] = feedback_args
        save_json(directory / "launch.json", receipt)
    if "--resume" in command:
        receipt["requested_session_id"] = command[command.index("--resume") + 1]
        save_json(directory / "launch.json", receipt)
    print(f"Handoff evidence: {directory}", file=sys.stderr, flush=True)
    started = time.monotonic()
    handlers = {}
    child = None
    try:
        with (directory / "events.jsonl").open("xb") as output, (directory / "stderr.log").open("xb") as errors:
            child = subprocess.Popen(command, cwd=cwd, env=env, stdout=output, stderr=errors, start_new_session=True)

            def forward(signum, _frame):
                try:
                    os.killpg(child.pid, signum)
                except ProcessLookupError:
                    pass

            for signum in (signal.SIGINT, signal.SIGTERM):
                handlers[signum] = signal.signal(signum, forward)
            code = child.wait()
            receipt["exit_code"] = code if code >= 0 else 128 - code
    except OSError as error:
        receipt["exit_code"] = 2
        receipt["launch_error"] = type(error).__name__
    finally:
        for signum, handler in handlers.items():
            signal.signal(signum, handler)
        receipt["elapsed_ms"] = round((time.monotonic() - started) * 1000)
        save_json(directory / "launch.json", receipt)
    result = inspect_receipt(directory, private_json)
    save_json(directory / "result.json", result)
    print(json.dumps(result, ensure_ascii=False))
    return receipt["exit_code"]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path.home() / ".config/agent-host/task-handoff.json")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--request-for", help="verified user ID whose demand this task fulfills")
    parser.add_argument("--notify-to", help="verified user ID to receive completion feedback")
    parser.add_argument("--notify-channel", choices=("dingtalk", "feishu"))
    parser.add_argument("--feedback-config-dir", help="absolute unified config directory for task feedback")
    parser.add_argument("-m", "--message")
    parser.add_argument("--resume")
    parser.add_argument("--resume-from", type=Path, help="continue a completed launch using its evidence directory")
    parser.add_argument("--inspect", type=Path, help="read a launch evidence directory without executing work")
    parser.add_argument("--output-dir", type=Path, help="new private evidence directory for result mode")
    parser.add_argument("--format", choices=("text", "json", "result"), default="json")
    options = parser.parse_args(argv)
    feedback_args = {flag: getattr(options, flag[2:].replace("-", "_")) for flag in FEEDBACK_FLAGS
                     if getattr(options, flag[2:].replace("-", "_")) is not None}
    if any(not value or value.strip() != value or any(c in value for c in "\x00\r\n") for value in feedback_args.values()):
        parser.error("feedback arguments require nonempty, single-line values")
    if feedback_args and (options.inspect or options.check):
        parser.error("feedback arguments require task execution")
    if options.feedback_config_dir and not Path(options.feedback_config_dir).is_absolute():
        parser.error("--feedback-config-dir must be absolute")
    if options.inspect and (options.message or options.resume or options.resume_from or options.check or options.output_dir):
        parser.error("--inspect cannot be combined with execution options")
    if options.check and (options.message or options.resume or options.resume_from or options.output_dir):
        parser.error("--check cannot be combined with execution options")
    if options.resume and options.resume_from:
        parser.error("choose --resume or --resume-from")
    if options.output_dir and options.format != "result":
        parser.error("--output-dir requires --format result")
    try:
        if options.inspect:
            print(json.dumps(inspect_receipt(options.inspect, private_json), ensure_ascii=False))
            return 0
        command, cwd, env = configured_command(options.config)
        if options.check:
            print(json.dumps({"status": "configured", "command": command, "cwd": cwd, "credentials": "loaded; values hidden"}))
            return 0
        if not options.message:
            parser.error("provide -m MESSAGE, or --check for local configuration validation")
        if len(options.message.encode("utf-8")) > 16 * 1024:
            parser.error("message exceeds the CLI limit of 16 KiB")
        if options.resume_from:
            previous = private_json(options.resume_from.absolute() / "launch.json")
            inherited = previous.get("feedback_args", {})
            if not isinstance(inherited, dict) or not inherited.keys() <= set(FEEDBACK_FLAGS) or not all(isinstance(v, str) and v for v in inherited.values()):
                raise HandoffError("Invalid saved feedback metadata; inspect the original receipt.")
            feedback_args = {**inherited, **feedback_args}
            # The command stored in a receipt is the pre-message destination.
            if previous["destination_key"] != destination_key(command, cwd):
                raise HandoffError("Destination changed; restore the original configuration before continuing.")
            if previous["exit_code"] is None:
                raise HandoffError("Previous process exit is unobserved; inspect its evidence and process before continuing.")
            observed = inspect_receipt(options.resume_from, private_json)
            options.resume = observed["ref"]["session_id"]
            if not options.resume:
                raise HandoffError("No observed session to continue; inspect the launch failure first.")
        base_command = command.copy()
        for flag, value in feedback_args.items():
            command += [flag, value]
        command += ["--format", "json" if options.format == "result" else options.format, "-m", options.message]
        if options.resume:
            command += ["--resume", options.resume]
        if options.format == "result":
            return run_result(command, cwd, env, options.config.absolute(), options.output_dir, base_command, feedback_args)
        result = subprocess.run(command, cwd=cwd, env=env, check=False)
        return result.returncode if result.returncode >= 0 else 128 - result.returncode
    except HandoffError as error:
        print(str(error), file=sys.stderr)
        return 2
    except (OSError, ValueError, KeyError, TypeError, sqlite3.Error) as error:
        print(f"Handoff configuration failed ({type(error).__name__}); check the private configuration and referenced files.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
