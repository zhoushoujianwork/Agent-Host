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

    def test_offload_destination_separates_native_and_classifier_credentials(self):
        profile=self.root/"offload.json"
        native=self.root/"native.json";native.write_text(json.dumps({"profiles":{"local":{"backend":"codex"}}}));native.chmod(0o600)
        profile.write_text(json.dumps({"version":2,"config_path":str(native),"profile":"local"}));profile.chmod(0o600)
        self.value["args"]=["--home",str(self.root/"cli-state"),"--workspace",str(self.root),"--agent","daily","--offload-config",str(profile)]
        self.value.pop("environment_file")
        self.save()
        with patch.dict(os.environ,{"ANTHROPIC_API_KEY":"inherited-native","JEV_API_KEY":"inherited-classifier"}):
            command,_,env=CHAT.configured_command(self.config)
        self.assertIn("--offload-config",command)
        self.assertNotIn("ANTHROPIC_API_KEY",env)
        self.assertNotIn("JEV_API_KEY",env)
        self.credentials.write_text(json.dumps({"JEV_API_KEY":"selected-classifier"}))
        self.value["environment_file"]=str(self.credentials);self.save()
        self.assertEqual(CHAT.configured_command(self.config)[2]["JEV_API_KEY"],"selected-classifier")
        self.credentials.write_text(json.dumps({"ANTHROPIC_API_KEY":"not-allowed"}))
        with self.assertRaises(ValueError):CHAT.configured_command(self.config)

    def test_exact_message_and_resume_preserve_failure_without_replay(self):
        message = "Read $HOME; $(touch never); `literal`\nNo writes."
        self.fake_host(exit_code=7)
        directory = self.root / "evidence"
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(CHAT.main(["--config", str(self.config), "--output-dir", str(directory),
                                       "-m", message, "--resume", "session-1"]), 7)
        args = self.calls()[0]
        self.assertEqual(args[args.index("-m") + 1], message)
        self.assertEqual(args[-2:], ["--resume", "session-1"])
        self.assertEqual(args[args.index("--caller-pid") + 1], str(os.getppid()))
        self.assertEqual((directory / "request.txt").read_bytes(), message.encode("utf-8"))
        self.assertIn(message, (directory / "execution.md").read_text())

    def test_offload_package_destination_needs_no_cli_and_keeps_native_credentials_private(self):
        native = self.root / "native.json"
        native.write_text(json.dumps({"profiles": {"local": {"backend": "codex", "token": "native-only-secret"}}}))
        native.chmod(0o600)
        profile = self.root / "offload-pkg.json"
        value = {"version": 2, "config_path": str(native), "profile": "local"}
        profile.write_text(json.dumps(value)); profile.chmod(0o600)
        self.value["args"] = ["--home", str(self.root/"cli-state"), "--workspace", str(self.root), "--agent", "daily", "--offload-config", str(profile)]
        self.value.pop("environment_file"); self.save()
        command, _, env = CHAT.configured_command(self.config)
        self.assertIn("--offload-config", command)
        self.assertNotIn("native-only-secret", json.dumps([command, env]))
        native.chmod(0o644)
        with self.assertRaises(ValueError): CHAT.configured_command(self.config)
        native.chmod(0o600)
        value["command"] = sys.executable
        profile.write_text(json.dumps(value))
        with self.assertRaises(ValueError): CHAT.configured_command(self.config)

    def test_full_host_accepts_agent_offload_profile(self):
        native = self.root / "native.json"
        native.write_text(json.dumps({"profiles": {"local": {"backend": "claude", "model": "fixture"}}}))
        native.chmod(0o600)
        profile = self.root / "offload-pkg.json"
        profile.write_text(json.dumps({"version": 2, "config_path": str(native), "profile": "local"}))
        profile.chmod(0o600)
        agents = Path(self.value["args"][self.value["args"].index("--agents-config") + 1])
        agents.write_text(json.dumps({"agents": [{"id": "daily", "projects": [], "offload_config": profile.name}]}))
        agents.chmod(0o600)
        self.save()
        command, _, env = CHAT.configured_command(self.config)
        self.assertIn("--agents-config", command)
        self.assertNotIn("--offload-config", command)
        self.assertEqual(env["ANTHROPIC_AUTH_TOKEN"], "managed-token")

    def test_caller_policy_is_recorded_and_detach_is_not_inherited_on_resume(self):
        self.fake_host()
        directory = self.root / "detached"
        _, result = self.result_launch(directory, "--detach")
        self.assertEqual(result["caller"], {"policy": "detached", "pid": None})
        self.assertEqual(CHAT.inspect_receipt(directory, CHAT.private_json)["caller"], result["caller"])
        _, continued = self.result_launch(self.root / "continued", "--resume-from", str(directory))
        self.assertEqual(continued["caller"], {"policy": "cancel_on_exit", "pid": os.getppid()})
        calls = [call for call in self.calls() if call[0] == "chat"]
        self.assertIn("--detach", calls[0])
        self.assertNotIn("--caller-pid", calls[0])
        self.assertNotIn("--detach", calls[1])
        self.assertEqual(calls[1][calls[1].index("--caller-pid") + 1], str(os.getppid()))

    def test_explicit_caller_and_invalid_lifetime_options(self):
        self.fake_host()
        _, result = self.result_launch(self.root / "explicit", "--caller-pid", str(os.getppid()))
        self.assertEqual(result["caller"]["pid"], os.getppid())
        for flags in (["--caller-pid", "1"], ["--caller-pid", str(os.getpid())],
                      ["--caller-pid", "-2"], ["--detach", "--caller-pid", "42"],
                      ["--detach", "--check"], ["--caller-pid", "42", "--inspect", "/unused"]):
            with self.subTest(flags=flags), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                CHAT.main(["--config", str(self.config), *flags])

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
        self.assertFalse((self.config.parent / "handoffs").exists())

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
import json, os, pathlib, sys, time
root = pathlib.Path(__file__).parent
config = json.loads((root / "behavior.json").read_text())
with (root / "calls.jsonl").open("a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
if config.get("preflight_error") and sys.argv[1] == "chat":
    print("session cannot resume after unknown execution", file=sys.stderr)
    sys.exit(1)
run = {"id": config.get("run_id", "run-1"), "session_id": "actual-session", "output": config.get("report", "BLOCKED: account denied; no changes"),
       "execution_status": config.get("execution_status", "completed"), "acceptance_status": config.get("acceptance_status", "unreviewed"),
       "memory_status": config.get("memory_status", "failed"), "memory_error": "memory unavailable", "model": "fixture-model"}
run["feedback"] = config.get("feedback")
if sys.argv[1:3] == ["run", "show"]:
    if config.get("inspect_failure"):
        sys.exit(3)
    if not (root / "finished").exists():
        run.update(output="", execution_status="running", memory_status="pending_execution")
    elif "canonical_report" in config:
        run["output"] = config["canonical_report"]
    raw = config.get("run_show_raw")
    sys.stdout.buffer.write(raw.encode("utf-8") if raw is not None else (json.dumps(run) + "\\n").encode("utf-8"))
    sys.exit(0)
stream_format = sys.argv[sys.argv.index("--format") + 1]
if stream_format == "text":
    raw = bytes.fromhex(config.get("text_stdout_hex", "")) or config.get("report", "text report").encode("utf-8")
    error = bytes.fromhex(config.get("text_stderr_hex", ""))
    split = max(1, len(raw) // 2)
    os.write(1, raw[:split]); os.write(1, raw[split:])
    if error: os.write(2, error)
    (root / "finished").touch()
    sys.exit(config.get("exit_code", 0))
remaining = config.get("bulk_stdout_size", 0)
while remaining:
    chunk = b"x" * min(65536, remaining)
    written = os.write(1, chunk)
    remaining -= written
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

    def process_launch(self, directory, stream_format):
        return subprocess.run(
            [sys.executable, "-B", str(Path(CHAT.__file__)), "--config", str(self.config),
             "--format", stream_format, "--output-dir", str(directory), "-m", "fixture task"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )

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
        self.assertEqual((directory / "request.txt").read_bytes(), b"Exact $HOME `literal` task")
        receipt = json.loads((directory / "launch.json").read_text())
        self.assertTrue(receipt["created_at_utc"].endswith("Z"))
        self.assertTrue(receipt["finalized_at_utc"].endswith("Z"))
        self.assertEqual(result["record_time"], {key: receipt[key] for key in ("created_at_utc", "finalized_at_utc")})
        record = (directory / "execution.md").read_text()
        self.assertIn("# 原始执行记录", record)
        self.assertIn("## 记录时间", record)
        self.assertIn("## 原始任务", record)
        self.assertIn("Exact $HOME `literal` task", record)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_all_formats_preserve_live_bytes_and_create_private_raw_records(self):
        text_stdout = b"first\x00\xff\xe2\x82\xaclast\n"
        text_stderr = b"child-error\x00\xfe\n"
        self.fake_host(text_stdout_hex=text_stdout.hex(), text_stderr_hex=text_stderr.hex())
        old_umask = os.umask(0)
        try:
            text_dir = self.root / "text"
            text = self.process_launch(text_dir, "text")
            json_dir = self.root / "json"
            json_launch = self.process_launch(json_dir, "json")
            result_dir = self.root / "result"
            result_launch = self.process_launch(result_dir, "result")
        finally:
            os.umask(old_umask)
        self.assertEqual(text.stdout, text_stdout)
        self.assertTrue(text.stderr.startswith(f"Handoff evidence: {text_dir}\n".encode()))
        self.assertTrue(text.stderr.endswith(text_stderr))
        self.assertEqual((text_dir / "stdout.txt").read_bytes(), text_stdout)
        self.assertEqual((text_dir / "stderr.log").read_bytes(), text_stderr)
        self.assertEqual(json_launch.stdout, (json_dir / "events.jsonl").read_bytes())
        self.assertTrue(json_launch.stderr.endswith(b"fixture stderr\n"))
        projected = json.loads(result_launch.stdout)
        self.assertEqual(projected["stream_format"], "result")
        self.assertNotEqual(result_launch.stdout, (result_dir / "events.jsonl").read_bytes())
        for directory in (text_dir, json_dir, result_dir):
            self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
            for path in directory.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600, path.name)
            self.assertTrue((directory / "execution.md").exists())
            self.assertTrue((directory / "result.json").exists())
            self.assertTrue((directory / "request.txt").exists())

    def test_stringio_console_decodes_split_utf8_incrementally(self):
        output = io.StringIO()
        console = CHAT.ConsoleBytes(output)
        encoded = "A€B".encode("utf-8")
        console.write(encoded[:2])
        console.write(encoded[2:3])
        console.write(encoded[3:])
        console.finish()
        self.assertEqual(output.getvalue(), "A€B")

    def test_terminal_agent_reports_are_retained_without_promoting_acceptance(self):
        for index, report in enumerate(("DONE: checked", "BLOCKED: permission denied", "FAILED: tool error")):
            with self.subTest(report=report):
                self.fake_host(report=report, acceptance_status="unreviewed")
                code, result = self.result_launch(self.root / f"terminal-{index}")
                self.assertEqual(code, 0)
                self.assertEqual(result["report"], report)
                self.assertEqual(result["execution"]["acceptance_status"], "unreviewed")
                record = (self.root / f"terminal-{index}" / "execution.md").read_text()
                self.assertIn(report, record)
                self.assertNotIn("验收状态: \"accepted\"", record)

    def test_launch_failure_has_truthful_result_and_does_not_start_or_replay(self):
        self.fake_host()
        output = io.StringIO()
        directory = self.root / "launch-failure"
        with patch.object(CHAT.subprocess, "Popen", side_effect=FileNotFoundError()), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            code = CHAT.main(["--config", str(self.config), "--format", "result",
                              "--output-dir", str(directory), "-m", "task"])
        result = json.loads(output.getvalue())
        self.assertEqual(code, 2)
        self.assertEqual(result["execution"]["exit_code"], 2)
        self.assertIn("launch_failed: FileNotFoundError", result["warnings"])
        self.assertEqual((directory / "request.txt").read_bytes(), b"task")
        self.assertTrue(json.loads((directory / "launch.json").read_text())["finalized_at_utc"])
        self.assertFalse((self.root / "calls.jsonl").exists())

    def test_sink_failure_keeps_draining_and_preserves_child_exit(self):
        self.fake_host(bulk_stdout_size=2 * 1024 * 1024, exit_code=7)
        directory = self.root / "sink-failure"
        original = CHAT.private_output

        class FailingSink:
            def __init__(self, wrapped):
                self.wrapped = wrapped
                self.failed = False

            def __enter__(self):
                return self

            def __exit__(self, *args):
                self.wrapped.close()

            def write(self, _data):
                self.failed = True
                raise OSError("fixture sink failure")

            def flush(self):
                return self.wrapped.flush()

        def failing_output(path):
            opened = original(path)
            return FailingSink(opened) if Path(path).name == "events.jsonl" else opened

        output = io.StringIO()
        with patch.object(CHAT, "private_output", side_effect=failing_output), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            code = CHAT.main(["--config", str(self.config), "--format", "result",
                              "--output-dir", str(directory), "-m", "drain all output"])
        result = json.loads(output.getvalue())
        warnings = [warning for warning in result["warnings"] if warning.startswith("artifact_tee_failed: stdout:")]
        self.assertEqual(warnings, ["artifact_tee_failed: stdout: OSError"])
        self.assertEqual(code, 7)
        self.assertEqual(result["execution"]["exit_code"], 7)
        self.assertIn(b"fixture stderr", (directory / "stderr.log").read_bytes())
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_failed_snapshot_exports_are_null_in_returned_artifacts(self):
        self.fake_host()
        execution_dir = self.root / "execution-export-failure"
        original_bytes = CHAT.save_bytes

        def fail_execution(path, value):
            if Path(path).name == "execution.md":
                raise PermissionError("fixture")
            return original_bytes(path, value)

        output = io.StringIO()
        with patch.object(CHAT, "save_bytes", side_effect=fail_execution), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            CHAT.main(["--config", str(self.config), "--format", "result",
                       "--output-dir", str(execution_dir), "-m", "task"])
        execution_result = json.loads(output.getvalue())
        self.assertIsNone(execution_result["artifacts"]["execution_record"])
        self.assertEqual(execution_result["artifacts"]["result"], str(execution_dir / "result.json"))

        self.fake_host()
        result_dir = self.root / "result-export-failure"
        original_json = CHAT.save_json

        def fail_result(path, value):
            if Path(path).name == "result.json":
                raise PermissionError("fixture")
            return original_json(path, value)

        output = io.StringIO()
        with patch.object(CHAT, "save_json", side_effect=fail_result), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            CHAT.main(["--config", str(self.config), "--format", "result",
                       "--output-dir", str(result_dir), "-m", "task"])
        result_result = json.loads(output.getvalue())
        self.assertIsNone(result_result["artifacts"]["result"])
        self.assertEqual(result_result["artifacts"]["execution_record"], str(result_dir / "execution.md"))
        self.assertFalse((result_dir / "result.json").exists())

    def test_post_execution_export_failure_does_not_replace_child_exit(self):
        self.fake_host(exit_code=7)
        directory = self.root / "export-failure"
        original = CHAT.save_json

        def fail_final_launch(path, value):
            if Path(path).name == "launch.json" and value.get("exit_code") is not None:
                raise PermissionError("fixture export failure")
            return original(path, value)

        output = io.StringIO()
        with patch.object(CHAT, "save_json", side_effect=fail_final_launch), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            code = CHAT.main(["--config", str(self.config), "--format", "result",
                              "--output-dir", str(directory), "-m", "task"])
        result = json.loads(output.getvalue())
        self.assertEqual(code, 7)
        self.assertEqual(result["execution"]["exit_code"], 7)
        self.assertIn("artifact_export_failed: launch.json: PermissionError", result["warnings"])
        self.assertEqual(json.loads((directory / "result.json").read_text())["execution"]["exit_code"], 7)
        self.assertEqual(sum(call[0] == "chat" for call in self.calls()), 1)

    def test_exact_canonical_run_snapshot_and_wrong_id_rejection(self):
        exact = '{ "id": "run-1", "session_id": "actual-session", "output": "DONE exact", "execution_status": "completed", "acceptance_status": "unreviewed", "memory_status": "retained" }\r\n'
        self.fake_host(run_show_raw=exact)
        exact_dir = self.root / "exact"
        _, result = self.result_launch(exact_dir)
        self.assertEqual((exact_dir / "run.json").read_bytes(), exact.encode())
        self.assertEqual(result["report"], "DONE exact")

        self.fake_host(run_id="another-run")
        wrong_dir = self.root / "wrong"
        _, wrong = self.result_launch(wrong_dir)
        self.assertFalse((wrong_dir / "run.json").exists())
        self.assertIsNone(wrong["artifacts"]["run"])
        self.assertIn("host_inspection_unavailable; retained launch evidence only", wrong["warnings"])
        self.assertEqual(wrong["ref"]["run_id"], "run-1")

    def test_text_mode_keeps_raw_report_and_does_not_guess_references(self):
        report = "DONE-looking text only; no canonical identifiers\n"
        self.fake_host(report=report)
        directory = self.root / "text-honest"
        launch = self.process_launch(directory, "text")
        result = json.loads((directory / "result.json").read_text())
        self.assertEqual(launch.stdout.decode(), report)
        self.assertEqual(result["report"], report)
        self.assertIsNone(result["ref"]["run_id"])
        self.assertIsNone(result["ref"]["session_id"])
        self.assertNotIn("execution_status", result["execution"])
        self.assertIsNone(result["artifacts"]["run"])

    def test_inspect_needs_no_credentials_and_never_starts_chat(self):
        self.fake_host()
        directory = self.root / "evidence"
        self.result_launch(directory)
        snapshots = {name: (directory / name).read_bytes()
                     for name in ("launch.json", "result.json", "run.json", "execution.md")}
        self.credentials.unlink()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(CHAT.main(["--inspect", str(directory)]), 0)
        self.assertEqual(json.loads(output.getvalue())["execution"]["exit_code"], 0)
        self.assertEqual(snapshots, {name: (directory / name).read_bytes() for name in snapshots})
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
            live = CHAT.inspect_receipt(directory, CHAT.private_json)
            self.assertIsNotNone(live["artifacts"]["request"])
            self.assertIsNotNone(live["artifacts"]["stdout"])
            self.assertIsNotNone(live["artifacts"]["stderr"])
            self.assertIsNone(live["artifacts"]["run"])
            self.assertIsNone(live["artifacts"]["result"])
            self.assertIsNone(live["artifacts"]["execution_record"])
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
        for override in (["-m", "unexpected"], ["--message=unexpected"], ["-format", "text"], ["--home=/other"],
                         ["--detach"], ["--caller-pid=42"]):
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
