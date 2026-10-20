"""Verify configured handoffs do not inherit the wrong provider or replay failures."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import signal
import sqlite3
import time
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("handoff_chat", Path(__file__).with_name("chat.py"))
CHAT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHAT)


class HandoffTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="handoff with spaces ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.credentials = self.root / "credentials.json"
        self.credentials.write_text(json.dumps({"ANTHROPIC_AUTH_TOKEN": "managed-token", "JEV_API_KEY": "managed-jev"}))
        self.credentials.chmod(0o600)
        agents = self.root / "agents.json"
        agents.write_text(json.dumps({"agents": [{"id": "daily", "projects": []}]}))
        agents.chmod(0o600)
        self.config = self.root / "handoff.json"
        self.value = {"binary": sys.executable, "cwd": str(self.root), "environment_file": str(self.credentials),
                      "args": ["--home", str(self.root / "cli-state"), "--agents-config", str(self.root / "agents.json"),
                               "--agent", "daily", "--workspace", str(self.root), "--native-profile", str(self.root / "native")]}
        self.save()

    def save(self):
        self.config.write_text(json.dumps(self.value))
        self.config.chmod(0o600)

    def test_exact_message_and_resume_preserve_failure_without_replay(self):
        message = "Read $HOME; $(touch never); `literal`\nNo writes."
        with patch.object(CHAT.subprocess, "run", return_value=subprocess.CompletedProcess([], 7)) as run:
            self.assertEqual(CHAT.main(["--config", str(self.config), "-m", message, "--resume", "session-1"]), 7)
        run.assert_called_once()
        args = run.call_args.args[0]
        self.assertEqual(args[args.index("-m") + 1], message)
        self.assertEqual(args[-2:], ["--resume", "session-1"])
        self.assertNotIn("shell", run.call_args.kwargs)

    def test_managed_environment_replaces_inherited_credentials(self):
        with patch.dict(os.environ, {"ANTHROPIC_AUTH_TOKEN": "wrong", "CLAUDE_CODE_OAUTH_TOKEN": "wrong", "CLAUDECODE": "1", "PATH": "/kept"}):
            _, _, env = CHAT.configured_command(self.config)
        self.assertEqual(env["ANTHROPIC_AUTH_TOKEN"], "managed-token")
        self.assertEqual(env["PATH"], "/kept")
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", env)
        self.assertNotIn("CLAUDECODE", env)

    def test_check_does_not_execute_or_disclose_credentials(self):
        output = io.StringIO()
        with patch.object(CHAT.subprocess, "run") as run, contextlib.redirect_stdout(output):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--check"]), 0)
        run.assert_not_called()
        self.assertNotIn("managed-token", output.getvalue())
        self.assertNotIn("managed-jev", output.getvalue())

    def test_insecure_or_incomplete_configuration_never_starts_worker(self):
        with patch.object(CHAT.subprocess, "run") as run, contextlib.redirect_stderr(io.StringIO()):
            self.credentials.chmod(0o644)
            self.assertEqual(CHAT.main(["--config", str(self.config), "-m", "task"]), 2)
            self.credentials.chmod(0o600)
            self.value["args"] = ["--agent", "daily"]
            self.save()
            self.assertEqual(CHAT.main(["--config", str(self.config), "-m", "task"]), 2)
        run.assert_not_called()

    def test_channel_only_project_bindings_are_rejected_without_starting_host(self):
        agents = self.root / "agents.json"
        agents.write_text(json.dumps({"agents": [{"id": "daily", "projects": ["channel-only"]}]}))
        with patch.object(CHAT.subprocess, "run") as run, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--check"]), 2)
        run.assert_not_called()

    def fake_host(self, **behavior):
        (self.root / "behavior.json").write_text(json.dumps(behavior))
        binary = self.root / "host"
        binary.write_text(f"#!{sys.executable}\n" + '''
import json, pathlib, sys, time
root = pathlib.Path(__file__).parent
config = json.loads((root / "behavior.json").read_text())
with (root / "calls.jsonl").open("a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
if config.get("preflight_error") and sys.argv[1] == "chat":
    print("session cannot resume after unknown execution", file=sys.stderr)
    sys.exit(1)
run = {"id": "run-1", "session_id": "actual-session", "output": config.get("report", "BLOCKED: account denied; no changes"),
       "execution_status": "completed", "acceptance_status": "unreviewed", "memory_status": "failed", "memory_error": "memory unavailable", "model": "fixture-model"}
run["feedback"] = config.get("feedback")
if sys.argv[1:3] == ["run", "show"]:
    if config.get("inspect_failure"):
        sys.exit(3)
    if not (root / "finished").exists():
        run.update(output="", execution_status="running", memory_status="pending_execution")
    elif "canonical_report" in config:
        run["output"] = config["canonical_report"]
    print(json.dumps(run))
    sys.exit(0)
def emit(kind, **fields):
    print(json.dumps({"type": kind, "run_id": "run-1", "session_id": "actual-session", **fields}), flush=True)
emit("run.started")
if run["feedback"]:
    emit("feedback.bound", data={"feedback": run["feedback"]})
time.sleep(config.get("sleep", 0))
emit("text.delta", phase="execute", component="harness.claude", delta="Checking...")
emit("agent.result", phase="execute", component="harness.claude", model="fixture-model", data={"raw": {"result": run["output"]}})
for event in config.get("accounting_events", []):
    print(json.dumps(event), flush=True)
emit("text.delta", phase="extract", component="harness.claude.extractor", delta="Secret memory extraction")
emit("run.finished", data={key: run[key] for key in ("execution_status", "acceptance_status", "memory_status", "memory_error")})
(root / "finished").touch()
print("fixture stderr", file=sys.stderr)
sys.exit(config.get("exit_code", 0))
''')
        binary.chmod(0o700)
        self.value["binary"] = str(binary)
        self.save()

    def result_launch(self, directory, *extra):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            code = CHAT.main(["--config", str(self.config), "--format", "result", "--output-dir", str(directory), "-m", "Exact $HOME `literal` task", *extra])
        return code, json.loads(output.getvalue())

    def calls(self):
        return [json.loads(line) for line in (self.root / "calls.jsonl").read_text().splitlines()]

    def test_result_preserves_blocked_report_memory_failure_and_exit(self):
        self.fake_host(exit_code=7)
        directory = self.root / "evidence"
        code, result = self.result_launch(directory)
        self.assertEqual(code, 7)
        self.assertEqual(result["execution"]["exit_code"], 7)
        self.assertEqual(result["execution"]["memory_status"], "failed")
        self.assertEqual(result["report"], "BLOCKED: account denied; no changes")
        self.assertEqual(result["ref"]["session_id"], "actual-session")
        self.assertIsNone(result["cost_usd"])
        self.assertNotIn("Secret memory", result["report"])
        self.assertEqual((directory / "stderr.log").read_text().strip(), "fixture stderr")
        self.assertEqual(result["stderr_tail"].strip(), "fixture stderr")
        self.assertEqual(directory.stat().st_mode & 0o077, 0)
        self.assertEqual((directory / "launch.json").stat().st_mode & 0o077, 0)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_inspect_needs_no_credentials_and_never_starts_chat(self):
        self.fake_host()
        directory = self.root / "evidence"
        self.result_launch(directory)
        self.credentials.unlink()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(CHAT.main(["--inspect", str(directory)]), 0)
        self.assertEqual(json.loads(output.getvalue())["execution"]["exit_code"], 0)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def accounting_event(self, phase, session, **fields):
        return {"type": "agent.result" if phase == "execute" else "extraction.evidence",
                "run_id": "run-1", "phase": phase, "model": "fixture-model",
                "component": "harness.claude" if phase == "execute" else "harness.claude.extractor",
                "data": {"raw": {"type": "result", "session_id": session, **fields}}}

    def test_usage_aggregates_terminal_calls_without_stream_or_model_double_counting(self):
        execute = self.accounting_event("execute", "task-session", uuid="task-result",
            usage={"input_tokens": 10, "output_tokens": 3, "cache_creation_input_tokens": 20, "cache_read_input_tokens": 30},
            total_cost_usd=0.12, modelUsage={"fixture-model": {"inputTokens": 999, "costUSD": 99}})
        extract = self.accounting_event("extract", "memory-session", uuid="memory-result",
            usage={"input_tokens": 4, "output_tokens": 2, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
            total_cost_usd=0.03)
        stream = self.accounting_event("execute", "task-session", usage={"input_tokens": 10000}, total_cost_usd=100)
        stream["data"]["raw"]["type"] = "stream_event"
        other = self.accounting_event("execute", "other", total_cost_usd=100)
        other["component"] = "unrelated"
        self.fake_host(accounting_events=[stream, execute, execute, extract, other])
        directory = self.root / "evidence"
        _, result = self.result_launch(directory)
        self.assertAlmostEqual(result["cost_usd"], 0.15)
        usage = result["usage"]
        self.assertEqual(usage["tokens"], {"input_tokens": 14, "output_tokens": 5,
                         "cache_creation_input_tokens": 20, "cache_read_input_tokens": 30})
        self.assertEqual(len(usage["calls"]), 2)
        self.assertEqual(usage["calls"][0]["model_usage"]["fixture-model"]["inputTokens"], 999)
        self.assertEqual(usage["cost_basis"], "native_reported_estimate")
        self.assertIn("jev", usage["excludes"])
        self.credentials.unlink()
        inspected = CHAT.inspect_receipt(directory, CHAT.private_json)
        self.assertEqual(inspected["usage"], usage)
        self.assertEqual(inspected["cost_usd"], result["cost_usd"])
        self.assertEqual(json.loads((directory / "result.json").read_text())["usage"], usage)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_unknown_and_invalid_usage_stays_unknown_while_reported_zero_is_kept(self):
        valid = self.accounting_event("execute", "task", usage={"input_tokens": 0, "output_tokens": 2}, total_cost_usd=0)
        invalid = self.accounting_event("extract", "memory", usage={"input_tokens": True, "output_tokens": -1,
                    "cache_read_input_tokens": "7"}, total_cost_usd=float("nan"))
        self.fake_host(accounting_events=[valid, invalid])
        _, result = self.result_launch(self.root / "evidence")
        self.assertEqual(result["cost_usd"], 0)
        self.assertEqual(result["usage"]["missing_cost_calls"], 1)
        self.assertEqual(result["usage"]["tokens"]["input_tokens"], 0)
        self.assertIsNone(result["usage"]["tokens"]["cache_read_input_tokens"])
        self.assertEqual(result["usage"]["missing_token_calls"]["input_tokens"], 1)
        self.assertIsNone(result["usage"]["calls"][1]["cost_usd"])

    def test_old_events_without_usage_do_not_invent_zero_consumption(self):
        self.fake_host()
        _, result = self.result_launch(self.root / "evidence")
        self.assertIsNone(result["cost_usd"])
        self.assertTrue(all(value is None for value in result["usage"]["tokens"].values()))
        self.assertEqual(result["usage"]["calls"], [])

    def test_canonical_output_keeps_host_notices(self):
        self.fake_host(canonical_report="BLOCKED: account denied. Host notice retained.")
        _, result = self.result_launch(self.root / "evidence")
        self.assertEqual(result["report"], "BLOCKED: account denied. Host notice retained.")

    def test_feedback_metadata_is_carried_into_continuation_and_receipts(self):
        self.fake_host(feedback={"mode": "notify", "request_for": "alice", "notify_to": "alice"})
        directory = self.root / "first"
        config_dir = self.root / "config"
        database_dir = self.root / "feedback"
        database_dir.mkdir(mode=0o700)
        database = database_dir / "state.db"
        with sqlite3.connect(database) as connection:
            connection.execute("CREATE TABLE feedback(payload BLOB)")
            connection.execute("INSERT INTO feedback VALUES(?)", (json.dumps({"run_id": "run-1", "status": "accepted", "receipt": "platform-receipt"}),))
        flags = ["--request-for", "alice", "--notify-channel", "feishu", "--feedback-config-dir", str(config_dir)]
        _, result = self.result_launch(directory, *flags)
        self.assertEqual(result["feedback"]["notify_to"], "alice")
        self.assertEqual(result["notifications"][0]["status"], "accepted")
        self.result_launch(self.root / "second", "--resume-from", str(directory))
        chats = [call for call in self.calls() if call[0] == "chat"]
        for call in chats:
            for flag in ("--request-for", "--notify-channel", "--feedback-config-dir"):
                self.assertEqual(call.count(flag), 1)
                self.assertEqual(call[call.index(flag) + 1], flags[flags.index(flag) + 1])
        self.config.unlink()
        self.credentials.unlink()
        before = database.stat().st_mtime_ns
        inspected = CHAT.inspect_receipt(directory, CHAT.private_json)
        self.assertEqual(inspected["notifications"][0]["receipt"], "platform-receipt")
        self.assertEqual(database.stat().st_mtime_ns, before)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 2)

    def test_resume_uses_observed_session_and_rejects_destination_changes(self):
        self.fake_host()
        directory = self.root / "first"
        self.result_launch(directory)
        self.result_launch(self.root / "second", "--resume-from", str(directory))
        chats = [call for call in self.calls() if call[0] == "chat"]
        self.assertEqual(chats[1][-2:], ["--resume", "actual-session"])
        self.assertEqual(chats[1][chats[1].index("-m") + 1], "Exact $HOME `literal` task")
        self.value["args"] += ["--model", "different"]
        self.save()
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--resume-from", str(directory), "-m", "continue"]), 2)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 2)

    def test_unavailable_host_and_truncated_events_preserve_partial_evidence(self):
        self.fake_host(inspect_failure=True)
        directory = self.root / "evidence"
        _, result = self.result_launch(directory)
        self.assertIn("BLOCKED", result["report"])
        self.assertTrue(result["warnings"])
        receipt = CHAT.private_json(directory / "launch.json")
        receipt["exit_code"] = None
        CHAT.save_json(directory / "launch.json", receipt)
        (directory / "events.jsonl").write_text('{"type":"run.started","run_id":"run-1","session_id":"actual-session"}\n{"type":')
        result = CHAT.inspect_receipt(directory, CHAT.private_json)
        self.assertEqual(result["report"], "")
        self.assertIsNone(result["execution"]["exit_code"])
        self.assertIn("incomplete_or_invalid_events", result["warnings"])
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--resume-from", str(directory), "-m", "continue"]), 2)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_termination_preserves_evidence_and_exit_without_replay(self):
        self.fake_host(sleep=30)
        directory = self.root / "interrupted"
        launcher = subprocess.Popen([sys.executable, "-B", str(Path(CHAT.__file__)), "--config", str(self.config),
                                     "--format", "result", "--output-dir", str(directory), "-m", "wait"],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                events = directory / "events.jsonl"
                if events.exists() and events.stat().st_size:
                    break
                time.sleep(0.02)
            else:
                self.fail("fixture never started")
            launcher.send_signal(signal.SIGTERM)
            output, _ = launcher.communicate(timeout=5)
            self.assertEqual(launcher.returncode, 143)
            self.assertEqual(json.loads(output)["execution"]["exit_code"], 143)
            self.assertEqual(json.loads(output)["report"], "")
            self.assertTrue((directory / "events.jsonl").read_text())
            self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)
        finally:
            if launcher.poll() is None:
                launcher.kill()
                launcher.communicate()

    def test_saved_task_or_duplicate_destination_overrides_are_rejected(self):
        original = self.value["args"].copy()
        for override in (["-m", "unexpected"], ["--message=unexpected"], ["-format", "text"], ["--home=/other"]):
            self.value["args"] = original + override
            self.save()
            with contextlib.redirect_stderr(io.StringIO()), patch.object(CHAT.subprocess, "run") as run:
                self.assertEqual(CHAT.main(["--config", str(self.config), "--check"]), 2)
            run.assert_not_called()

    def test_inspect_rejects_execution_options_and_existing_output_is_preserved(self):
        self.fake_host()
        directory = self.root / "evidence"
        self.result_launch(directory)
        old_receipt = (directory / "launch.json").read_bytes()
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--format", "result", "--output-dir", str(directory), "-m", "again"]), 2)
            with self.assertRaises(SystemExit):
                CHAT.main(["--inspect", str(directory), "-m", "again"])
        self.assertEqual((directory / "launch.json").read_bytes(), old_receipt)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_preflight_failure_surfaces_error_without_inventing_actual_ids(self):
        self.fake_host(preflight_error=True)
        code, result = self.result_launch(self.root / "evidence", "--resume", "requested-session")
        self.assertEqual(code, 1)
        self.assertIn("cannot resume", result["stderr_tail"])
        self.assertEqual(result["requested_session_id"], "requested-session")
        self.assertIsNone(result["ref"]["session_id"])
        self.assertIsNone(result["ref"]["run_id"])

    def test_inspection_rejects_a_mutating_receipt_command(self):
        self.fake_host()
        directory = self.root / "evidence"
        self.result_launch(directory)
        receipt = CHAT.private_json(directory / "launch.json")
        receipt["inspect_command"] = [self.value["binary"], "chat", "-m", "unexpected"]
        CHAT.save_json(directory / "launch.json", receipt)
        with patch.object(CHAT.subprocess, "run") as run:
            result = CHAT.inspect_receipt(directory, CHAT.private_json)
        run.assert_not_called()
        self.assertTrue(result["warnings"])


if __name__ == "__main__":
    unittest.main()
