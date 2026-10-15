"""Exercise read-only loading, archived review provenance, and HTTP boundaries."""

import hashlib
import importlib.util
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

SPEC = importlib.util.spec_from_file_location("viewer", Path(__file__).with_name("server.py"))
viewer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(viewer)


def write_run(conn, run_id, scope="/test/project", **fields):
    run = {"id": run_id, "scope": scope, "created_at": "2026-10-10T05:30:00Z",
           "input": '<img src=x onerror="alert(1)">', "execution_status": "running",
           "acceptance_status": "unverified", "experience_status": "pending", **fields}
    conn.execute("INSERT INTO runs VALUES(?,?,?,?)", (run_id, scope, run["created_at"], json.dumps(run)))
    conn.commit()


class CatalogTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="agent-host-viewer-test-")
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.reports = self.root / "reports"
        self.reports.mkdir()
        self.catalog = viewer.Catalog(self.home, self.reports)

    def tearDown(self):
        self.temporary.cleanup()

    def database(self, version=2):
        conn = sqlite3.connect(self.home / "state.db")
        conn.executescript("CREATE TABLE runs(id TEXT PRIMARY KEY,scope TEXT,created_at TEXT,payload TEXT);"
                           "CREATE TABLE events(run_id TEXT,seq INTEGER,payload TEXT);"
                           "CREATE TABLE memories(id TEXT,scope TEXT,payload TEXT);")
        if version >= 2:
            conn.execute("CREATE TABLE candidates(id TEXT,run_id TEXT,payload TEXT)")
        if version >= 7:
            conn.execute("CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT,workspace TEXT,scope TEXT,created_at TEXT,updated_at TEXT)")
        conn.execute(f"PRAGMA user_version={version}")
        write_run(conn, "first")
        return conn

    def test_unified_channels_preserve_colliding_ids_and_memory_links(self):
        first = self.database()
        event = {"type": "tool.called", "data": {"key": "unaltered-tool-input"}}
        first.execute("INSERT INTO events VALUES(?,?,?)", ("first", 1, json.dumps(event)))
        memory = {"id": "same-memory", "text": "saved statement", "scope": "/test/project",
                  "source_run_id": "first", "status": "active"}
        first.execute("INSERT INTO memories VALUES(?,?,?)", (memory["id"], memory["scope"], json.dumps(memory)))
        first.commit()
        first.close()
        other = self.root / "other"
        other.mkdir()
        import shutil
        shutil.copyfile(self.home / "state.db", other / "state.db")
        unified = viewer.MultiCatalog([f"dingtalk={self.home}", f"feishu={other}"])
        index = unified.index()
        self.assertEqual(index["database_count"], 2)
        self.assertEqual({row["key"] for row in index["records"]}, {"dingtalk:db:first", "feishu:db:first"})
        for channel in ("dingtalk", "feishu"):
            detail = unified.record(channel + ":db:first")
            self.assertEqual(detail["events"], [event])
            self.assertEqual(detail["source"]["channel"], channel)
            self.assertEqual(detail["run"]["id"], "first")
        memories = unified.memories()
        self.assertEqual(memories["database_total"], 2)
        self.assertEqual({item["key"] for item in memories["memories"]},
                         {"dingtalk:db:same-memory", "feishu:db:same-memory"})
        for item in memories["memories"]:
            self.assertEqual(unified.record(item["origin_run"]["key"])["source"], item["source"])
        with self.assertRaises(KeyError):
            unified.record("unknown:db:first")
        with self.assertRaises(ValueError):
            viewer.MultiCatalog([f"dingtalk={self.home}", f"dingtalk={other}"])
        broken = viewer.MultiCatalog([f"dingtalk={self.home}", f"broken={other}"])
        conn = sqlite3.connect(other / "state.db")
        conn.execute("PRAGMA user_version=999")
        conn.close()
        with self.assertRaises(ValueError):
            broken.index()

    def test_read_only_snapshot_and_new_records(self):
        conn = self.database()
        event = {"run_id": "first", "seq": 1, "type": "run.started", "phase": "request"}
        conn.execute("INSERT INTO events VALUES(?,?,?)", ("first", 1, json.dumps(event)))
        conn.commit()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        self.assertEqual(self.catalog.index()["database_count"], 1)
        detail = self.catalog.record("db:first")
        self.assertEqual(detail["events"], [event])
        self.assertIn("<img", detail["run"]["input"])
        readonly = self.catalog.connection()
        with self.assertRaises(sqlite3.OperationalError):
            readonly.execute("DELETE FROM runs")
        readonly.close()
        self.assertEqual(conn.execute("PRAGMA user_version").fetchone()[0], 2)
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        write_run(conn, "second")
        self.assertEqual(self.catalog.index()["database_count"], 2)
        self.assertEqual(self.catalog.record("db:second")["run"]["id"], "second")
        conn.close()

    def test_missing_database_does_not_create_state(self):
        missing = self.root / "not-created"
        self.assertEqual(viewer.Catalog(missing, self.reports).index()["records"], [])
        self.assertFalse(missing.exists())

    def test_disabled_reports_reads_only_current_database(self):
        conn = self.database()
        (self.reports / "unrelated.json").write_text('{"not":"a valid report"}')
        catalog = viewer.Catalog(self.home, None)
        self.assertEqual(catalog.archives(), {})
        self.assertEqual(catalog.index()["database_count"], 1)
        conn.close()

    def test_v1_does_not_migrate_or_require_candidates(self):
        conn = self.database(1)
        self.assertEqual(self.catalog.record("db:first")["candidates"], [])
        self.assertEqual(conn.execute("PRAGMA user_version").fetchone()[0], 1)
        self.assertEqual(conn.execute("SELECT count(*) FROM sqlite_master WHERE name='candidates'").fetchone()[0], 0)
        conn.close()

    def test_joint_run_is_visible_from_each_member_and_loads_member_memory(self):
        conn = self.database(9)
        conn.execute("INSERT INTO projects VALUES('a','A','/a','project:a','','')")
        conn.execute("INSERT INTO projects VALUES('b','B','/b','project:b','','')")
        projects = [{"id": "a", "name": "A", "workspace": "/a", "scope": "project:a"},
                    {"id": "b", "name": "B", "workspace": "/b", "scope": "project:b"}]
        write_run(conn, "joint", scope="neutral", workspace="/neutral", projects=projects,
                  additional_directories=["/a", "/b"])
        for scope in ("project:a", "project:b", "neutral"):
            conn.execute("INSERT INTO memories VALUES(?,?,?)", (scope, scope, json.dumps({"id": scope})))
        conn.commit()
        for project in ("a", "b"):
            catalog = viewer.Catalog(self.home, self.reports, project=project)
            self.assertEqual(catalog.index()["database_count"], 1)
            row = catalog.record("db:joint")
            self.assertEqual(row["run"]["workspace"], "/neutral")
            self.assertEqual({m["id"] for m in row["memories"]}, {"project:a", "project:b"})
        self.assertEqual(conn.execute("PRAGMA user_version").fetchone()[0], 9)
        conn.close()

    def test_unknown_schema_and_workspace_scope_fail_explicitly(self):
        conn = self.database()
        filtered = viewer.Catalog(self.home, self.reports, "/different/project")
        self.assertEqual(filtered.index()["database_count"], 0)
        with self.assertRaises(KeyError):
            filtered.record("db:first")
        conn.execute("PRAGMA user_version=99")
        with self.assertRaisesRegex(ValueError, "unsupported state schema"):
            self.catalog.record("db:first")
        conn.close()

    def test_instance_audience_filters_run_detail_memory_and_archives(self):
        conn = self.database(10)
        conn.execute("ALTER TABLE memories ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default'")
        conn.execute("ALTER TABLE memories ADD COLUMN audience_id TEXT NOT NULL DEFAULT 'owner'")
        conn.execute("INSERT INTO projects VALUES('app','Application','/checkout','/test/project','','')")
        for name, agent, audience in (("owner", "default", "owner"), ("a", "helper", "group-a"),
                                      ("b", "helper", "group-b"), ("review", "reviewer", "group-a")):
            memory = {"id": "memory-" + name, "scope": "/test/project", "text": "remember " + name,
                      "agent_id": agent, "audience_id": audience}
            conn.execute("INSERT INTO memories VALUES(?,?,?,?,?)", (memory["id"], memory["scope"], json.dumps(memory), agent, audience))
            write_run(conn, name, agent_id=agent, audience_id=audience,
                      projects=[{"id": "app", "scope": "/test/project"}])
        report = {"runs": [{"run_id": "archive-owner"}, {"run_id": "archive-group", "agent_id": "helper", "audience_id": "group-a"}]}
        (self.reports / "partitions.json").write_text(json.dumps(report))
        conn.commit()
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        selected = viewer.Catalog(self.home, self.reports, project="app", agent="helper", audience="group-a")
        index = selected.index()
        self.assertEqual(index["agent_id"], "helper")
        self.assertEqual(index["audience_id"], "group-a")
        self.assertEqual({r["id"] for r in index["records"]}, {"a", "archive-group"})
        self.assertEqual(index["database_count"], 1)
        self.assertEqual([m["id"] for m in selected.record("db:a")["memories"]], ["memory-a"])
        for name in ("owner", "b", "review", "first"):
            with self.assertRaisesRegex(KeyError, "agent or audience"):
                selected.record("db:" + name)
        archived_owner = next(r["key"] for r in self.catalog.index()["records"] if r["id"] == "archive-owner")
        with self.assertRaises(KeyError):
            selected.record(archived_owner)
        self.assertEqual({r["id"] for r in self.catalog.index()["records"]}, {"first", "owner", "archive-owner"})
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        conn.close()

    def test_project_filter_keeps_history_after_workspace_update(self):
        conn = self.database(7)
        conn.execute("INSERT INTO projects VALUES('app','Application','/checkout/old','project:app','','')")
        write_run(conn, "old", "project:app", project_id="app", project_name="Application",
                  workspace="/checkout/old", session_id="host-old", native_session_id="native-old",
                  runtime_fingerprint="runtime-old")
        filtered = viewer.Catalog(self.home, self.reports, project="app")
        self.assertEqual(filtered.index()["workspace"], "/checkout/old")
        conn.execute("UPDATE projects SET workspace='/checkout/new' WHERE id='app'")
        write_run(conn, "new", "project:app", project_id="app", project_name="Application",
                  workspace="/checkout/new", session_id="host-new", native_session_id="native-new")
        before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
        for catalog in (filtered, viewer.Catalog(self.home, self.reports, "/checkout/new")):
            with self.subTest(workspace=catalog.workspace):
                index = catalog.index()
                self.assertEqual(index["database_count"], 2)
                self.assertEqual(index["project"], "app")
                self.assertEqual(index["scope"], "project:app")
                self.assertEqual(index["workspace"], "/checkout/new")
                rows = {r["id"]: r for r in index["records"]}
                self.assertEqual(rows["old"]["workspace"], "/checkout/old")
                self.assertEqual(rows["new"]["workspace"], "/checkout/new")
                self.assertEqual(rows["old"]["session_id"], "host-old")
                self.assertEqual(rows["old"]["native_session_id"], "native-old")
                self.assertEqual(rows["old"]["runtime_fingerprint"], "runtime-old")
                self.assertEqual(catalog.record("db:old")["run"]["workspace"], "/checkout/old")
                with self.assertRaises(KeyError):
                    catalog.record("db:first")
        self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        conn.close()

    def test_registered_legacy_scope_and_unregistered_workspace_fallback(self):
        conn = self.database(7)
        conn.execute("INSERT INTO projects VALUES('legacy','Legacy','/checkout/moved','/test/project','','')")
        conn.commit()
        for catalog in (viewer.Catalog(self.home, self.reports, project="legacy"),
                        viewer.Catalog(self.home, self.reports, "/checkout/moved"),
                        viewer.Catalog(self.home, self.reports, "/test/project")):
            row = catalog.index()["records"][0]
            self.assertEqual(row["workspace"], "/test/project")
            self.assertEqual(row["project_id"], "")
            self.assertNotIn("workspace", catalog.record("db:first")["run"])
        conn.close()

    def test_unknown_project_is_explicit_with_missing_or_legacy_database(self):
        for version in (None, 2, 7):
            with self.subTest(version=version):
                if version is not None:
                    conn = self.database(version)
                    conn.close()
                with self.assertRaisesRegex(ValueError, "unknown project: absent"):
                    viewer.Catalog(self.home, self.reports, project="absent").index()
                if version is not None:
                    (self.home / "state.db").unlink()
        with self.assertRaisesRegex(ValueError, "mutually exclusive"):
            viewer.Catalog(self.home, self.reports, "/checkout", "app")

    def test_cli_rejects_unknown_project_and_conflicting_filters(self):
        for extra, message in ((["--project", "absent"], "unknown project: absent"),
                               (["--project", "app", "--workspace", "/checkout"], "not allowed with argument")):
            result = subprocess.run([sys.executable, "-B", str(viewer.ASSETS / "server.py"),
                                     "--home", str(self.home), "--reports", str(self.reports), *extra],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 2)
            self.assertIn(message, result.stderr)

    def test_archives_preserve_workspace_scope_and_project_snapshots(self):
        report = {"runs": [{"run_id": "new", "recalled": None}, {"run_id": "legacy"}], "commands": [{"events": [
            {"run_id": "new", "seq": 1, "type": "run.started", "data": {
                "workspace": "/checkout/new", "scope": "project:app", "project_id": "app",
                "project_name": "Application", "runtime_fingerprint": "runtime-1"}},
            {"run_id": "new", "seq": 2, "type": "agent.dispatched", "data": {"native_session_id": "dispatched-session"}},
            {"run_id": "new", "seq": 3, "type": "agent.started", "data": {"native_session_id": "started-session"}},
            {"run_id": "legacy", "seq": 1, "type": "run.started", "data": {"workspace": "/checkout/legacy"}}
        ]}]}
        (self.reports / "projects.json").write_text(json.dumps(report))
        records = viewer.Catalog(self.home, self.reports, "/different").index()["records"]
        self.assertEqual(len(records), 2)
        rows = {r["id"]: r for r in records}
        self.assertEqual(rows["new"]["scope"], "project:app")
        self.assertEqual(rows["new"]["workspace"], "/checkout/new")
        self.assertEqual(rows["new"]["project_id"], "app")
        self.assertEqual(rows["new"]["runtime_fingerprint"], "runtime-1")
        self.assertEqual(rows["new"]["native_session_id"], "started-session")
        self.assertEqual(rows["legacy"]["scope"], "/checkout/legacy")
        self.assertEqual(rows["legacy"]["workspace"], "/checkout/legacy")
        self.assertIsNone(rows["legacy"]["native_session_id"])

    def test_archive_native_session_fallback_preserves_recorded_values(self):
        for row, events, expected in (
            ({"run_id": "r"}, [{"type": "agent.dispatched", "data": {"native_session_id": "dispatched-session"}}], "dispatched-session"),
            ({"run_id": "r", "native_session_id": "saved-session"}, [{"type": "agent.started", "data": {"native_session_id": "started-session"}}], "saved-session"),
            ({"run_id": "r"}, [{"type": "agent.started", "data": {"native_session_id": ""}}], None),
        ):
            with self.subTest(expected=expected):
                report = {"runs": [row], "commands": [{"events": [
                    {"run_id": "r", "seq": seq, **event} for seq, event in enumerate(events, 1)
                ]}]}
                record = viewer.report_records(report, {"id": "fixture"})["r"]
                self.assertEqual(record["run"]["native_session_id"], expected)

    def test_additive_schemas_preserve_provenance_without_reading_reply_capabilities(self):
        conn = self.database(3)
        run = json.loads(conn.execute("SELECT payload FROM runs WHERE id='first'").fetchone()[0])
        run["source"] = {"channel": "dingtalk", "channel_message_id": "message-1"}
        conn.execute("UPDATE runs SET payload=? WHERE id='first'", (json.dumps(run),))
        conn.execute("CREATE TABLE channel_messages(id TEXT,reply_url TEXT,input_capabilities TEXT)")
        conn.execute("INSERT INTO channel_messages VALUES('message-1','synthetic-private-reply-capability','synthetic-private-input-capability')")
        conn.execute("CREATE TABLE channel_reactions(id INTEGER,channel_message_id TEXT,provider_reaction_id TEXT)")
        conn.execute("INSERT INTO channel_reactions VALUES(1,'message-1','synthetic-private-provider-reaction-id')")
        conn.execute("CREATE TABLE discovery_fixture(payload TEXT)")
        conn.execute("INSERT INTO discovery_fixture VALUES('synthetic-private-discovery-cache')")
        conn.execute("INSERT INTO candidates VALUES('candidate-1','first',?)",
                     (json.dumps({"id": "candidate-1", "status": "pending"}),))
        conn.commit()
        # The v10 memory partition columns are additive to this fixture. The
        # viewer must not inspect private tables to read run evidence, including
        # the provider reaction ID added in v14.
        conn.execute("ALTER TABLE memories ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default'")
        conn.execute("ALTER TABLE memories ADD COLUMN audience_id TEXT NOT NULL DEFAULT 'owner'")
        conn.commit()
        for version in (3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14):
            with self.subTest(version=version):
                conn.execute(f"PRAGMA user_version={version}")
                before = hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest()
                index = self.catalog.index()
                self.assertEqual(index["database_count"], 1)
                detail = self.catalog.record("db:first")
                self.assertEqual(detail["run"]["source"], run["source"])
                self.assertEqual(detail["candidates"][0]["id"], "candidate-1")
                for private in ("synthetic-private-reply-capability", "synthetic-private-input-capability",
                                "synthetic-private-discovery-cache", "synthetic-private-provider-reaction-id"):
                    self.assertNotIn(private, json.dumps(index))
                    self.assertNotIn(private, json.dumps(detail))
                self.assertEqual(conn.execute("PRAGMA user_version").fetchone()[0], version)
                readonly = self.catalog.connection()
                with self.assertRaises(sqlite3.OperationalError):
                    readonly.execute("UPDATE channel_reactions SET provider_reaction_id='changed'")
                readonly.close()
                self.assertEqual(hashlib.sha256((self.home / "state.db").read_bytes()).hexdigest(), before)
        conn.close()

    def test_saved_reports_preserve_later_review_and_invalidation(self):
        report = json.loads((Path(__file__).parent / "fixtures/reviewed-experience.json").read_text())
        records = viewer.report_records(report, {"id": "fixture", "kind": "report"})
        repair = next(r for r in records.values() if r["run"]["label"] == "repair")
        self.assertEqual(repair["run"]["experience_status"], "review_pending")
        self.assertEqual(repair["run"]["acceptance_status"], "unverified")
        receipt = repair["receipt"]["seq"]
        accepted = next(e for e in repair["events"] if e["type"] == "candidate.accepted")
        self.assertGreater(accepted["seq"], receipt)
        self.assertEqual(accepted["data"]["memory"]["status"], "active")
        self.assertEqual(repair["memories"][0]["status"], "invalidated")
        reuse = next(r for r in records.values() if r["run"]["label"] == "reuse")
        self.assertEqual(len(reuse["run"]["context"]), 1)
        self.assertEqual(reuse["run"]["context"][0]["status"], "active")
        self.assertEqual(reuse["memories"][0]["status"], "invalidated")
        invalidated = next(r for r in records.values() if r["run"]["label"] == "after_invalidation")
        self.assertEqual(invalidated["run"]["context"], [])
        rejected_report = json.loads((Path(__file__).parent / "fixtures/rejected-experience.json").read_text())
        rejected = next(r for r in viewer.report_records(rejected_report, {"id": "fixture"}).values() if r["run"]["label"] == "repair")
        self.assertEqual(rejected["run"]["experience_status"], "empty")
        self.assertFalse(next(e for e in rejected["events"] if e["type"] == "experience.assessed")["data"]["assessment"]["eligible"])

    def test_malformed_report_is_not_silently_skipped(self):
        (self.reports / "broken.json").write_text('{"runs": [], "commands": null}')
        with self.assertRaisesRegex(ValueError, "commands must be an array"):
            self.catalog.index()
        event = {"run_id": "r", "seq": 1, "type": "run.started"}
        report = {"runs": [{"run_id": "r"}], "commands": [{"events": [event, {**event, "type": "run.finished"}]}]}
        with self.assertRaisesRegex(ValueError, "conflicting events"):
            viewer.report_records(report, {"id": "fixture"})
        with self.assertRaisesRegex(ValueError, "unique run IDs"):
            viewer.report_records({"runs": [{"id": "r"}, {"id": "r"}]}, {"id": "fixture"})


class HTTPTests(unittest.TestCase):
    def setUp(self):
        CatalogTests.setUp(self)
        conn = CatalogTests.database(self)
        conn.close()
        self.server = viewer.ThreadingHTTPServer(("127.0.0.1", 0), viewer.Handler)
        self.server.catalog = self.catalog
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.base = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join()
        CatalogTests.tearDown(self)

    def test_backup_health_is_read_only_and_omits_recovery_key(self):
        with urlopen(self.base + "/api/backup") as response:
            self.assertEqual(json.load(response), {"enabled": False})
        self.server.backup_root = self.root
        (self.root / "config").mkdir()
        (self.root / "backup-state").mkdir()
        (self.root / "config/backup.json").write_text(json.dumps({
            "enabled": True, "destination": "/backups", "identity_file": "private-key-location"}))
        (self.root / "backup-state/status.json").write_text(json.dumps({
            "last_success": "2026-10-15T00:00:00Z", "last_error": "", "latest": {"bytes": 1200}}))
        with urlopen(self.base + "/api/backup") as response:
            data = json.load(response)
        self.assertTrue(data["enabled"])
        self.assertEqual(data["destination"], "/backups")
        self.assertEqual(data["latest"]["bytes"], 1200)
        self.assertNotIn("identity_file", data)
        with self.assertRaises(HTTPError) as error:
            urlopen(Request(self.base + "/api/backup", method="POST"))
        self.assertEqual(error.exception.code, 405)
        error.exception.close()

    def test_assets_api_headers_and_write_boundary(self):
        for asset in ("/", "/app.js", "/style.css"):
            with urlopen(self.base + asset) as response:
                self.assertEqual(response.status, 200)
                self.assertEqual(response.headers["X-Content-Type-Options"], "nosniff")
                self.assertIn("script-src 'self'", response.headers["Content-Security-Policy"])
                self.assertEqual(response.headers["Cache-Control"], "no-store")
        with urlopen(self.base + "/api/records") as response:
            self.assertEqual(json.load(response)["database_count"], 1)
        with urlopen(self.base + "/api/record?key=db:first") as response:
            self.assertIn("<img", json.load(response)["run"]["input"])
        for path, method, headers, expected in [
            ("/api/record?key=db:missing", "GET", {}, 404),
            ("/api/record?key=x&key=y", "GET", {}, 400),
            ("/../server.py", "GET", {}, 404),
            ("/api/records", "GET", {"Host": "remote.example"}, 403),
            ("/api/records", "POST", {}, 405),
        ]:
            with self.assertRaises(HTTPError) as error:
                urlopen(Request(self.base + path, method=method, headers=headers))
            self.assertEqual(error.exception.code, expected)
            error.exception.close()
        (self.reports / "broken.json").write_text("not json")
        with self.assertRaises(HTTPError) as error:
            urlopen(self.base + "/api/records")
        self.assertEqual(error.exception.code, 500)
        error.exception.close()


if __name__ == "__main__":
    unittest.main()
