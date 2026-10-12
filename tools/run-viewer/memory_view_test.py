"""Verify memory provenance, source separation and the read-only HTTP boundary."""

import hashlib
import json
from pathlib import Path
import shutil
import sqlite3
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server
from memory_view import memory_catalog


class MemoryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="agent-host-memory-view-test-")
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


if __name__ == "__main__":
    unittest.main()
