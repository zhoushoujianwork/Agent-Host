import hashlib
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("history_validation", Path(__file__).with_name("validate_file_memory_history.py"))
validation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validation)


class HistoryValidationTests(unittest.TestCase):
    def fixture(self, root):
        home = root / "source"
        home.mkdir(mode=0o700)
        database = home / "state.db"
        with sqlite3.connect(database) as db:
            db.execute("CREATE TABLE memories(id TEXT PRIMARY KEY,agent_id TEXT,audience_id TEXT,sender_id TEXT,scope TEXT,text TEXT,status TEXT,payload BLOB)")
            db.execute("CREATE TABLE memory_revisions(memory_id TEXT,revision INTEGER,payload BLOB)")
            db.execute("CREATE TABLE memory_evidence(memory_id TEXT,run_id TEXT)")
            db.execute("CREATE TABLE candidates(id TEXT,agent_id TEXT,audience_id TEXT,scope TEXT,status TEXT,payload BLOB)")
            for identifier, status in (("active", "active"), ("old", "invalidated")):
                memory = {"id": identifier, "scope": "private-scope", "kind": "preference", "text": "sensitive historical wording " + identifier, "status": status, "revision": 1, "source_run_id": "old-source-run"}
                raw = json.dumps(memory).encode()
                db.execute("INSERT INTO memories VALUES(?,?,?,?,?,?,?,?)", (identifier, "default", "owner", "", memory["scope"], memory["text"], status, raw))
                db.execute("INSERT INTO memory_revisions VALUES(?,?,?)", (identifier, 1, raw))
                db.execute("INSERT INTO memory_evidence VALUES(?,?)", (identifier, "old-source-run"))
            candidate = {"id": "pending", "scope": "private-scope", "status": "pending", "draft": {"summary": "sensitive candidate wording"}}
            db.execute("INSERT INTO candidates VALUES(?,?,?,?,?,?)", ("pending", "default", "owner", "private-scope", "pending", json.dumps(candidate).encode()))
        return home

    def executor(self, corrupt=False):
        calls = []

        def execute(_binary, arguments):
            calls.append(arguments)
            command = arguments[1]
            home = Path(arguments[arguments.index("--home") + 1])
            marker = home / "file-memory.json"
            if command == "migrate":
                cache = Path(arguments[arguments.index("--memory-cache") + 1])
                cache.mkdir(parents=True, exist_ok=True, mode=0o700)
                (cache / "index.db").write_bytes(b"derived")
                receipt = {"source_id": arguments[-1], "generation": 1, "memories": 2, "revisions": 2, "evidence": 2, "domains": 1, "skills": 0, "digest": "fixture-content-digest"}
                validation.private_json(marker, {"cache": str(cache), "receipt": receipt})
                return receipt
            if command == "verify":
                metadata = json.loads(marker.read_text())
                cache = Path(metadata["cache"])
                cache.mkdir(parents=True, exist_ok=True, mode=0o700)
                (cache / "index.db").write_bytes(b"rebuilt")
                return metadata["receipt"]
            if command == "rollback":
                metadata = json.loads(marker.read_text())
                # Re-serialization fills normalized partition attribution. This
                # preserves semantic tuples while legitimately changing bytes.
                with sqlite3.connect(home / "state.db") as db:
                    for identifier, payload in db.execute("SELECT id,payload FROM memories").fetchall():
                        memory = json.loads(payload)
                        memory.update(agent_id="default", audience_id="owner")
                        raw = json.dumps(memory, separators=(",", ":")).encode()
                        db.execute("UPDATE memories SET payload=?,text=? WHERE id=?", (raw, memory["text"], identifier))
                        db.execute("UPDATE memory_revisions SET payload=? WHERE memory_id=?", (raw, identifier))
                marker.unlink()
                return metadata["receipt"]
            with sqlite3.connect((home / "state.db").as_uri() + "?mode=ro", uri=True) as db:
                rows = db.execute("SELECT id,payload FROM memories ORDER BY id").fetchall()
            memories = []
            for identifier, payload in rows:
                memory = json.loads(payload)
                memory.update(agent_id="default", audience_id="owner")
                if command == "show" and identifier != arguments[-1]:
                    continue
                if command == "search" and (memory["status"] != "active" or arguments[-1] not in memory["text"]):
                    continue
                if corrupt and marker.exists():
                    memory["text"] = "synthetic mismatch with sensitive wording"
                memories.append(memory)
            return memories[0] if command == "show" else memories

        return execute, calls

    def test_source_is_readonly_and_report_has_no_plaintext(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            before = hashlib.sha256((source / "state.db").read_bytes()).hexdigest()
            original = validation.capture_tables(source / "state.db")
            execute, calls = self.executor()
            output = root / "private-validation"
            report = validation.validate_history(Path("unused-mocked-binary"), {"dingtalk": source}, output, execute)
            self.assertEqual("passed", report["status"])
            self.assertEqual(before, hashlib.sha256((source / "state.db").read_bytes()).hexdigest())
            self.assertEqual(original, validation.capture_tables(source / "state.db"))
            self.assertFalse((source / "file-memory.json").exists())
            self.assertTrue(report["homes"]["dingtalk"]["cache_rebuild_equal"])
            self.assertTrue(report["homes"]["dingtalk"]["rollback_tuples_equal"])
            self.assertEqual(2, report["homes"]["dingtalk"]["queries"]["show"]["count"])
            self.assertEqual(1, report["homes"]["dingtalk"]["queries"]["search"]["count"])
            text = (output / "report.json").read_text()
            for forbidden in ("sensitive historical", "sensitive candidate", "private-scope", "old-source-run"):
                self.assertNotIn(forbidden, text)
            self.assertEqual(0o700, output.stat().st_mode & 0o777)
            self.assertEqual(0o600, (output / "report.json").stat().st_mode & 0o777)
            self.assertTrue(all(str(source) not in arguments for arguments in calls))
            with self.assertRaises(RuntimeError):
                validation.validate_history(Path("unused-mocked-binary"), {"dingtalk": source}, output, execute)

    def test_changed_file_recall_fails_without_disclosing_source_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            before = validation.capture_tables(source / "state.db")
            execute, _ = self.executor(corrupt=True)
            output = root / "failed-validation"
            with self.assertRaisesRegex(RuntimeError, "historical comparison failed"):
                validation.validate_history(Path("unused-mocked-binary"), {"dingtalk": source}, output, execute)
            self.assertEqual(before, validation.capture_tables(source / "state.db"))
            report = json.loads((output / "report.json").read_text())
            self.assertEqual("failed", report["status"])
            self.assertEqual("file_queries", report["failure_phase"])
            self.assertNotIn("sensitive", json.dumps(report))

    def test_live_wal_snapshot_includes_committed_records(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            with sqlite3.connect(source / "state.db") as writer:
                writer.execute("PRAGMA journal_mode=WAL")
                writer.execute("INSERT INTO memory_evidence VALUES('active','wal-source-run')")
                writer.commit()
                copied = validation.backup_readonly(source, root / "copied")
                self.assertEqual(validation.capture_tables(source / "state.db"), validation.capture_tables(copied))

    def test_rollback_reports_only_derived_text_correction(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            with sqlite3.connect(source / "state.db") as db:
                db.execute("UPDATE memories SET text='outdated derived procedure' WHERE id='active'")
            original = validation.capture_tables(source / "state.db")
            execute, _ = self.executor()
            output = root / "projection-validation"
            report = validation.validate_history(Path("unused-mocked-binary"), {"dingtalk": source}, output, execute)
            corrections = report["homes"]["dingtalk"]["derived_projection_corrections"]
            self.assertEqual(1, len(corrections))
            self.assertEqual("memories.text", corrections[0]["field"])
            self.assertNotEqual(corrections[0]["before_sha256"], corrections[0]["after_sha256"])
            self.assertEqual(original, validation.capture_tables(source / "state.db"))
            text = (output / "report.json").read_text()
            self.assertNotIn("outdated derived procedure", text)
            self.assertNotIn("sensitive", text)

    def test_derived_normalization_does_not_waive_changed_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            execute, _ = self.executor()

            def corrupt_rollback(binary, arguments):
                result = execute(binary, arguments)
                if arguments[1] == "rollback":
                    home = Path(arguments[arguments.index("--home") + 1])
                    with sqlite3.connect(home / "state.db") as db:
                        memory = json.loads(db.execute("SELECT payload FROM memories WHERE id='active'").fetchone()[0])
                        memory["text"] = "changed authoritative knowledge"
                        db.execute("UPDATE memories SET payload=?,text=? WHERE id='active'", (json.dumps(memory).encode(), memory["text"]))
                return result

            output = root / "corrupt-rollback"
            with self.assertRaisesRegex(RuntimeError, "rollback semantic table tuples"):
                validation.validate_history(Path("unused-mocked-binary"), {"dingtalk": source}, output, corrupt_rollback)
            report = json.loads((output / "report.json").read_text())
            self.assertEqual("rollback", report["failure_phase"])


if __name__ == "__main__":
    unittest.main()
