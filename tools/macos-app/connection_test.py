import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
import sys

spec = importlib.util.spec_from_file_location("connection", Path(__file__).with_name("connection.py"))
connection = importlib.util.module_from_spec(spec)
spec.loader.exec_module(connection)


class ConnectionValidationTests(unittest.TestCase):
    def test_validation_reuses_chat_checker_and_does_not_return_credentials(self):
        calls = []
        fake = SimpleNamespace(configured_launch=lambda p: calls.append(p),
            agent_destination=lambda p: {"state_home": "/tmp/chosen-state", "selected_agent": "daily"})
        with tempfile.TemporaryDirectory() as root:
            config = Path(root) / "task-handoff.json"; config.write_text('{}')
            with patch.dict(sys.modules, {"chat_runtime": fake}):
                result = connection.validate(str(config))
            self.assertEqual(calls, [config.resolve()])
            self.assertEqual(set(result), {"status", "config", "home", "agent"})
            self.assertEqual(result["agent"], "daily")

    def test_checker_error_is_sanitized_by_cli(self):
        def fail(path):
            raise ValueError("sensitive-provider-value")
        with tempfile.TemporaryDirectory() as root:
            config = Path(root) / "task-handoff.json"; config.write_text('{}')
            fake = SimpleNamespace(configured_launch=fail, agent_destination=lambda p: {})
            output = io.StringIO()
            with patch.dict(sys.modules, {"chat_runtime": fake}), patch.object(sys, "argv", ["connection.py", str(config)]), patch("sys.stdout", output):
                self.assertEqual(connection.main(), 1)
            self.assertNotIn("sensitive-provider-value", output.getvalue())
            self.assertIn("validation failed", output.getvalue())


if __name__ == "__main__":
    unittest.main()
