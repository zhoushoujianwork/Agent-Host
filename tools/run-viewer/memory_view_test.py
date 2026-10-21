"""Verify memory provenance, source separation and the read-only HTTP boundary."""

import hashlib
import json
from pathlib import Path
import shutil
import sqlite3
import tempfile
import threading
import unittest
import os
import socketserver
import subprocess
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server
from memory_view import memory_catalog, object_payload


class MemoryTests(unittest.TestCase):
    def test_projection_skips_native_transcripts_but_preserves_memory_evidence(self):
        conn = self.database()
        native = {"type": "native.evidence", "data": {"raw": "TRANSCRIPT_SENTINEL" + "x" * 32768}}
        for seq in range(64):
            conn.execute("INSERT INTO events VALUES(?,?,?)", ("source-a", seq, json.dumps(native)))
        decision = {"type": "decision.completed", "data": {"decision": {
            "kind": "store", "store": True, "details": {"reason": "Recorded reason"}}}}
        retrieval = {"type": "memory.retrieval.evaluated", "data": {"scope": "/project-a", "decisions": [
            {"memory_id": "m-a", "accepted": False, "reason": "Recorded exclusion"}]}}
        for seq, event in enumerate((decision, retrieval), 64):
            conn.execute("INSERT INTO events VALUES(?,?,?)", ("source-a", seq, json.dumps(event)))
        conn.commit()
        conn.close()
        with patch("memory_view.object_payload", wraps=object_payload) as decode:
            result = memory_catalog(self.catalog)
        self.assertFalse(any("TRANSCRIPT_SENTINEL" in str(call.args[0]) for call in decode.call_args_list))
        memory = next(m for m in result["memories"] if m["memory"]["id"] == "m-a")
        self.assertEqual(memory["decision"]["reason"], "Recorded reason")
        self.assertEqual(memory["uses"][0]["reason"], "Recorded exclusion")
        self.assertEqual(len(self.catalog.record("db:source-a")["events"]), 66)

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="ah-memory-", dir="/tmp")
        self.root = Path(self.temporary.name)
        self.home, self.reports = self.root / "home", self.root / "reports"
        self.reports.mkdir()
        self.catalog = server.Catalog(self.home, self.reports)

    def tearDown(self):
        self.temporary.cleanup()

    def database(self):
        self.home.mkdir()
        conn = sqlite3.connect(self.home / "state.db")
        conn.executescript("CREATE TABLE memories(id TEXT PRIMARY KEY,scope TEXT,payload TEXT);"
                           "CREATE TABLE memory_annotations(memory_id TEXT PRIMARY KEY,payload TEXT);"
                           "CREATE TABLE runs(id TEXT PRIMARY KEY,scope TEXT,created_at TEXT,payload TEXT);"
                           "CREATE TABLE events(run_id TEXT,seq INTEGER,payload TEXT);"
                           "CREATE TABLE candidates(id TEXT PRIMARY KEY,scope TEXT,run_id TEXT,payload TEXT,agent_id TEXT DEFAULT 'default',audience_id TEXT DEFAULT 'owner');"
                           "CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT,workspace TEXT,scope TEXT,created_at TEXT,updated_at TEXT);"
                           "CREATE TABLE channel_messages(id TEXT,reply_url TEXT);"
                           "CREATE TABLE channel_reactions(id INTEGER,channel_message_id TEXT,status TEXT);"
                           "PRAGMA user_version=7;")
        conn.execute("INSERT INTO channel_messages VALUES('private','synthetic-reply-capability')")
        for scope, name in (("/project-a", "m-a"), ("/project-b", "m-b")):
            memory = {"id": name, "text": '<img src=x onerror="alert(1)">', "scope": scope,
                      "status": "active", "kind": "constraint", "source_type": "user_statement",
                      "source_run_id": "source-a" if scope == "/project-a" else "missing", "revision": 1}
            derived = {"version": "entity-topic-v1", "objects": [{"id": name, "name": "订单服务", "kind": "service"}], "topics": [], "aliases": []}
            conn.execute("INSERT INTO memories VALUES(?,?,?)", (name, scope, json.dumps(memory)))
            conn.execute("INSERT INTO memory_annotations VALUES(?,?)", (name, json.dumps(derived)))
        run = {"id": "source-a", "scope": "/project-a", "created_at": "2026-10-10T06:00:00Z", "input": "A statement", "context": []}
        conn.execute("INSERT INTO runs VALUES(?,?,?,?)", (run["id"], run["scope"], run["created_at"], json.dumps(run)))
        conn.commit()
        return conn

    def test_all_chats_shows_group_memory_without_cross_partition_detail_or_actions(self):
        conn = self.database()
        for name, agent, audience in (("group-a", "default", "group-a"),
                                      ("group-b", "helper", "group-b")):
            memory = {"id": name, "scope": "/project-a", "text": "Group experience " + name,
                      "agent_id": agent, "audience_id": audience, "source_run_id": name,
                      "status": "active", "revision": 1}
            conn.execute("INSERT INTO memories VALUES(?,?,?)", (name, "/project-a", json.dumps(memory)))
            conn.execute("INSERT INTO memory_annotations VALUES(?,?)", (name, json.dumps({})))
            run = {"id": name, "scope": "/project-a", "created_at": "2026-10-15T01:00:00Z",
                   "agent_id": agent, "audience_id": audience, "input": "Group question"}
            conn.execute("INSERT INTO runs VALUES(?,?,?,?)", (name, run["scope"], run["created_at"], json.dumps(run)))
            candidate = {"id": name, "run_id": name, "scope": "/project-a",
                         "agent_id": agent, "audience_id": audience}
            conn.execute("INSERT INTO candidates VALUES(?,?,?,?,?,?)",
                         (name, "/project-a", name, json.dumps(candidate), agent, audience))
        conn.commit()
        conn.close()
        catalog = server.Catalog(self.home, None, all_chats=True)
        catalog.control_available = lambda: True
        self.assertTrue(catalog.index()["all_chats"])
        self.assertEqual(catalog.index()["database_count"], 3)
        result = catalog.memories()
        self.assertEqual(result["database_total"], 4)
        self.assertEqual(len(result["candidates"]), 2)
        for name in ("group-a", "group-b"):
            item = next(item for item in result["memories"] if item["memory"]["id"] == name)
            self.assertEqual(item["origin_run"]["id"], name)
            self.assertFalse(item["can_manage"])
            self.assertEqual([m["id"] for m in catalog.record("db:" + name)["memories"]], [name])
            for action in ("invalidate", "accept"):
                with self.assertRaises(KeyError):
                    catalog.action("db:" + name, action, "reason", 1)
        self.assertTrue(all(not item["can_manage"] for item in result["candidates"]))
        self.assertEqual(self.catalog.index()["database_count"], 1)
        self.assertEqual(memory_catalog(self.catalog)["database_total"], 2)
        unified = server.MultiCatalog([f"test={self.home}"], all_chats=True)
        self.assertTrue(unified.index()["all_chats"])
        self.assertTrue(unified.memories()["all_chats"])
        self.assertEqual(unified.record("test:db:group-b")["run"]["agent_id"], "helper")

    def test_external_memory_keeps_receipt_evidence_without_a_native_run(self):
        conn = self.database()
        conn.execute("CREATE TABLE external_memory_receipts(id TEXT PRIMARY KEY,payload TEXT)")
        memory = json.loads(conn.execute("SELECT payload FROM memories WHERE id='m-a'").fetchone()[0])
        memory.update(source_run_id="", external_receipt_id="external-1", evidence_ids=["u1"])
        receipt = {"id": "external-1", "digest": "digest", "received_at": "2026-10-15T01:00:00Z",
                   "connection": {"scope": "/project-a", "agent_id": "default", "audience_id": "owner"},
                   "submission": {"source": {"entry": "codex"}, "evidence": [
                       {"id": "u1", "role": "user", "text": "Selected original source"},
                       {"id": "not-cited", "role": "user", "text": "Other evidence"}]}}
        conn.execute("UPDATE memories SET payload=? WHERE id='m-a'", (json.dumps(memory),))
        conn.execute("INSERT INTO external_memory_receipts VALUES(?,?)", (receipt["id"], json.dumps(receipt)))
        conn.commit()
        conn.close()
        item = next(m for m in memory_catalog(self.catalog)["memories"] if m["memory"]["id"] == "m-a")
        self.assertIsNone(item["origin_run"])
        self.assertEqual(item["external_evidence"]["role"], "external_agent_supplied_evidence")
        self.assertEqual([e["id"] for e in item["external_evidence"]["evidence"]], ["u1"])

    def file_backend(self, memories, generation=1):
        """Represent a published file snapshot beside deliberately stale SQL."""
        home = self.home.resolve()
        memory_root = (self.root / ("memory-" + home.name)).resolve()
        cache_root = (self.root / "cache" / home.name / "memory").resolve()
        source_id = "channel:dingtalk"
        source_key = hashlib.sha256(source_id.encode()).hexdigest()[:24]
        source = memory_root / "sources" / source_key
        source.mkdir(parents=True, exist_ok=True)

        def write(path, payload):
            path.parent.mkdir(parents=True, exist_ok=True)
            body = (json.dumps(payload, ensure_ascii=False) + "\n").encode() if isinstance(payload, dict) else payload
            path.write_bytes(body)
            path.chmod(0o600)
            return hashlib.sha256(body).hexdigest()

        write(source / "publish.lock", b"")
        marker = {"version": 1, "memory_root": str(memory_root), "cache_root": str(cache_root),
                  "source_id": source_id, "state_home": str(home)}
        write(home / "file-memory.json", marker)
        domains, entries, normalized = {}, [], []
        for order, value in enumerate(memories, 1):
            memory = {"agent_id": "default", "audience_id": "owner", "revision": 1, **value}
            normalized.append(memory)
            domain_id = hashlib.sha256(json.dumps([source_id, memory["scope"], memory["agent_id"], memory["audience_id"]], separators=(",", ":")).encode()).hexdigest()[:32]
            domain = {"version": 1, "id": domain_id, "source_id": source_id, "state_home": str(home),
                      "scope": memory["scope"], "agent_id": memory["agent_id"], "audience_id": memory["audience_id"]}
            domains[domain_id] = domain
            write(memory_root / "domains" / domain_id / "scope.json", domain)
            base = memory_root / "domains" / domain_id / "entries" / memory["id"] / "revisions" / str(memory["revision"])
            record_hash = write(base / "record.json", {"version": 1, "source_id": source_id,
                                                      "state_home": str(home), "memory": memory})
            content_hash = write(base / "content.md", (memory["text"] + "\n").encode())
            entries.append({"id": memory["id"], "domain": domain_id, "directory": "entries",
                            "current": memory["revision"], "order": order,
                            "revisions": [{"revision": memory["revision"], "record_sha256": record_hash,
                                           "content_sha256": content_hash}], "evidence_runs": []})
        manifest = {"version": 1, "source_id": source_id, "state_home": str(home), "generation": generation,
                    "domains": list(domains.values()), "entries": entries}
        write(source / "current.json", manifest)
        index = cache_root / source_key / "index.db"
        self.write_file_index(index, normalized, generation)
        return marker, manifest, index, normalized

    @staticmethod
    def write_file_index(index, memories, generation):
        index.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(index)
        conn.executescript("CREATE TABLE memories(id TEXT PRIMARY KEY,scope TEXT,payload TEXT);"
                           "CREATE TABLE memory_annotations(memory_id TEXT PRIMARY KEY,payload TEXT);"
                           "CREATE TABLE file_index_checkpoint(id INTEGER PRIMARY KEY,generation INTEGER);")
        conn.execute("INSERT INTO file_index_checkpoint VALUES(1,?)", (generation,))
        for memory in memories:
            conn.execute("INSERT INTO memories VALUES(?,?,?)", (memory["id"], memory["scope"], json.dumps(memory)))
            conn.execute("INSERT INTO memory_annotations VALUES(?,?)", (memory["id"], '{"version":"entity-topic-v1","objects":[],"topics":[]}'))
        conn.commit()
        conn.close()
        index.chmod(0o600)

    def test_file_memory_views_current_revision_instead_of_stale_state_db(self):
        conn = self.database()
        old = json.loads(conn.execute("SELECT payload FROM memories WHERE id='m-a'").fetchone()[0])
        current = {**old, "revision": 2, "text": "The corrected project preference."}
        marker, manifest, index, normalized = self.file_backend([current])
        with patch.object(server.Catalog, "verify_file_memory", return_value={"generation": 1}):
            result = memory_catalog(server.Catalog(self.home, None, "/project-a"))
            self.assertEqual(result["database_total"], 1)
            self.assertEqual(result["memories"][0]["memory"]["revision"], 2)
            self.assertEqual(result["memories"][0]["memory"]["text"], current["text"])
            self.assertIn("文件记忆", result["memories"][0]["source"]["name"])
            record = server.Catalog(self.home, None).record("db:source-a")
            self.assertEqual(record["memories"], normalized)
        self.assertEqual(json.loads(conn.execute("SELECT payload FROM memories WHERE id='m-a'").fetchone()[0])["revision"], 1)
        conn.close()

    def test_file_memory_preserves_agent_audience_and_project_filters(self):
        conn = self.database()
        memories = [{"id": "owner-a", "scope": "/project-a", "text": "Owner private A", "status": "active"},
                    {"id": "owner-b", "scope": "/project-b", "text": "Owner private B", "status": "active"},
                    {"id": "group-a", "scope": "/project-a", "text": "Group knowledge", "status": "active",
                     "agent_id": "helper", "audience_id": "group-a"}]
        self.file_backend(memories)
        with patch.object(server.Catalog, "verify_file_memory", return_value={"generation": 1}):
            owner = memory_catalog(server.Catalog(self.home, None, "/project-a"))
            group = memory_catalog(server.Catalog(self.home, None, "/project-a", agent="helper", audience="group-a"))
            self.assertEqual([item["memory"]["id"] for item in owner["memories"]], ["owner-a"])
            self.assertEqual([item["memory"]["id"] for item in group["memories"]], ["group-a"])
        conn.close()

    def test_empty_go_publication_null_arrays_work_in_multi_channel_catalog(self):
        conn = self.database()
        conn.close()
        self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        populated_home = self.home
        self.home = self.root / "empty-home"
        conn = self.database()
        conn.close()
        marker, manifest, index, memories = self.file_backend([])
        # This is Go's canonical JSON representation of nil empty slices.
        manifest["domains"], manifest["entries"] = None, None
        source_key = hashlib.sha256(marker["source_id"].encode()).hexdigest()[:24]
        (Path(marker["memory_root"]) / "sources" / source_key / "current.json").write_text(json.dumps(manifest))
        with patch.object(server.Catalog, "verify_file_memory", return_value={"generation": 1}):
            result = server.MultiCatalog([f"populated={populated_home}", f"empty={self.home}"]).memories()
            self.assertEqual(result["database_total"], 1)
            self.assertEqual([item["key"] for item in result["memories"]], ["populated:db:current"])
            # Empty publication must not expose the stale SQL rows seeded above.
            self.assertEqual(server.Catalog(self.home, None).memories()["database_total"], 0)
        self.home = populated_home

    def test_missing_file_cache_is_rebuilt_only_through_host_verification(self):
        conn = self.database()
        marker, manifest, index, memories = self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        index.unlink()

        def verify():
            self.write_file_index(index, memories, 1)
            return {"generation": 1}

        with patch.object(self.catalog, "verify_file_memory", side_effect=verify) as verifier:
            self.assertEqual(memory_catalog(self.catalog)["database_total"], 1)
            verifier.assert_called_once_with()
        conn.close()

    def test_file_cache_corruption_and_missing_source_fail_without_legacy_fallback(self):
        for mutation in ("generation", "payload", "source", "missing_cache"):
            with self.subTest(mutation=mutation):
                home = self.home
                self.home = self.root / mutation
                catalog = server.Catalog(self.home, None)
                conn = self.database()
                marker, manifest, index, memories = self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
                if mutation in {"generation", "payload"}:
                    cache = sqlite3.connect(index)
                    if mutation == "generation":
                        cache.execute("UPDATE file_index_checkpoint SET generation=0")
                    else:
                        cache.execute("UPDATE memories SET payload=?", (json.dumps({**memories[0], "text": "STALE CACHE"}),))
                    cache.commit(); cache.close()
                elif mutation == "source":
                    entry = manifest["entries"][0]
                    (Path(marker["memory_root"]) / "domains" / entry["domain"] / "entries" / entry["id"] / "revisions" / "1" / "content.md").unlink()
                else:
                    index.unlink()
                with patch.object(catalog, "verify_file_memory", return_value={"generation": 1}):
                    with self.assertRaises((ValueError, FileNotFoundError)):
                        memory_catalog(catalog)
                conn.close()
                self.home = home

    def test_file_memory_verifier_is_explicit_and_errors_are_visible(self):
        conn = self.database()
        self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        binary = self.root / "host"
        binary.write_text("fixture"); binary.chmod(0o700)
        catalog = server.Catalog(self.home, None, host_binary=binary)
        completed = subprocess.CompletedProcess([], 0, b'{"generation":1}', b"")
        with patch.object(server.subprocess, "run", return_value=completed) as invocation:
            catalog.verify_file_memory()
            self.assertEqual(invocation.call_args.args[0], [str(binary.resolve()), "memory", "verify", "--home", str(self.home.resolve())])
        with patch.object(server.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, b"", b"document checksum mismatch")):
            with self.assertRaisesRegex(ValueError, "document checksum mismatch"):
                memory_catalog(catalog)
        with self.assertRaisesRegex(ValueError, "provide --host-binary"):
            memory_catalog(server.Catalog(self.home, None, host_binary=self.root / "missing-host"))
        conn.close()

    def test_unchanged_parallel_reads_share_one_complete_verification(self):
        conn = self.database()
        conn.close()
        self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        barrier = threading.Barrier(4)

        def read():
            barrier.wait(timeout=5)
            return self.catalog.record("db:source-a")["memories"][0]["text"]

        with patch.object(self.catalog, "verify_file_memory", return_value={"generation": 1}) as verifier, \
                patch.object(self.catalog, "validate_memory_cache", wraps=self.catalog.validate_memory_cache) as validation:
            with ThreadPoolExecutor(max_workers=4) as pool:
                self.assertEqual(list(pool.map(lambda _: read(), range(4))), ["Current knowledge"] * 4)
            self.assertEqual(memory_catalog(self.catalog)["database_total"], 1)
            verifier.assert_called_once_with()
            self.assertEqual(validation.call_count, 1)

    def test_publication_lock_metadata_refresh_does_not_invalidate_verified_knowledge(self):
        conn = self.database()
        conn.close()
        marker, _, _, _ = self.file_backend([
            {"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        key = hashlib.sha256(marker["source_id"].encode()).hexdigest()[:24]
        lock = Path(marker["memory_root"]) / "sources" / key / "publish.lock"

        def verify():
            # chmod to the existing mode changes ctime without changing knowledge.
            lock.chmod(0o600)
            os.utime(lock, ns=(1, 1))
            return {"generation": 1}

        with patch.object(self.catalog, "verify_file_memory", side_effect=verify) as verifier:
            self.assertEqual(memory_catalog(self.catalog)["database_total"], 1)
            lock.chmod(0o600)
            self.assertEqual(memory_catalog(self.catalog)["database_total"], 1)
            verifier.assert_called_once_with()

    def test_replaced_or_nonprivate_publication_lock_is_still_rejected(self):
        conn = self.database()
        conn.close()
        marker, _, _, _ = self.file_backend([
            {"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        key = hashlib.sha256(marker["source_id"].encode()).hexdigest()[:24]
        lock = Path(marker["memory_root"]) / "sources" / key / "publish.lock"

        def replace_lock():
            replacement = lock.with_name("replacement.lock")
            replacement.write_bytes(b"")
            replacement.chmod(0o600)
            os.replace(replacement, lock)
            return {"generation": 1}

        with patch.object(self.catalog, "verify_file_memory", side_effect=replace_lock):
            with self.assertRaisesRegex(ValueError, "changed during verification"):
                memory_catalog(self.catalog)
        self.assertIsNone(self.catalog._verified_memory_signature)
        lock.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "unsafe file-memory publication lock"):
            memory_catalog(self.catalog)

    def test_cached_reads_detect_same_size_edit_even_with_restored_mtime(self):
        conn = self.database()
        conn.close()
        marker, manifest, _, _ = self.file_backend([
            {"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        entry = manifest["entries"][0]
        content = Path(marker["memory_root"]) / "domains" / entry["domain"] / "entries/current/revisions/1/content.md"
        with patch.object(self.catalog, "verify_file_memory", return_value={"generation": 1}) as verifier:
            self.catalog.record("db:source-a")
            original, info = content.read_bytes(), content.stat()
            content.write_bytes(b"X" * len(original))
            os.utime(content, ns=(info.st_atime_ns, info.st_mtime_ns))
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                self.catalog.record("db:source-a")
            self.assertEqual(verifier.call_count, 2)
            content.write_bytes(original)
            self.assertEqual(self.catalog.record("db:source-a")["memories"][0]["text"], "Current knowledge")
            self.assertEqual(verifier.call_count, 3)

    def test_parallel_verifier_failures_are_shared_and_a_later_read_can_retry(self):
        conn = self.database()
        conn.close()
        self.file_backend([{"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        barrier = threading.Barrier(4)

        def read():
            barrier.wait(timeout=5)
            try:
                self.catalog.record("db:source-a")
            except ValueError as error:
                return str(error)

        with patch.object(self.catalog, "verify_file_memory", side_effect=ValueError("audit unavailable")) as verifier:
            with ThreadPoolExecutor(max_workers=4) as pool:
                self.assertEqual(list(pool.map(lambda _: read(), range(4))), ["audit unavailable"] * 4)
            verifier.assert_called_once_with()
        with patch.object(server.time, "monotonic", return_value=self.catalog._memory_verification_error[1] + 1), \
                patch.object(self.catalog, "verify_file_memory", return_value={"generation": 1}) as retry:
            self.assertEqual(self.catalog.record("db:source-a")["memories"][0]["text"], "Current knowledge")
            retry.assert_called_once_with()

    def test_changed_publication_and_cache_invalidate_verified_snapshot(self):
        conn = self.database()
        conn.close()
        marker, manifest, index, memories = self.file_backend([
            {"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        source_key = hashlib.sha256(marker["source_id"].encode()).hexdigest()[:24]
        current = Path(marker["memory_root"]) / "sources" / source_key / "current.json"
        with patch.object(self.catalog, "verify_file_memory", return_value={"generation": 1}) as verifier:
            self.catalog.record("db:source-a")
            manifest["generation"] = 2
            current.write_text(json.dumps(manifest))
            cache = sqlite3.connect(index)
            cache.execute("UPDATE file_index_checkpoint SET generation=2")
            cache.commit(); cache.close()
            self.catalog.record("db:source-a")
            self.assertEqual(verifier.call_count, 2)
            # Derived annotation edits must also invalidate the successful audit.
            cache = sqlite3.connect(index)
            cache.execute("UPDATE memory_annotations SET payload='{}'")
            cache.commit(); cache.close()
            self.catalog.record("db:source-a")
            self.assertEqual(verifier.call_count, 3)
            index.unlink()
            with patch.object(self.catalog, "verify_file_memory", side_effect=lambda: self.write_file_index(index, memories, 2)) as rebuild:
                self.catalog.record("db:source-a")
                rebuild.assert_called_once_with()

    def test_verifier_failure_and_mid_verification_edit_never_reuse_old_snapshot(self):
        conn = self.database()
        conn.close()
        marker, manifest, _, _ = self.file_backend([
            {"id": "current", "scope": "/project-a", "text": "Current knowledge", "status": "active"}])
        entry = manifest["entries"][0]
        content = Path(marker["memory_root"]) / "domains" / entry["domain"] / "entries/current/revisions/1/content.md"
        with patch.object(self.catalog, "verify_file_memory", side_effect=ValueError("audit unavailable")):
            with self.assertRaisesRegex(ValueError, "audit unavailable"):
                self.catalog.record("db:source-a")
        self.assertIsNone(self.catalog._verified_memory_signature)

        def changed_during_verification():
            content.write_bytes(content.read_bytes())

        with patch.object(self.catalog, "verify_file_memory", side_effect=changed_during_verification):
            self.catalog._memory_verification_error = None
            with self.assertRaisesRegex(ValueError, "changed during verification"):
                self.catalog.record("db:source-a")
        self.assertIsNone(self.catalog._verified_memory_signature)

    def archive(self, name="native.json"):
        path = Path(__file__).parent / "fixtures/attributed-retrieval.json"
        shutil.copyfile(path, self.reports / name)

    def test_live_rows_scope_and_bytes_preserved(self):
        conn = self.database()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        result = memory_catalog(server.Catalog(self.home, self.reports, "/project-a"))
        self.assertEqual(result["observed_total"], 1)
        item = result["memories"][0]
        self.assertEqual(item["memory"]["id"], "m-a")
        self.assertEqual(item["annotation_origin"], "stored")
        self.assertEqual(item["origin_run"]["id"], "source-a")
        self.assertIsNone(item["decision"])
        self.assertIn("<img", item["memory"]["text"])
        self.assertNotIn("synthetic-reply-capability", json.dumps(result))
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        self.assertEqual(conn.execute("PRAGMA user_version").fetchone()[0], 7)
        conn.close()

    def test_missing_database_is_empty_without_creation(self):
        self.assertEqual(memory_catalog(self.catalog)["memories"], [])
        self.assertFalse(self.home.exists())

    def test_memory_library_filters_same_scope_by_agent_and_audience(self):
        conn = self.database()
        conn.executescript("ALTER TABLE memories ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default';"
                           "ALTER TABLE memories ADD COLUMN audience_id TEXT NOT NULL DEFAULT 'owner';"
                           "PRAGMA user_version=10;")
        for name, agent, audience in (("a", "helper", "group-a"), ("b", "helper", "group-b"), ("other", "reviewer", "group-a")):
            memory = {"id": name, "scope": "/project-a", "text": "member preference " + name,
                      "kind": "preference", "status": "active", "agent_id": agent,
                      "audience_id": audience, "sender_id": "member-" + name, "source_run_id": "run-" + name}
            conn.execute("INSERT INTO memories VALUES(?,?,?,?,?)", (name, "/project-a", json.dumps(memory), agent, audience))
            conn.execute("INSERT INTO memory_annotations VALUES(?,?)", (name, json.dumps({"version": "entity-topic-v1", "objects": [], "topics": []})))
            run = {"id": "run-" + name, "agent_id": agent, "audience_id": audience, "scope": "/project-a", "context": [memory]}
            conn.execute("INSERT INTO runs VALUES(?,?,?,?)", (run["id"], run["scope"], "2026-10-11T00:00:00Z", json.dumps(run)))
        conn.commit()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        result = memory_catalog(server.Catalog(self.home, self.reports, "/project-a", agent="helper", audience="group-a"))
        self.assertEqual(result["database_total"], 1)
        self.assertEqual(result["database_runs_total"], 1)
        self.assertEqual([item["memory"]["id"] for item in result["memories"]], ["a"])
        item = result["memories"][0]
        self.assertEqual(item["memory"]["sender_id"], "member-a")
        self.assertEqual(item["origin_run"]["audience_id"], "group-a")
        self.assertEqual([r["id"] for r in result["recalls"]], ["run-a"])
        self.assertEqual(memory_catalog(self.catalog)["database_total"], 2)
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        conn.close()

    def test_joint_run_records_each_scopes_recall_decisions(self):
        conn = self.database()
        conn.execute("PRAGMA user_version=9")
        conn.execute("INSERT INTO projects VALUES('a','A','/project-a','/project-a','','')")
        conn.execute("INSERT INTO projects VALUES('b','B','/project-b','/project-b','','')")
        projects = [{"id": "a", "scope": "/project-a", "workspace": "/project-a"},
                    {"id": "b", "scope": "/project-b", "workspace": "/project-b"}]
        run = {"id": "joint", "scope": "neutral", "workspace": "/neutral", "projects": projects,
               "context": [json.loads(conn.execute("SELECT payload FROM memories WHERE id='m-a'").fetchone()[0])]}
        conn.execute("INSERT INTO runs VALUES(?,?,?,?)", ("joint", "neutral", "2026-10-10T07:00:00Z", json.dumps(run)))
        for seq, scope, memory_id, accepted in ((1, "/project-a", "m-a", True), (2, "/project-b", "m-b", False)):
            event = {"type": "memory.retrieval.evaluated", "data": {"scope": scope,
                     "decisions": [{"memory_id": memory_id, "accepted": accepted, "reason": scope}]}}
            conn.execute("INSERT INTO events VALUES(?,?,?)", ("joint", seq, json.dumps(event)))
        conn.commit()
        result = memory_catalog(server.Catalog(self.home, self.reports, project="b"))
        self.assertEqual(result["database_runs_total"], 1)
        item = result["memories"][0]
        self.assertEqual(item["memory"]["id"], "m-b")
        self.assertEqual(item["uses"][0]["status"], "excluded")
        self.assertEqual(item["uses"][0]["reason"], "/project-b")
        self.assertEqual(len(result["recalls"][0]["decisions"]), 2)
        conn.close()

    def test_project_and_workspace_filters_keep_memory_after_checkout_moves(self):
        conn = self.database()
        conn.execute("INSERT INTO projects VALUES('app','Application','/project-a','/project-a','','')")
        conn.commit()
        selected = server.Catalog(self.home, self.reports, project="app")
        self.assertEqual(memory_catalog(selected)["workspace"], "/project-a")
        conn.execute("UPDATE projects SET workspace='/checkout/moved' WHERE id='app'")
        conn.commit()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        for catalog in (selected, server.Catalog(self.home, self.reports, "/checkout/moved")):
            with self.subTest(workspace=catalog.workspace):
                result = memory_catalog(catalog)
                self.assertEqual(result["project"], "app")
                self.assertEqual(result["workspace"], "/checkout/moved")
                self.assertEqual(result["scope"], "/project-a")
                self.assertEqual(result["database_total"], 1)
                self.assertEqual(result["database_runs_total"], 1)
                item = result["memories"][0]
                self.assertEqual(item["memory"]["id"], "m-a")
                self.assertEqual(item["origin_run"]["workspace"], "/project-a")
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        conn.close()

    def test_unknown_project_errors_without_creating_state(self):
        catalog = server.Catalog(self.home, self.reports, project="absent")
        with self.assertRaisesRegex(ValueError, "unknown project: absent"):
            memory_catalog(catalog)
        self.assertFalse(self.home.exists())
        conn = self.database()
        with self.assertRaisesRegex(ValueError, "unknown project: absent"):
            memory_catalog(catalog)
        conn.close()

    def test_missing_stored_annotation_stops_read(self):
        conn = self.database()
        conn.execute("DELETE FROM memory_annotations WHERE memory_id='m-a'")
        conn.commit()
        with self.assertRaisesRegex(ValueError, "missing stored annotation"):
            memory_catalog(self.catalog)
        conn.close()

    def test_report_three_memories_two_objects_and_recorded_judgments(self):
        self.archive()
        result = memory_catalog(self.catalog)
        self.assertEqual(len(result["memories"]), 3)
        objects = {o["id"] for item in result["memories"] for o in item["annotation"]["objects"]}
        self.assertEqual(len(objects), 2)
        self.assertEqual(len(result["recalls"]), 2)
        self.assertTrue(all(item["snapshot"] for item in result["memories"]))
        self.assertTrue(all(item["annotation_origin"] == "recorded_input" for item in result["memories"]))
        order = next(item for item in result["memories"] if "PostgreSQL" in item["memory"]["text"])
        self.assertEqual(order["decision"]["eligibility"]["choice"], "remember")
        self.assertEqual(order["decision"]["memory_kind"]["choice"], "project_fact")
        billing = next(item for item in result["memories"] if "MySQL" in item["memory"]["text"])
        self.assertIsNone(billing["origin_run"])
        self.assertIsNone(billing["decision"])
        self.assertTrue(any(use["status"] == "excluded" and use["reason"] == "different_object" for use in billing["uses"]))
        self.assertEqual(len(self.catalog.index()["records"]), 5)

    def test_duplicate_ids_in_different_reports_remain_separate(self):
        self.archive("one.json")
        self.archive("two.json")
        items = memory_catalog(self.catalog)["memories"]
        self.assertEqual(len(items), 6)
        self.assertEqual(len({item["key"] for item in items}), 6)
        self.assertEqual(len({item["memory"]["id"] for item in items}), 3)

    def test_latest_inspection_preserves_invalidation_over_old_context(self):
        path = Path(__file__).parent / "fixtures/reviewed-experience.json"
        shutil.copyfile(path, self.reports / "experience.json")
        items = memory_catalog(self.catalog)["memories"]
        procedure = next(item for item in items if item["memory"]["source_type"] == "reviewed_experience")
        self.assertEqual(procedure["memory"]["status"], "invalidated")
        self.assertTrue(any(use["status"] == "injected" for use in procedure["uses"]))
        self.assertIsNone(procedure["annotation"])
        self.assertIsNotNone(procedure["origin_run"])
        # The source run's user-statement rejection is not the procedure's admission.
        self.assertIsNone(procedure["decision"])

    def test_unsupported_report_remains_an_explicit_error(self):
        (self.reports / "bad.json").write_text('{"commands":[{"events":[]}]}')
        with self.assertRaisesRegex(ValueError, "inspected run"):
            memory_catalog(self.catalog)

    def test_limits_do_not_claim_complete_library(self):
        conn = self.database()
        for i in range(201):
            memory = {"id": f"extra-{i}", "text": "A fact", "scope": "/project-a", "status": "active"}
            conn.execute("INSERT INTO memories VALUES(?,?,?)", (memory["id"], memory["scope"], json.dumps(memory)))
            conn.execute("INSERT INTO memory_annotations VALUES(?,?)", (memory["id"], '{"version":"entity-topic-v1","objects":[],"topics":[]}'))
        conn.commit()
        result = memory_catalog(self.catalog)
        self.assertEqual(result["observed_total"], 203)
        self.assertEqual(len(result["memories"]), 200)
        self.assertTrue(result["truncated"])
        conn.close()

    def test_memory_http_assets_and_write_boundary(self):
        self.archive()
        http = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        http.catalog = self.catalog
        worker = threading.Thread(target=http.serve_forever, daemon=True)
        worker.start()
        base = f"http://127.0.0.1:{http.server_port}"
        try:
            for route in ("/memory.html", "/memory.js", "/memory.css", "/api/memories"):
                with urlopen(base + route) as response:
                    self.assertEqual(response.status, 200)
                    self.assertIn("script-src 'self'", response.headers["Content-Security-Policy"])
                    if route == "/api/memories":
                        self.assertEqual(len(json.load(response)["memories"]), 3)
            for method, headers, expected in (("POST", {}, 405), ("GET", {"Host": "outside.example"}, 403)):
                with self.assertRaises(HTTPError) as error:
                    urlopen(Request(base + "/api/memories", method=method, headers=headers))
                self.assertEqual(error.exception.code, expected)
                error.exception.close()
        finally:
            http.shutdown(); http.server_close(); worker.join()

    def test_candidate_source_scope_and_live_host_forwarding(self):
        conn = self.database()
        for name, scope in (("candidate-a", "/project-a"), ("candidate-b", "/project-b")):
            candidate = {"id": name, "scope": scope, "run_id": "source-a", "status": "pending",
                         "draft": {"title": "Fixture", "summary": "Fixture evidence", "steps": [], "evidence_ids": []},
                         "assessment": {"eligible": True}, "created_at": "2026-10-13T00:00:00Z"}
            conn.execute("INSERT INTO candidates(id,scope,run_id,payload) VALUES(?,?,?,?)", (name, scope, "source-a", json.dumps(candidate)))
        conn.commit(); conn.close()
        requests = []

        class HostHandler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                body = b'{"ok":true,"result":{"status":"accepted"}}'
                self.send_response(200); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

            def log_message(self, *args):
                pass

        path = self.home / "memory-control.sock"
        host = socketserver.UnixStreamServer(str(path), HostHandler)
        os.chmod(path, 0o600)
        worker = threading.Thread(target=host.serve_forever, daemon=True); worker.start()
        try:
            selected = server.Catalog(self.home, self.reports, "/project-a")
            candidates = selected.memories()["candidates"]
            self.assertEqual([item["key"] for item in candidates], ["db:candidate-a"])
            self.assertTrue(candidates[0]["can_manage"])
            with self.assertRaises(KeyError):
                selected.action("db:candidate-b", "accept", "reviewed", 0)
            with self.assertRaises(ValueError):
                selected.action("report-123:candidate-a", "accept", "reviewed", 0)
            self.assertEqual(requests, [])
            status, result = selected.action("db:candidate-a", "accept", "reviewed", 0)
            self.assertEqual(status, 200); self.assertTrue(result["ok"])
            self.assertEqual(requests, [{"action": "accept", "id": "candidate-a", "agent_id": "default", "audience_id": "owner", "note": "reviewed", "revision": 0}])
            sources = server.MultiCatalog([f"dingtalk={self.home}", f"feishu={self.home}"]).memories()["candidates"]
            self.assertEqual({item["key"] for item in sources}, {"dingtalk:db:candidate-a", "dingtalk:db:candidate-b", "feishu:db:candidate-a", "feishu:db:candidate-b"})
        finally:
            host.shutdown(); host.server_close(); worker.join()

    def test_memory_action_requires_same_origin_token_and_bounded_action(self):
        self.archive()
        http = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        http.catalog = self.catalog
        http.mutation_token = "fixture-token"
        calls = []
        worker = threading.Thread(target=http.serve_forever, daemon=True); worker.start()
        base = f"http://127.0.0.1:{http.server_port}"
        body = json.dumps({"key": "db:fixture", "action": "reject", "note": "unsupported", "revision": 0}).encode()
        headers = {"Content-Type": "application/json", "Origin": base, "X-Host-Action": "fixture-token"}
        try:
            with patch.object(self.catalog, "action", side_effect=lambda *args: (calls.append(args) or (200, {"ok": True}))):
                for changed in ({"Origin": "http://outside.example"}, {"X-Host-Action": "wrong"}, {"Host": "outside.example"}, {"Origin": "null"}):
                    with self.assertRaises(HTTPError) as error:
                        urlopen(Request(base + "/api/memory-action", data=body, headers={**headers, **changed}))
                    self.assertEqual(error.exception.code, 403); error.exception.close()
                self.assertEqual(calls, [])
                for bad in (b'{}', b'{"action":"shell"}', b'x' * 8193):
                    with self.assertRaises(HTTPError) as error:
                        urlopen(Request(base + "/api/memory-action", data=bad, headers=headers))
                    self.assertEqual(error.exception.code, 400); error.exception.close()
                with urlopen(Request(base + "/api/memory-action", data=body, headers=headers)) as response:
                    self.assertTrue(json.load(response)["ok"])
                self.assertEqual(calls, [("db:fixture", "reject", "unsupported", 0)])
        finally:
            http.shutdown(); http.server_close(); worker.join()


if __name__ == "__main__":
    unittest.main()
