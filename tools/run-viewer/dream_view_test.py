"""Validate the fixed-root, read-only Dream projection."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

import dream_view

HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("viewer", HERE / "server.py")
viewer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(viewer)


class DreamViewTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="agent-host-dream-view-")
        self.base = Path(self.temporary.name).resolve()
        self.home = self.base / "state"
        self.root = self.base / "dream" / "hot"
        self.home.mkdir(mode=0o700)
        self.root.mkdir(parents=True, mode=0o700)
        self.database()
        self.catalog = viewer.Catalog(self.home, None)
        self.partition = {"PersonID": "person", "AgentID": "agent", "AudienceID": "owner", "Scope": "scope"}
        self.identifier = hashlib.sha256(b"person\0agent\0owner\0scope").hexdigest()[:24]

    def tearDown(self):
        self.temporary.cleanup()

    def database(self):
        conn = sqlite3.connect(self.home / "state.db")
        conn.executescript("CREATE TABLE dream_cycles(id TEXT,config_id TEXT,status TEXT,started_at TEXT,payload BLOB);"
                           "CREATE TABLE dream_work_items(id TEXT,person_id TEXT,agent_id TEXT,audience_id TEXT,scope TEXT,state TEXT,updated_at TEXT,payload BLOB);"
                           "CREATE TABLE dream_candidates(id TEXT,person_id TEXT,agent_id TEXT,audience_id TEXT,scope TEXT,status TEXT,payload BLOB);"
                           "CREATE TABLE dream_cleanup(id TEXT,person_id TEXT,agent_id TEXT,audience_id TEXT,scope TEXT,status TEXT,payload BLOB);")
        conn.commit()
        conn.close()
        os.chmod(self.home / "state.db", 0o600)

    def write_private(self, path, body):
        path.write_bytes(body)
        path.chmod(0o600)

    def publication(self, expiry=None, current=True, source_home=None):
        digest = "a" * 64
        partition_root = self.root / self.identifier
        version = partition_root / "versions" / digest
        version.mkdir(parents=True, mode=0o700)
        partition_root.chmod(0o700)
        (partition_root / "versions").chmod(0o700)
        documents = {"USER.md": "user\n", "MEMORY.md": "memory\n", "TASKS.md": "tasks\n"}
        manifest = {"version": "dream-hot-v1", "partition": self.partition, "documents": documents,
                    "memories": [{"id": "memory", "revision": 2, "kind": "preference", "expires_at": expiry}], "digest": digest}
        raw = (json.dumps(manifest, separators=(",", ":")) + "\n").encode()
        for name, text in documents.items():
            self.write_private(version / name, text.encode())
        self.write_private(version / "manifest.json", raw)
        metadata = {"version": "dream-current-v1", "id": self.identifier, "partition": self.partition,
                    "source": {"state_home": str(source_home or self.home), "config_id": "config-one"},
                    "manifest_sha256": {digest: hashlib.sha256(raw).hexdigest()}}
        self.write_private(partition_root / "partition.json", (json.dumps(metadata) + "\n").encode())
        self.write_private(partition_root / ".publish.lock", b"")
        if current:
            (partition_root / "current").symlink_to("versions/" + digest)
        return partition_root, version

    def records(self, matching=1, decoys=1):
        conn = sqlite3.connect(self.home / "state.db")
        for index in range(matching):
            suffix = f"m{index:02d}"
            conn.execute("INSERT INTO dream_cycles VALUES(?,?,?,?,?)", (suffix, "config-one", "completed", suffix, json.dumps({"id": suffix, "status": "completed"})))
            conn.execute("INSERT INTO dream_work_items VALUES(?,?,?,?,?,?,?,?)", (suffix, "person", "agent", "owner", "scope", "missing_evidence", suffix, json.dumps({"id": suffix, "state": "missing_evidence", "missing_reasons": ["source_missing"]})))
            conn.execute("INSERT INTO dream_candidates VALUES(?,?,?,?,?,?,?)", (suffix, "person", "agent", "owner", "scope", "pending", json.dumps({"ID": suffix, "Status": "pending", "SkillID": "skill", "SkillRevision": 2})))
            conn.execute("INSERT INTO dream_cleanup VALUES(?,?,?,?,?,?,?)", (suffix, "person", "agent", "owner", "scope", "planned", json.dumps({"id": suffix, "status": "planned"})))
        for index in range(decoys):
            suffix = f"d{index:02d}"
            conn.execute("INSERT INTO dream_cycles VALUES(?,?,?,?,?)", (suffix, "other-config", "failed", suffix, json.dumps({"id": suffix})))
            conn.execute("INSERT INTO dream_work_items VALUES(?,?,?,?,?,?,?,?)", (suffix, "other", "agent", "owner", "scope", "pending", suffix, json.dumps({"id": suffix})))
            conn.execute("INSERT INTO dream_candidates VALUES(?,?,?,?,?,?,?)", (suffix, "other", "agent", "owner", "scope", "pending", json.dumps({"ID": suffix})))
            conn.execute("INSERT INTO dream_cleanup VALUES(?,?,?,?,?,?,?)", (suffix, "other", "agent", "owner", "scope", "planned", json.dumps({"id": suffix})))
        conn.commit()
        conn.close()

    def test_current_and_exact_read_only_records(self):
        self.publication()
        self.records()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        reader = viewer.DreamReader(self.root, self.catalog)
        self.assertEqual(reader.partitions()["status"], "ready")
        detail = reader.partition(self.identifier)
        self.assertEqual(detail["status"], "current")
        self.assertTrue(detail["current"]["snapshot_valid"])
        self.assertEqual(detail["current"]["source_mtime_kind"], "filesystem_mtime_not_content_generation")
        self.assertEqual(detail["current"]["documents"]["MEMORY.md"], "memory\n")
        for name in ("cycles", "work", "candidates", "cleanup"):
            self.assertEqual(len(detail["records"][name]["items"]), 1)
            self.assertFalse(detail["records"][name]["has_more"])
        conn = self.catalog.connection()
        with self.assertRaises(sqlite3.OperationalError):
            conn.execute("DELETE FROM dream_cycles")
        conn.close()
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)

    def test_truncation_expiry_empty_and_unmounted_source(self):
        self.publication(expiry="2020-01-01T00:00:00Z")
        self.records(matching=51, decoys=0)
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "expired")
        self.assertFalse(detail["current"]["snapshot_valid"])
        self.assertTrue(detail["records"]["work"]["truncated"])
        self.assertEqual(len(detail["records"]["work"]["items"]), 50)
        self.assertEqual(detail["records"]["work"]["ordering"], "recent")
        self.assertEqual(detail["records"]["candidates"]["ordering"], "bounded")
        other = self.base / "other"
        other.mkdir(mode=0o700)
        unmounted = self.base / "unmounted"
        unmounted.mkdir(mode=0o700)
        # Rewriting only fixture metadata models a publication owned by a source
        # that this workbench was not authorized to mount.
        metadata = json.loads((self.root / self.identifier / "partition.json").read_text())
        metadata["source"]["state_home"] = str(unmounted)
        self.write_private(self.root / self.identifier / "partition.json", json.dumps(metadata).encode())
        row = viewer.DreamReader(self.root, self.catalog).partitions()["partitions"][0]
        self.assertIn("not mounted", row["error"])

    def test_missing_and_unsafe_current_and_document_size(self):
        root, version = self.publication(current=False)
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["current"]["status"], "missing")
        (root / "current").symlink_to("../outside")
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertIn("unsafe Dream current", detail["error"])
        (root / "current").unlink()
        (root / "current").symlink_to("versions/" + "a" * 64)
        self.write_private(version / "MEMORY.md", b"x" * ((1 << 20) + 1))
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertRegex(detail["error"], "exceeds limit|unsafe Dream file")

    def test_absent_and_unsafe_roots_fail_closed(self):
        absent = self.base / "absent"
        reader = viewer.DreamReader(absent, self.catalog)
        result = reader.partitions()
        self.assertEqual(result["status"], "empty")
        self.assertFalse(absent.exists())
        absent.symlink_to(self.base / "dangling")
        result = reader.partitions()
        self.assertEqual(result["status"], "unsafe")
        absent.unlink()
        absent.mkdir(mode=0o700)
        self.assertEqual(reader.partitions()["status"], "empty")
        absent.chmod(0o755)
        self.assertEqual(reader.partitions()["status"], "unsafe")

        live = viewer.DreamReader(self.root, self.catalog)
        self.assertEqual(live.partitions()["status"], "empty")
        self.root.chmod(0o755)
        self.assertEqual(live.partitions()["status"], "unsafe")

    def test_partition_iteration_and_record_bytes_are_bounded(self):
        bounded = self.base / "bounded"
        bounded.mkdir(mode=0o700)
        for index in range(dream_view.PARTITION_LIMIT + 1):
            (bounded / f"entry-{index}").mkdir(mode=0o700)
        result = viewer.DreamReader(bounded, self.catalog).partitions()
        self.assertEqual(result["status"], "unsafe")
        self.assertIn("limit exceeded", result["errors"][0])

        self.publication()
        conn = sqlite3.connect(self.home / "state.db")
        oversized = json.dumps({"padding": "x" * (dream_view.RECORD_LIMIT + 1)})
        conn.execute("INSERT INTO dream_candidates VALUES(?,?,?,?,?,?,?)", ("large", "person", "agent", "owner", "scope", "pending", oversized))
        conn.commit()
        conn.close()
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertIn("per-record byte limit", detail["error"])

    def test_record_total_byte_limit_is_explicit(self):
        self.publication()
        conn = sqlite3.connect(self.home / "state.db")
        payload = json.dumps({"padding": "x" * 900000})
        for index in range(5):
            suffix = f"large-{index}"
            conn.execute("INSERT INTO dream_work_items VALUES(?,?,?,?,?,?,?,?)", (suffix, "person", "agent", "owner", "scope", "pending", suffix, payload))
        conn.commit()
        conn.close()
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertIn("response byte limit", detail["error"])


if __name__ == "__main__":
    unittest.main()
