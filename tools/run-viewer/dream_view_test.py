"""Validate the fixed-root, read-only Dream projection."""

import hashlib
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import subprocess
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

    def rewrite_manifest(self, version, manifest):
        partition_root = self.root / self.identifier
        digest = "a" * 64
        path = partition_root / "versions" / digest / "manifest.json"
        manifest.update({"version": version, "partition": self.partition, "digest": digest})
        raw = (json.dumps(manifest, separators=(",", ":")) + "\n").encode()
        self.write_private(path, raw)
        metadata_path = partition_root / "partition.json"
        metadata = json.loads(metadata_path.read_text())
        metadata["manifest_sha256"][digest] = hashlib.sha256(raw).hexdigest()
        self.write_private(metadata_path, (json.dumps(metadata) + "\n").encode())

    def coordination(self):
        coordination = self.root.parent / "coordination"
        anchor_dir = self.home / ".dream-coordination" / "targets"
        anchor_dir.mkdir(parents=True, mode=0o700)
        (self.home / ".dream-coordination").chmod(0o700)
        policy_path = str(self.base / "sharing.json")
        policy_key = hashlib.sha256(policy_path.encode()).hexdigest()
        anchor = {"version": "dream-coordination-v1", "target_key": self.identifier,
                  "coordination_root": str(coordination), "state_home": str(self.home), "config_id": "config-one",
                  "config_path": str(self.base / "dream.json"), "hot_root": str(self.root), "policy_path": policy_path,
                  "policy_key": policy_key, "partition": self.partition}
        self.write_private(anchor_dir / f"{self.identifier}.json", (json.dumps(anchor) + "\n").encode())
        policy_state = {"version": "dream-coordination-v1", "policy_key": policy_key, "policy_path": policy_path,
                        "highest_generation": 2, "policy_digest": "b" * 64, "state": "active"}
        target_state = {"version": "dream-coordination-v1", "target_key": self.identifier, "state_home": str(self.home),
                        "config_id": "config-one", "config_path": str(self.base / "dream.json"), "hot_root": str(self.root),
                        "policy_path": policy_path, "policy_key": policy_key, "partition": self.partition,
                        "mount_id": "mount", "canonical_person_id": "person", "highest_generation": 2,
                        "policy_digest": "b" * 64, "binding_digest": "c" * 64, "state": "active",
                        "authorization_revision": 3, "last_committed_hot_digest": "a" * 64}
        for kind, key, state_value in (("policies", policy_key, policy_state), ("targets", self.identifier, target_state)):
            ledger = coordination / kind / key
            events = ledger / "events"
            events.mkdir(parents=True, mode=0o700)
            for directory in (coordination, coordination / kind, ledger, events): directory.chmod(0o700)
            state_hash = hashlib.sha256(json.dumps(state_value, separators=(",", ":")).encode()).hexdigest()
            event = {"version": "dream-coordination-v1", "sequence": 1, "previous_state_hash": "0" * 64,
                     "state_hash": state_hash, "actor": "test", "reason": "fixture", "at": "2026-01-01T00:00:00Z", "state": state_value}
            head = {"version": "dream-coordination-v1", "sequence": 1, "state_hash": state_hash, "state": state_value}
            self.write_private(events / "00000000000000000001.json", (json.dumps(event) + "\n").encode())
            self.write_private(ledger / "head.json", (json.dumps(head) + "\n").encode())
        cleanup = coordination / "cleanup" / self.identifier
        cleanup.mkdir(parents=True, mode=0o700)
        (coordination / "cleanup").chmod(0o700)
        intent = {"version": "dream-current-cleanup-v1", "id": "intent", "target_key": self.identifier,
                  "state_home": str(self.home), "config_id": "config-one", "hot_root": str(self.root),
                  "partition": self.partition, "digest": "d" * 64, "status": "completed", "reason": "retained",
                  "planned_at": "2026-01-01T00:00:00Z", "completed_at": "2026-01-02T00:00:00Z"}
        self.write_private(cleanup / "intent.json", (json.dumps(intent) + "\n").encode())

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

    def test_v2_projection_is_strictly_validated_and_only_returned_as_metadata(self):
        self.publication()
        projection = {"canonical_person_id": "alice", "mount_id": "alice-share", "generation": 2,
                      "policy_digest": "b" * 64, "binding_digest": "c" * 64,
                      "sources": [{"id": "ding", "state_home": "/operator/configured/source",
                                   "partition": {"PersonID": "raw-alice", "AgentID": "agent", "AudienceID": "user:alice", "Scope": "scope"},
                                   "account": {"channel": "dingtalk", "account_id": "app", "tenant_id": "tenant", "sender_id": "raw-alice"},
                                   "kinds": ["preference"]}],
                      "memories": [{"source_id": "ding", "memory_id": "memory", "revision": 2, "kind": "preference"}]}
        manifest = {"documents": {"USER.md": "user\n", "MEMORY.md": "memory\n", "TASKS.md": "tasks\n"},
                    "memories": [{"id": "ding:memory", "revision": 2, "kind": "preference", "expires_at": None}],
                    "projection": projection}
        self.rewrite_manifest("dream-hot-v2", manifest)
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["current"]["projection"]["canonical_person_id"], "alice")
        self.assertEqual(detail["current"]["projection"]["sources"][0]["state_home"], "/operator/configured/source")
        projection["memories"][0]["source_id"] = "unknown"
        self.rewrite_manifest("dream-hot-v2", manifest)
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertIn("memory reference", detail["error"])

    def test_authorization_and_unified_cleanup_are_read_only_metadata(self):
        self.publication()
        self.coordination()
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["authorization"]["status"], "active")
        self.assertEqual(detail["authorization"]["highest_generation"], 2)
        self.assertEqual(detail["authorization"]["authorization_revision"], 3)
        self.assertEqual(detail["current_cleanup"]["items"][0]["status"], "completed")
        (self.root.parent / "coordination" / "targets" / self.identifier / "head.json").unlink()
        recovering = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(recovering["authorization"]["status"], "active")
        self.assertTrue(recovering["authorization"]["needs_recovery"])
        events = self.root.parent / "coordination" / "targets" / self.identifier / "events"
        events.chmod(0o755)
        unsafe = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(unsafe["status"], "current")
        self.assertEqual(unsafe["authorization"]["status"], "unknown")

    def test_real_go_ledger_hash_and_global_revocation_are_effective(self):
        self.publication()
        self.root.parent.chmod(0o700)
        environment = dict(os.environ, DREAM_VIEWER_HOME=str(self.home), DREAM_VIEWER_HOT_ROOT=str(self.root))
        completed = subprocess.run(["go", "test", "./internal/dream", "-run", "^TestWriteViewerCoordinationFixture$", "-count=1"],
                                   cwd=HERE.parents[1], env=environment, check=False, capture_output=True, text=True)
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "current")
        self.assertEqual(detail["authorization"]["status"], "invalid")
        self.assertEqual(detail["authorization"]["highest_generation"], 2)
        self.assertIn("全局撤销 <&>", detail["authorization"]["reason"])

    def test_real_go_policy_highwater_invalidates_lagging_target(self):
        self.publication()
        self.root.parent.chmod(0o700)
        environment = dict(os.environ, DREAM_VIEWER_HOME=str(self.home), DREAM_VIEWER_HOT_ROOT=str(self.root),
                           DREAM_VIEWER_FIXTURE_MODE="behind")
        completed = subprocess.run(["go", "test", "./internal/dream", "-run", "^TestWriteViewerCoordinationFixture$", "-count=1"],
                                   cwd=HERE.parents[1], env=environment, check=False, capture_output=True, text=True)
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        authority = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)["authorization"]
        self.assertEqual(authority["status"], "invalid")
        self.assertEqual(authority["highest_generation"], 3)
        self.assertIn("current policy generation", authority["reason"])

    def test_authorization_sampling_retries_once_then_fails_unknown(self):
        self.publication()
        self.coordination()
        reader = viewer.DreamReader(self.root, self.catalog)
        row = reader._entries()[0][0]
        original, calls = reader._ledger, {"count": 0}
        def changes_once(root, kind, anchor):
            value = original(root, kind, anchor)
            calls["count"] += 1
            if calls["count"] in {1, 3}:
                value = dict(value); value["token"] = (calls["count"],)
            return value
        reader._ledger = changes_once
        self.assertEqual(reader._authorization(row)["status"], "active")
        self.assertEqual(calls["count"], 8)
        calls["count"] = 0
        def always_changes(root, kind, anchor):
            value = original(root, kind, anchor)
            calls["count"] += 1
            value = dict(value); value["token"] = (calls["count"],)
            return value
        reader._ledger = always_changes
        unstable = reader._authorization(row)
        self.assertEqual(unstable["status"], "unknown")
        self.assertIn("changed during read", unstable["error"])

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

    def test_reader_lock_is_shared_bounded_and_never_created(self):
        root, _ = self.publication()
        lock = root / ".publish.lock"
        descriptor = os.open(lock, os.O_RDONLY)
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
            self.assertEqual(detail["status"], "error")
            self.assertIn("lock wait exceeded", detail["error"])
        finally:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)
        lock.unlink()
        detail = viewer.DreamReader(self.root, self.catalog).partition(self.identifier)
        self.assertEqual(detail["status"], "error")
        self.assertFalse(lock.exists())

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
        self.assertEqual(detail["status"], "current")
        self.assertIn("per-record byte limit", detail["records_error"])
        self.assertTrue(detail["current"]["snapshot_valid"])

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
        self.assertEqual(detail["status"], "current")
        self.assertIn("response byte limit", detail["records_error"])


if __name__ == "__main__":
    unittest.main()
