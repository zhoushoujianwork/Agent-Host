#!/usr/bin/env python3
"""Probe real CLI/Web ACP, MCP, Skills and automatic recall in private temp state."""

import argparse
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tools/run-viewer"))
from chat_runtime import ChatRuntime, PROVIDER_KEYS, configured_launch


def require(value, message):
    if not value:
        raise AssertionError(message)


def private_json(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--connection", type=Path, required=True)
    parser.add_argument("--execution-config", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    require(not args.output.exists(), "choose a fresh report")
    # Validate the explicitly selected connection; reuse only its provider flags
    # and environment, never its Agent, state, tools, workspace or native profile.
    command, _, env, _ = configured_launch(args.connection.resolve())
    flags = ("--claude", "--model", "--jev-provider", "--jev-model", "--jev-endpoint")
    provider = [word for flag in flags if flag in command for word in (flag, command[command.index(flag) + 1])]
    binary = ROOT / "bin/agent-host"
    report = {"passed": False, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "isolation": "temporary Host/files/native/workspace/local datasource; no channel messages", "turns": []}
    secrets = [v for k, v in env.items() if any(s in k.upper() for s in ("TOKEN", "SECRET", "API_KEY", "AUTH_KEY"))]
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="agh-architecture-") as temporary:
        root = Path(temporary).resolve()
        def save():
            report["elapsed_seconds"] = round(time.monotonic() - started, 2)
            raw = json.dumps(report, ensure_ascii=False, indent=2).replace(str(root), "<isolated-root>")
            for secret in secrets:
                if secret:
                    raw = raw.replace(secret, "[REDACTED]")
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(raw + "\n")
            args.output.chmod(0o600)
        runtime = None
        try:
            for name in ("state", "native/skills/architecture-probe", "workspace", "source/notes"):
                (root / name).mkdir(parents=True)
            (root / "native/skills/architecture-probe/SKILL.md").write_text(
                "---\nname: architecture-probe\ndescription: Read-only architecture acceptance marker.\n---\n"
                "When explicitly invoked, include SKILL_ARCHITECTURE_OK in the answer.\n")
            marker = "DOC_" + uuid.uuid4().hex
            (root / "source/notes/probe.md").write_text("# Architecture probe\nThe datasource marker is " + marker + ".\n")
            private_json(root / "knowledge.json", {"version": 1, "sources": [{"name": "probe_docs", "provider": "local_markdown",
                "root": str(root / "source"), "paths": ["notes"], "projects": ["default"],
                "principal": "local-owner", "description": "Synthetic acceptance document"}]})
            private_json(root / "agents.json", {"default_agent": "probe", "agents": [{"id": "probe", "name": "Architecture probe",
                "projects": [], "native_skills": ["architecture-probe"], "execution_config": str(args.execution_config.resolve())}]})
            private_json(root / "environment.json", {k: v for k, v in env.items() if k in PROVIDER_KEYS})
            base = ["--home", str(root / "state"), "--agents-config", str(root / "agents.json"), "--agent", "probe",
                "--workspace", str(root / "workspace"), "--native-profile", str(root / "native"), "--memory-scope", "architecture:probe",
                "--knowledge-config", str(root / "knowledge.json"), "--disable-dingtalk-workspace", "--timeout", "5m", *provider]
            wrapper = root / "capture-host"
            wrapper.write_text("#!" + sys.executable + "\nimport os, sys\nf = os.open(" + repr(str(root / "web-stderr.log")) +
                ", os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)\nos.dup2(f, 2)\nos.execv(" + repr(str(binary)) + ", [" + repr(str(binary)) + ", *sys.argv[1:]])\n")
            wrapper.chmod(0o700)
            private_json(root / "connection.json", {"binary": str(wrapper), "cwd": str(root / "workspace"), "args": base,
                "environment_file": str(root / "environment.json")})
            migrate = subprocess.run([str(binary), "memory", "migrate", "--home", str(root / "state"), "--memory-root", str(root / "memory"),
                "--memory-cache", str(root / "cache/memory"), "--memory-source", "architecture-probe"], capture_output=True, text=True)
            require(migrate.returncode == 0, "isolated memory migration failed: " + migrate.stderr)
            codename = "ARCHIVE_" + uuid.uuid4().hex
            statement = "The permanent codename of my architecture acceptance project is " + codename + "."
            cli = subprocess.run([str(binary), "chat", *base, "--format", "json", "-m", statement],
                cwd=root / "workspace", env=env, capture_output=True, text=True, timeout=360)
            events = [json.loads(line) for line in cli.stdout.splitlines() if line.strip()]
            report["turns"].append({"entry": "cli", "exit_code": cli.returncode, "events": events, "stderr": cli.stderr})
            save()
            require(cli.returncode == 0, "CLI run failed; inspect report")
            receipt = next(e["data"] for e in reversed(events) if e["type"] == "run.finished")
            require(receipt["memory_status"] == "saved", "declaration was not automatically saved")
            runtime = ChatRuntime(root / "connection.json")
            runtime.start("web-fresh", "What is the permanent codename of my architecture acceptance project? "
                "Use recalled Host memory. Invoke the architecture-probe Skill, then use the supplied MCP "
                "knowledge search/read tools to find the Architecture probe document and report its datasource marker. "
                "Do not read datasource files directly. Do not change files or contact any external account.")
            runtime.worker.join(timeout=360)
            if runtime.worker.is_alive():
                runtime.stop(runtime.active)
                runtime.worker.join(timeout=30)
                raise TimeoutError("Web execution exceeded probe deadline")
            web = runtime.snapshot()["turns"][-1]
            report["turns"].append({"entry": "web-fresh", "projection": web})
            report["web_stderr"] = (root / "web-stderr.log").read_text()
            save()
            require(web["exit_code"] == 0, "Web run failed; inspect report")
            require(codename in web["text"], "fresh Web conversation did not use CLI-learned memory")
            require(marker in web["text"], "MCP document marker missing")
            require("SKILL_ARCHITECTURE_OK" in web["text"], "native Skill marker missing")
            with sqlite3.connect(root / "state/state.db") as db:
                saved = json.loads(db.execute("SELECT payload FROM runs WHERE id=?", (web["run_id"],)).fetchone()[0])
                report["web_receipt"] = saved
                recorded = [json.loads(row[0]) for row in db.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (web["run_id"],))]
            report["web_events"] = recorded
            tools = [e.get("data", {}).get("tool", {}) for e in recorded if e["type"] == "tool.activity"]
            report["observed_tool_names"] = sorted({t.get("name", "") for t in tools})
            successful = [t for t in tools if t.get("stage") == "result" and t.get("is_error") is False]
            require(any(t.get("name") == "Skill" for t in successful), "successful Skill result was not journaled")
            require(any(t.get("name", "").startswith("mcp__") for t in successful), "successful MCP result was not journaled")
            require(codename in json.dumps(saved.get("context")), "Host receipt has no recalled declaration")
            cli_session = next(e["session_id"] for e in events if e["type"] == "run.started")
            require(saved["session_id"] != cli_session, "Web reused CLI conversation instead of durable recall")
            runtime.start("web-continue", "Reply exactly CONTINUATION_OK. No tools are needed.")
            runtime.worker.join(timeout=360)
            require(not runtime.worker.is_alive(), "continuation exceeded deadline")
            continued = runtime.snapshot()["turns"][-1]
            report["turns"].append({"entry": "web-continue", "projection": continued})
            require(continued["exit_code"] == 0 and "CONTINUATION_OK" in continued["text"], "Web continuation failed")
            require(continued["session_id"] == web["session_id"], "compatible Web continuation rotated Host session")
            with sqlite3.connect(root / "state/state.db") as db:
                continuation = json.loads(db.execute("SELECT payload FROM runs WHERE id=?", (continued["run_id"],)).fetchone()[0])
            require(continuation["native_session_id"] == saved["native_session_id"], "compatible ACP native session changed")
            report["continuation_receipt"] = continuation
            report["passed"] = True
        except Exception as error:
            report["error"] = str(error)
            raise
        finally:
            if runtime and runtime.active:
                runtime.stop(runtime.active)
                runtime.worker.join(timeout=30)
            save()
    print(json.dumps({k: report[k] for k in ("passed", "elapsed_seconds")}))


if __name__ == "__main__":
    main()
