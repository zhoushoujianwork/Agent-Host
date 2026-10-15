import contextlib
import fcntl
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("datasource_migration", Path(__file__).with_name("migrate_datasource.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MigrationTests(unittest.TestCase):
    def fixture(self, root):
        config = root / "config"
        config.mkdir(mode=0o700)
        channel = root / "channels/dingtalk/state"
        channel.mkdir(parents=True)
        with sqlite3.connect(channel / "state.db") as db:
            db.execute("create table memories(id text)")
            db.execute("insert into memories values ('retained')")
        email = root / "plugins/email"
        email.mkdir(parents=True)
        with sqlite3.connect(email / "operations.db") as db:
            db.execute("create table operations(id text, status text)")
            db.execute("insert into operations values ('retained', 'unknown')")
        migration.write(config / "host.json", {"profiles": {"default": {"execution": {"allowed_tools": ["mcp__knowledge_notes__*", "mcp__email__*"]}}}})
        migration.write(config / "agents.json", {"agents": [{"knowledge_sources": ["notes"], "plugins": ["knowledge", "email"]}]})
        migration.write(config / "plugins.knowledge.json", {"version": 1, "enabled": True, "access": {"channels": ["dingtalk"], "audiences": ["owner"]}, "settings": {"version": 1, "sources": [{"name": "notes", "provider": "local_markdown", "root": "/unchanged/docs", "paths": ["note.md"], "projects": ["default"], "audiences": ["owner"]}, {"name": "company", "provider": "confluence", "endpoint": "https://example.test", "credential_file": "/unchanged/token"}]}})
        migration.write(config / "plugins.email.json", {"version": 1, "enabled": True, "access": {"channels": ["dingtalk"], "audiences": ["owner"]}, "settings": {"accounts": []}})

    def test_split_preserves_sources_and_ledger_without_scope(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            channel = migration.receipt(root / "channels/dingtalk/state/state.db")
            ledger = migration.receipt(root / "plugins/email/operations.db")
            with contextlib.redirect_stdout(io.StringIO()):
                report = migration.migrate(root)
            self.assertTrue(report["email_ledger_unchanged"])
            self.assertEqual(channel, migration.receipt(root / "channels/dingtalk/state/state.db"))
            self.assertEqual(ledger, migration.receipt(root / "datasource/plugins/email/operations.db"))
            local = migration.read(root / "config/datasource.plugins.local-documents.json")
            self.assertNotIn("access", local)
            self.assertNotIn("projects", local["settings"]["sources"][0])
            self.assertNotIn("audiences", local["settings"]["sources"][0])
            self.assertEqual("/unchanged/docs", local["settings"]["sources"][0]["root"])
            self.assertNotIn("provider", local["settings"]["sources"][0])
            agent = migration.read(root / "config/agents.json")["agents"][0]
            self.assertNotIn("knowledge_sources", agent)
            self.assertNotIn("plugins", agent)
            rule = migration.read(root / "config/host.json")["profiles"]["default"]["execution"]["allowed_tools"][0]
            self.assertEqual("mcp__knowledge_local-documents_notes__*", rule)
            with self.assertRaises(RuntimeError):
                migration.migrate(root)

    def test_running_channel_is_not_modified(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            with (root / "channels/dingtalk/run.lock").open("w") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                with self.assertRaises(BlockingIOError):
                    migration.migrate(root)
            self.assertTrue((root / "config/plugins.email.json").exists())

    def test_failed_state_verification_restores_old_layout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            original = migration.receipt

            def fail_target(path):
                if "datasource" in path.parts:
                    raise RuntimeError("synthetic verification failure")
                return original(path)

            with mock.patch.object(migration, "receipt", side_effect=fail_target):
                with self.assertRaises(RuntimeError):
                    migration.migrate(root)
            self.assertTrue((root / "config/plugins.email.json").exists())
            self.assertTrue((root / "plugins/email/operations.db").exists())
            self.assertFalse((root / "config/datasource.plugins.email.json").exists())


if __name__ == "__main__":
    unittest.main()
