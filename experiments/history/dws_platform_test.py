"""Offline regressions; no DWS, service, production or subprocess invocation."""

import importlib.util
import json
from pathlib import Path
import sqlite3
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location("dws_platform", Path(__file__).with_name("dws_platform.py"))
platform = importlib.util.module_from_spec(spec)
spec.loader.exec_module(platform)


DEFERRED = {"error": {"category": "api", "code": 1, "message": "消息不存在", "operation": "tools/call",
                      "origin": "dingtalk_api", "reason": "invalid_request", "server_error_code": "PARAM_ERROR",
                      "server_key": "im", "stage": "tool_validation"}}


class PreservationTest(unittest.TestCase):
    def fixture(self):
        before, after = sqlite3.connect(":memory:"), sqlite3.connect(":memory:")
        self.addCleanup(before.close)
        self.addCleanup(after.close)
        before.executescript("""
            CREATE TABLE ordinary (id TEXT PRIMARY KEY, payload BLOB);
            INSERT INTO ordinary VALUES ('one', X'007F');
            CREATE TABLE composite (tenant TEXT, id TEXT, payload TEXT, PRIMARY KEY (tenant,id)) WITHOUT ROWID;
            INSERT INTO composite VALUES ('owner','one','original');
            CREATE TABLE unkeyed (payload TEXT);
            INSERT INTO unkeyed VALUES ('original');
        """)
        before.backup(after)
        return before, after

    def test_without_rowid_and_new_columns_and_rows(self):
        before, after = self.fixture()
        after.executescript("""
            ALTER TABLE composite ADD COLUMN new_column TEXT;
            INSERT INTO composite VALUES ('owner','two','new','added');
            INSERT INTO ordinary VALUES ('two', X'FF');
            INSERT INTO unkeyed VALUES ('new');
        """)
        self.assertEqual(platform.preserved_original_rows(before, after), {"tables": 3, "rows": 3})

    def test_original_value_mutation_is_rejected(self):
        before, after = self.fixture()
        after.execute("UPDATE composite SET payload='changed'")
        with self.assertRaisesRegex(AssertionError, "original table rows changed"):
            platform.preserved_original_rows(before, after)

    def test_original_row_deletion_is_rejected(self):
        before, after = self.fixture()
        after.execute("DELETE FROM unkeyed")
        with self.assertRaisesRegex(AssertionError, "original table rows changed"):
            platform.preserved_original_rows(before, after)


class SendStatusTest(unittest.TestCase):
    def fixture(self, responses):
        acceptance = platform.Acceptance.__new__(platform.Acceptance)
        acceptance.target = {"profile": "tenant:owner"}
        acceptance.context = {"conversation_id": "synthetic-conversation"}
        acceptance.public = {"terminal_source_send_successes": 0}
        acceptance.save = Mock()
        acceptance.command_count = 0
        clock = {"now": 0}
        calls = []

        def command(words, timeout):
            calls.append(words)
            acceptance.command_count += 1
            return responses[min(len(calls) - 1, len(responses) - 1)]

        acceptance.command = command
        step = {"label": "recall-after-new", "send_state": "api_accepted",
                "send": {"ok": True, "result": {"success": True}, "sendReceipt": {"openTaskId": "genuine-fixture-task"}}}
        return acceptance, step, clock, calls

    def test_exact_deferred_error_after_warning_is_recognized(self):
        stderr = "warning: configured runtime\n" + json.dumps(DEFERRED)
        self.assertTrue(platform.deferred_send_status_error(stderr))
        changed = json.loads(json.dumps(DEFERRED))
        changed["error"]["message"] = "another failure"
        self.assertFalse(platform.deferred_send_status_error(json.dumps(changed)))
        changed = json.loads(json.dumps(DEFERRED))
        changed["error"]["server_error_code"] = "AUTH_ERROR"
        self.assertFalse(platform.deferred_send_status_error(json.dumps(changed)))

    def test_same_task_reads_can_observe_eventual_terminal_success(self):
        delayed = SimpleNamespace(returncode=1, stderr="warning\n" + json.dumps(DEFERRED), stdout="")
        delivered = SimpleNamespace(returncode=0, stderr="", stdout=json.dumps({
            "readyForMessageActions": True, "result": {"sendStatus": "SUCCESS"},
            "messageRef": {"openMessageId": "genuine-fixture-message", "openConversationId": "synthetic-conversation"}}))
        acceptance, step, clock, calls = self.fixture([delayed, delayed, delivered])
        with patch.object(platform.time, "monotonic", side_effect=lambda: clock["now"]), \
                patch.object(platform.time, "sleep", side_effect=lambda seconds: clock.update(now=clock["now"] + seconds)), \
                patch("builtins.print"):
            self.assertIs(acceptance.await_send_status(step), step)
        self.assertEqual(len(calls), 3)
        self.assertTrue(all(words[1:3] == ["chat", "+messages-query-send-status"] for words in calls))
        self.assertTrue(all(words[words.index("--open-task-id") + 1] == "genuine-fixture-task" for words in calls))
        self.assertEqual(acceptance.public["terminal_source_send_successes"], 1)
        self.assertEqual([r["deferred_message_visibility"] for r in step["status_read_attempts"]], [True, True, False])
        # Re-inspection of the completed step never increments or sends again.
        acceptance.await_send_status(step)
        self.assertEqual(len(calls), 3)
        self.assertEqual(acceptance.public["terminal_source_send_successes"], 1)

    def test_deferred_reads_stop_at_bound_without_send_replay(self):
        delayed = SimpleNamespace(returncode=1, stderr=json.dumps(DEFERRED), stdout="")
        acceptance, step, clock, calls = self.fixture([delayed])
        with patch.object(platform.time, "monotonic", side_effect=lambda: clock["now"]), \
                patch.object(platform.time, "sleep", side_effect=lambda seconds: clock.update(now=clock["now"] + seconds)):
            with self.assertRaisesRegex(AssertionError, "bounded reads; do not replay"):
                acceptance.await_send_status(step)
        self.assertEqual(clock["now"], 45)
        self.assertEqual(len(calls), 45)
        self.assertEqual(acceptance.public["terminal_source_send_successes"], 0)
        self.assertEqual(step["send_state"], "api_accepted")

    def test_unrelated_error_is_not_retried(self):
        failed = SimpleNamespace(returncode=1, stderr='{"error":{"server_error_code":"AUTH_ERROR"}}', stdout="")
        acceptance, step, clock, calls = self.fixture([failed])
        with self.assertRaisesRegex(AssertionError, "status read failed"):
            acceptance.await_send_status(step)
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
