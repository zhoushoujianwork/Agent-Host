#!/usr/bin/env python3
"""Observe real statement writes and fresh-session recall in isolated native chat.

Uses the existing explicit company credential loader; does not execute aliases,
replay failures, run tools, or alter any evaluation label or policy threshold.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("experience_experiment", ROOT / "experiments/experience/run.py")
existing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(existing)


def retain_output(records, save, label, completed, stdout, stderr, duration_ms, timed_out=False, **metadata):
    """Persist command evidence before parsing or checking its outcome."""
    record = {"label": label, "duration_ms": duration_ms, "exit_code": completed,
              "timed_out": timed_out, "stdout": stdout, "stderr": stderr, **metadata}
    records.append(record)
    save()
    return record


def decode_output(record, save, field, ndjson=False):
    """Replace raw stdout only after the entire output has parsed successfully."""
    raw = record["stdout"]
    parsed = ([json.loads(line) for line in raw.splitlines() if line.strip()]
              if ndjson else json.loads(raw))
    record[field] = parsed
    del record["stdout"]
    save()
    return parsed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--credentials", choices=["env", "llm-alias"], default="env")
    parser.add_argument("--claude-config", choices=["settings", "llm-alias"], default="settings")
    parser.add_argument("--model")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise ValueError("Output exists; preserve the earlier report")
    key = existing.JEV.load_key(args.credentials)
    native, configured_model = existing.native_configuration(args.claude_config)
    model = args.model or native.get("ANTHROPIC_MODEL") or configured_model or "sonnet"
    env = os.environ.copy()
    env.update(native)
    env["PATSNAP_API_KEY"] = key
    secrets = [v for k, v in env.items() if any(x in k.upper() for x in ["TOKEN", "SECRET", "API_KEY", "AUTH_KEY"])]
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "provider": "patsnap", "requested_claude_model": model,
              "native_version": subprocess.check_output(["claude", "--version"], text=True).strip(),
              "binary_sha256": hashlib.sha256((ROOT / "bin/agent-host").read_bytes()).hexdigest(),
              "isolation": "temporary workspace and host state; fresh native config/session per turn; no tools",
              "scope": "real statement admission, write annotations, retrieval and context delivery; not a causal task-benefit experiment",
              "commands": [], "assertions": [], "passed": False, "retries": 0}
    temporary_root = ""

    def save():
        raw = json.dumps(report, ensure_ascii=False, indent=2)
        for secret in secrets:
            if secret:
                raw = raw.replace(secret, "[REDACTED]")
        if temporary_root:
            raw = raw.replace(temporary_root, "<isolated-root>")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(raw + "\n")
        args.output.chmod(0o600)

    def require(condition, message):
        report["assertions"].append({"check": message, "passed": bool(condition)})
        if not condition:
            raise AssertionError(message)

    def inspect_command(record, label, command):
        inspections = record.setdefault("inspections", [])
        started = time.monotonic()
        try:
            completed = subprocess.run(command, capture_output=True, text=True, timeout=20)
        except subprocess.TimeoutExpired as exc:
            def text(value):
                return value.decode("utf-8", errors="replace") if isinstance(value, bytes) else value or ""
            retain_output(inspections, save, label, None, text(exc.stdout), text(exc.stderr),
                          round((time.monotonic()-started)*1000), timed_out=True, command=command)
            raise
        return retain_output(inspections, save, label, completed.returncode, completed.stdout,
                             completed.stderr, round((time.monotonic()-started)*1000), command=command)

    with tempfile.TemporaryDirectory(prefix="agent-host-retrieval-") as temp:
        temporary_root = temp
        root = Path(temp)
        project, home = root / "project", root / "host"
        project.mkdir()
        saved_ids = {}
        statements = ["订单服务的数据库使用 PostgreSQL。", "账单服务的数据库使用 MySQL。", "密钥每九十天更换一次。"]
        tasks = [("store-orders", statements[0], None), ("store-billing", statements[1], None),
                 ("store-rotation", statements[2], None),
                 ("query-orders", "订单服务用什么数据库？请仅根据提供的记忆回答，并引用记忆 ID。", statements[0]),
                 ("query-rotation", "凭据的轮换周期是多少？请仅根据提供的记忆回答，并引用记忆 ID。", statements[2])]
        try:
            for label, message, expected in tasks:
                config = root / ("native-" + label)
                config.mkdir()
                (config / "settings.json").write_text(json.dumps({"disableAllHooks": True, "autoMemoryEnabled": False}))
                child_env = dict(env, CLAUDE_CONFIG_DIR=str(config))
                command = [str(ROOT / "bin/agent-host"), "chat", "--home", str(home), "--workspace", str(project),
                           "--format", "json", "--no-tools", "--model", model, "--timeout", "3m", "-m", message]
                print(json.dumps({"case": label, "status": "running"}), flush=True)
                start = time.monotonic()
                proc = subprocess.Popen(command, env=child_env, cwd=project, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True, start_new_session=True)
                timed_out = False
                try:
                    stdout, stderr = proc.communicate(timeout=200)
                except subprocess.TimeoutExpired:
                    timed_out = True
                    os.killpg(proc.pid, signal.SIGINT)
                    try:
                        stdout, stderr = proc.communicate(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL)
                        stdout, stderr = proc.communicate()
                record = retain_output(report["commands"], save, label, proc.returncode, stdout, stderr,
                                       round((time.monotonic()-start)*1000), timed_out=timed_out,
                                       input=message, command=command)
                decode_output(record, save, "events", ndjson=True)
                require(proc.returncode == 0 and not timed_out, label + ": command completed")
                run_id = record["events"][0]["run_id"]
                inspect = inspect_command(record, "run-show", [str(ROOT / "bin/agent-host"), "run", "show",
                                          "--home", str(home), "--workspace", str(project), run_id])
                require(inspect["exit_code"] == 0, label + ": run is inspectable")
                run = decode_output(inspect, save, "result")
                record["run"] = run
                require(run["acceptance_status"] == "unverified", label + ": execution did not assert business acceptance")
                if expected is None:
                    require(run["memory_status"] == "saved", label + ": real Jev admitted exact source statement")
                    search = inspect_command(record, "memory-search", [str(ROOT / "bin/agent-host"), "memory", "search",
                                             "--home", str(home), "--workspace", str(project), message])
                    require(search["exit_code"] == 0, label + ": saved memory searchable")
                    exact = [m for m in decode_output(search, save, "result") if m["text"] == message]
                    require(len(exact) == 1, label + ": one exact source retained")
                    saved_ids[message] = exact[0]["id"]
                else:
                    require([m["id"] for m in run["context"]] == [saved_ids[expected]], label + ": only applicable memory injected")
                    require(saved_ids[expected] in run["output"], label + ": native answer cited selected memory")
                    require(any(e["type"] == "memory.query.analyzed" for e in record["events"]), label + ": query analysis observable")
                    require(any(e["type"] == "memory.retrieval.evaluated" for e in record["events"]), label + ": candidate filtering observable")
                save()
                print(json.dumps({"case": label, "status": "passed", "duration_ms": record["duration_ms"]}), flush=True)
            report["passed"] = True
        except Exception as exc:
            report["error"] = str(exc)
            raise
        finally:
            save()
    print(json.dumps({"passed": report["passed"], "assertions": len(report["assertions"])}), flush=True)


if __name__ == "__main__":
    main()
