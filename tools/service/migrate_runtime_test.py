"""Verify stopped full-history relocation without touching the user's runtime."""

import fcntl
from pathlib import Path
import sqlite3
import tempfile
import unittest

import migrate_runtime as MIGRATE


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.source, self.target = self.base / "development/.agent-host", self.base / "production"
        self.checkout = self.base / "development"
        (self.source / "config").mkdir(parents=True)
        (self.source / "channels/feishu/state").mkdir(parents=True)
        self.host = {"version": 1, "memory_scope": str(self.source / "legacy/workspace"),
                     "credential_file": str(self.source / "config/credential.secret")}
        MIGRATE.write(self.source / "config/host.json", self.host)
        (self.source / "config/credential.secret").write_text("fixture-only")
        with sqlite3.connect(self.source / "channels/feishu/state/state.db") as db:
            db.execute("create table evidence(id text primary key, body text)")
            db.execute("insert into evidence values ('old-task', 'preserved history')")
        (self.checkout / "bin").mkdir()
        binary = self.checkout / "bin/agent-host"
        binary.write_text("#!/bin/sh\nexit 0\n")
        binary.chmod(0o700)
        for name in ("tools/run-viewer/server.py", "skills/agent-host-memory/SKILL.md",
                     "tools/service/autostart.py", "tools/service/install.py", "tools/install.py"):
            path = self.checkout / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture")

    def test_copy_preserves_database_credentials_and_logical_scope(self):
        cli = self.source / "cli-state"
        cli.mkdir()
        with sqlite3.connect(cli / "state.db") as db:
            db.execute("pragma user_version=14")
        old = MIGRATE.digest(self.source / "channels/feishu/state/state.db")
        report = MIGRATE.migrate(self.source, self.target, self.checkout)
        self.assertEqual(report["status"], "verified_stopped")
        self.assertFalse(report["channels_started"])
        self.assertEqual(MIGRATE.digest(self.target / "channels/feishu/state/state.db"), old)
        self.assertEqual(MIGRATE.digest(self.source / "channels/feishu/state/state.db"), old)
        config = MIGRATE.read(self.target / "config/host.json")
        self.assertEqual(config["memory_scope"], self.host["memory_scope"])
        self.assertEqual(config["credential_file"], str(self.target / "config/credential.secret"))
        self.assertEqual((self.target / "config/credential.secret").read_text(), "fixture-only")
        self.assertEqual(MIGRATE.read(self.source / "config/host.json"), self.host)
        self.assertFalse((self.target / "bin/agent-host").is_symlink())
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o700)
        with sqlite3.connect(self.target / "cli-state/state.db") as db:
            self.assertEqual(db.execute("pragma user_version").fetchone()[0], 14)

    def test_stopped_bundle_can_be_relocated_without_using_development_artifacts(self):
        MIGRATE.migrate(self.source, self.target, self.checkout)
        (self.checkout / "bin/agent-host").write_text("broken development build")
        recovered = self.base / "recovered"
        MIGRATE.migrate(self.target, recovered, self.checkout)
        self.assertEqual((recovered / "bin/agent-host").read_text(), "#!/bin/sh\nexit 0\n")
        self.assertEqual(MIGRATE.read(recovered / "config/host.json")["credential_file"],
                         str(recovered / "config/credential.secret"))

    def test_running_source_is_rejected_before_copy(self):
        with (self.source / "channels/feishu/run.lock").open("a+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                MIGRATE.migrate(self.source, self.target, self.checkout)
        self.assertFalse(self.target.exists())

    def test_existing_destination_is_never_overwritten(self):
        self.target.mkdir()
        (self.target / "keep").write_text("existing")
        with self.assertRaisesRegex(ValueError, "absent"):
            MIGRATE.migrate(self.source, self.target, self.checkout)
        self.assertEqual((self.target / "keep").read_text(), "existing")

    def test_memory_rebases_provenance_and_all_manifests_preserving_payload(self):
        memory = self.target / "memory"
        record = memory / "domains/domain/entries/id/revisions/1/record.json"
        record.parent.mkdir(parents=True)
        payload = {"scope": str(self.source / "legacy/workspace"),
                   "content": "Keep the original evidence: " + str(self.source)}
        home = str(self.source / "channels/feishu/state")
        MIGRATE.write(record, {"version": 1, "state_home": home, "memory": payload})
        # Raw audit JSON retains its original Unicode escape/number spellings.
        record.write_text(record.read_text().replace('"memory": {', '"memory": {"audit":{"escaped":"\\u4e2d","number":1e-06},'))
        payload = MIGRATE.read(record)["memory"]
        original_payload = MIGRATE.raw_field(record.read_text(), "memory")
        old = MIGRATE.digest(record)
        manifest = {"state_home": home, "domains": [{"state_home": home, "scope": payload["scope"]}],
                    "entries": [{"revisions": [{"record_sha256": old, "content_sha256": "unchanged"}]}],
                    "operations": [{"after": payload}]}
        source = memory / "sources/key"
        (source / "publications").mkdir(parents=True)
        for name in ("current.json", "publications/1.json"):
            MIGRATE.write(source / name, manifest)
        MIGRATE.write(source / "publications/empty.json", {"state_home": home, "domains": None, "entries": None})
        marker = self.target / "channels/feishu/state/file-memory.json"
        marker.parent.mkdir(parents=True)
        MIGRATE.write(marker, {"state_home": home, "memory_root": str(self.source / "memory")})
        MIGRATE.relocate_memory(self.source, self.target)
        self.assertEqual(MIGRATE.read(record)["memory"], payload)
        self.assertEqual(MIGRATE.raw_field(record.read_text(), "memory"), original_payload)
        self.assertEqual(MIGRATE.read(marker)["memory_root"], str(self.target / "memory"))
        for name in ("current.json", "publications/1.json"):
            value = MIGRATE.read(source / name)
            self.assertEqual(value["entries"][0]["revisions"][0]["record_sha256"], MIGRATE.digest(record))
            self.assertEqual(value["entries"][0]["revisions"][0]["content_sha256"], "unchanged")
            self.assertEqual(value["operations"], manifest["operations"])
            self.assertEqual(value["domains"][0]["scope"], payload["scope"])

    def test_failed_preflight_keeps_source_and_does_not_mark_target_ready(self):
        binary = self.checkout / "bin/agent-host"
        binary.write_text("#!/bin/sh\nexit 1\n")
        with self.assertRaisesRegex(RuntimeError, "preflight"):
            MIGRATE.migrate(self.source, self.target, self.checkout)
        self.assertEqual(MIGRATE.read(self.source / "config/host.json"), self.host)
        self.assertFalse((self.target / "migration.json").exists())


if __name__ == "__main__":
    unittest.main()
