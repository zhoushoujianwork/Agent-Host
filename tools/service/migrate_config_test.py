import contextlib
import fcntl
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("migration", Path(__file__).with_name("migrate_config.py"))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MigrationTests(unittest.TestCase):
    def fixture(self, root):
        for name in ("dingtalk", "runtime"):
            old = root / name
            for part in ("config", "state", "workspace", "native", "logs"):
                (old / part).mkdir(parents=True)
            with sqlite3.connect(old / "state/state.db") as db:
                for table in ("runs", "events", "memories", "candidates", "channel_messages"):
                    db.execute(f"create table {table}(id text)")
                    db.execute(f"insert into {table} values ('retained')")
            migration.write(old / "config/component-service.json", {"claude": "/fixture/claude", "model": "fixture"})
            migration.write(old / "config/component-environment.json", {"JEV_API_KEY": "fixture"})
        config = root / "dingtalk/config"
        secret = config / "credential-dingtalk.secret"
        secret.write_text("fixture")
        migration.write(config / "channel-dingtalk.json", {"client_secret_file": str(secret)})
        migration.write(config / "data-dingtalk-workspace.json", {"profile": "corp:owner", "services": ["contact"], "state_directory": str(root / "dingtalk/workspace")})
        migration.write(config / "component-agents.json", {"agents": []})
        knowledge = root / "dingtalk/knowledge/owner"
        knowledge.mkdir(parents=True)
        (knowledge / "note.md").write_text("retained source")
        migration.write(config / "data-knowledge.json", {"version": 1, "sources": [{"root": str(knowledge)}]})
        (root / "dingtalk/workspace/token.json").write_text("retained token")
        (root / "dingtalk/workspace/report.md").write_text("task report")
        migration.write(root / "runtime/config/channel-feishu.json", {"profile": "fixture"})
        email = root / "email"
        email.mkdir()
        password = email / "credential-email.secret"
        password.write_text("fixture")
        migration.write(email / "data-email-accounts.json", {"accounts": [{"credential_file": str(password)}], "state_directory": str(email / "state")})
        (email / "state").mkdir()
        (email / "state/operations.db").write_bytes(b"retained ledger")
        for name, old in (("dingtalk", "dingtalk"), ("feishu", "runtime")):
            migration.write(root / f"channels/channel-{name}.json", {"enabled": True, "runtime_root": f"../{old}"})

    def test_preserves_databases_ledger_sources_and_feature_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            before = migration.database_receipt(root / "dingtalk/state/state.db")
            with contextlib.redirect_stdout(io.StringIO()):
                migration.migrate(root)
            self.assertEqual(before, migration.database_receipt(root / "channels/dingtalk/state/state.db"))
            self.assertEqual("retained token", (root / "channels/dingtalk/dws/token.json").read_text())
            self.assertEqual("task report", (root / "channels/dingtalk/workspace/report.md").read_text())
            self.assertEqual(b"retained ledger", (root / "plugins/email/operations.db").read_bytes())
            self.assertEqual("retained source", (root / "knowledge/owner/note.md").read_text())
            self.assertEqual(0o700, (root / "config").stat().st_mode & 0o777)
            host = json.loads((root / "config/host.json").read_text())
            self.assertEqual(str(root / "dingtalk/workspace"), host["profiles"]["dingtalk"]["execution"]["memory_scope"])
            with self.assertRaises(RuntimeError):
                migration.migrate(root)

    def test_running_runtime_is_not_migrated(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            with (root / "dingtalk/run.lock").open("w") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                with self.assertRaises(BlockingIOError):
                    migration.migrate(root)
            self.assertFalse((root / "config").exists())


if __name__ == "__main__":
    unittest.main()
