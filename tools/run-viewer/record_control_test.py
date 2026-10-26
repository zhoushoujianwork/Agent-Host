"""Record authorization and HTTP control checks using isolated fixture state only."""

import http.client
import json
from pathlib import Path
import sqlite3
import tempfile
import io
from email.message import Message
from types import SimpleNamespace
import unittest
from unittest.mock import patch, Mock

import record_control
import run_control
from server import MultiCatalog, Handler


class RecordControlTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="agh-record-control-")
        self.root = Path(self.temp.name).resolve()
        for name in ("cli", "feishu"):
            home = self.root / name
            home.mkdir(mode=0o700)
            with sqlite3.connect(home / "state.db") as conn:
                conn.executescript("CREATE TABLE runs(id TEXT,scope TEXT,created_at TEXT,payload TEXT);"
                    "CREATE TABLE events(run_id TEXT,seq INTEGER,payload TEXT);"
                    "CREATE TABLE memories(id TEXT,scope TEXT,payload TEXT);"
                    "CREATE TABLE candidates(id TEXT,run_id TEXT,payload TEXT);PRAGMA user_version=2;")
            (home / "state.db").chmod(0o600)
            self.update(name)
        self.catalog = MultiCatalog([f"{name}={self.root / name}" for name in ("cli", "feishu")])

    def tearDown(self):
        self.temp.cleanup()

    def update(self, name, status="running", finished=False, **fields):
        run = {"id":"same", "scope":"/fixture", "created_at":"2026-10-26T00:00:00Z",
               "execution_status":status, "agent_id":"default", "audience_id":"owner", **fields}
        with sqlite3.connect(self.root / name / "state.db") as conn:
            conn.execute("DELETE FROM runs")
            conn.execute("INSERT INTO runs VALUES(?,?,?,?)", (run["id"],run["scope"],run["created_at"],json.dumps(run)))
            conn.execute("DELETE FROM events")
            if finished:
                conn.execute("INSERT INTO events VALUES('same',1,?)", (json.dumps({"type":"run.finished"}),))

    def capability(self, name="cli", port=12345):
        path = self.root / name / ".run-control-same.json"
        path.write_text(json.dumps({"version":1,"run_id":"same","port":port,"token":"a"*64}))
        path.chmod(0o600)
        return path

    def test_exact_multi_source_resolution_and_scope_authorization_before_control(self):
        self.capability("cli")
        self.capability("feishu")
        with patch.object(run_control, "stop") as stop:
            result = record_control.stop(self.catalog, "feishu:db:same")
            stop.assert_called_once_with(self.root / "feishu", "same")
            self.assertEqual(result, {"key":"feishu:db:same", "run_id":"same", "status":"stopping"})
        self.update("cli", audience_id="another")
        with patch.object(run_control, "available") as available, patch.object(run_control, "stop") as stop:
            with self.assertRaises(KeyError): record_control.stop(self.catalog, "cli:db:same")
            available.assert_not_called()
            stop.assert_not_called()
        with self.assertRaises(KeyError): record_control.record(self.catalog, "unknown:db:same")

    def test_single_catalog_metadata_and_memory_finalization(self):
        source = self.catalog.catalogs["cli"]
        control = record_control.record(source, "db:same")["control"]
        self.assertTrue(control["active"])
        self.assertFalse(control["can_stop"])
        self.assertIn("original entry", control["reason"])
        self.update("cli", status="completed")
        self.assertFalse(record_control.record(source, "db:same")["control"]["active"])
        self.capability()
        self.assertEqual(record_control.record(source, "db:same")["control"], {"active":True,"can_stop":True,"reason":""})
        self.update("cli", status="completed", finished=True)
        self.assertFalse(record_control.record(source, "db:same")["control"]["active"])

    def test_finished_archived_and_missing_control_are_rejected(self):
        with patch.object(run_control, "stop") as stop:
            with self.assertRaises(record_control.RecordControlConflict): record_control.stop(self.catalog, "cli:db:same")
            self.capability()
            self.update("cli", finished=True)
            with self.assertRaises(record_control.RecordControlConflict): record_control.stop(self.catalog, "cli:db:same")
            source = self.catalog.catalogs["cli"]
            archive = {"snapshot":True,"run":{"id":"same","execution_status":"running"},"events":[]}
            with patch.object(source, "record", return_value=archive):
                with self.assertRaises(record_control.RecordControlConflict): record_control.stop(source, "archive:same")
            stop.assert_not_called()

    def test_stop_freshly_rechecks_finished_record(self):
        self.capability()
        self.assertTrue(record_control.record(self.catalog, "cli:db:same")["control"]["can_stop"])
        self.update("cli", finished=True)
        with patch.object(run_control, "stop") as stop:
            with self.assertRaises(record_control.RecordControlConflict): record_control.stop(self.catalog, "cli:db:same")
            stop.assert_not_called()

    def test_fixture_endpoint_receipt_and_safe_failures(self):
        self.capability(port=12345)
        response = Mock(status=200)
        response.read.return_value = b'{"run_id":"same","status":"stopping"}'
        connection = Mock()
        connection.getresponse.return_value = response
        with patch.object(run_control.http.client, "HTTPConnection", return_value=connection) as endpoint:
            self.assertEqual(record_control.stop(self.catalog, "cli:db:same")["status"], "stopping")
            endpoint.assert_called_once_with("127.0.0.1", 12345, timeout=3)
            connection.request.assert_called_once_with("POST", "/stop", headers={"X-Run-Token":"a"*64})
            connection.close.assert_called_once()
            connection.request.side_effect = http.client.HTTPException("private transport details")
            with self.assertRaisesRegex(record_control.RecordControlConflict, "could not be confirmed"):
                record_control.stop(self.catalog, "cli:db:same")
            connection.request.side_effect = None
            for body, status in ((b'{"run_id":"different","status":"stopping"}',200),
                                 (b'{"run_id":"same","status":"completed"}',200),
                                 (b'{}',403),(b'invalid',200),(b'x'*4097,200)):
                response.read.return_value, response.status = body, status
                with self.assertRaisesRegex(record_control.RecordControlConflict, "could not be confirmed"):
                    record_control.stop(self.catalog, "cli:db:same")
        with patch.object(run_control, "stop", side_effect=OSError("secret capability path")):
            with self.assertRaises(record_control.RecordControlConflict) as error:
                record_control.stop(self.catalog, "cli:db:same")
            self.assertNotIn("secret", str(error.exception))

    def test_http_guards_strict_payload_metadata_and_direct_stop_without_chat(self):
        self.capability()
        host = "127.0.0.1:8765"
        def request(method="POST", payload=None, headers=None, path="/api/record/stop", raw=None, token="owner-token"):
            body = json.dumps({"key":"cli:db:same"} if payload is None else payload).encode()
            if raw is not None:
                body = raw
            defaults = {"Host":host, "Content-Type":"application/json", "Content-Length":str(len(body)),
                        "Origin":"http://"+host, "X-Host-Action":"owner-token"}
            defaults.update(headers or {})
            handler = object.__new__(Handler)
            handler.server = SimpleNamespace(catalog=self.catalog, mutation_token=token, server_port=8765)
            handler.headers = Message()
            for name, value in defaults.items():
                handler.headers[name] = value
            handler.path = path
            handler.rfile = io.BytesIO(body)
            handler.connection = Mock()
            handler.send = Mock()
            (handler.do_POST if method == "POST" else handler.do_GET)()
            return handler.send.call_args.args
        with patch.object(run_control, "stop") as stop:
            for headers in ({"Origin":"http://evil"},{"X-Host-Action":"wrong"},{"Host":"evil"}):
                self.assertEqual(request(headers=headers)[0],403)
            for payload in ({},[],"key",{"key":1},{"key":""},{"key":"x"*257},{"key":"cli:db:same","run_id":"same"}):
                self.assertEqual(request(payload=payload)[0],400)
            self.assertEqual(request(headers={"Content-Type":"text/plain"})[0],400)
            self.assertEqual(request(headers={"Transfer-Encoding":"chunked"})[0],400)
            for length in ("8193", "0", "-1", "invalid"):
                self.assertEqual(request(headers={"Content-Length":length})[0],400)
            self.assertEqual(request(raw=b'{invalid')[0],400)
            self.assertEqual(request(token=None)[0],405)
            self.assertEqual(request(payload={"key":"unknown:db:same"})[0],404)
            stop.assert_not_called()
            status, value = request(method="GET",path="/api/record?key=cli:db:same")
            self.assertEqual(status,200)
            self.assertTrue(value["control"]["can_stop"])
            self.assertEqual(value["mutation_token"],"owner-token")
            self.assertNotIn("a"*64,json.dumps(value))
            self.assertNotIn(".run-control-",json.dumps(value))
            self.assertNotIn(str(self.root),json.dumps(value["control"]))
            self.assertEqual(request()[1],{"key":"cli:db:same","run_id":"same","status":"stopping"})
            stop.assert_called_once_with(self.root / "cli","same")
        with patch.object(run_control, "stop", side_effect=OSError("private")):
            status, value = request()
            self.assertEqual(status,409)
            self.assertNotIn("private",value["error"])
        self.update("cli",finished=True)
        self.assertEqual(request()[0],409)


if __name__ == "__main__":
    unittest.main()
