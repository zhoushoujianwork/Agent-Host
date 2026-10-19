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


if __name__ == "__main__":
    unittest.main()
