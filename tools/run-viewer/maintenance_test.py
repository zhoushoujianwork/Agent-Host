import sqlite3
import json
import threading
import unittest
from types import SimpleNamespace
from http.server import ThreadingHTTPServer
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from maintenance import MaintenanceGate


class GateTests(unittest.TestCase):
    def chat(self, active=None, children=None):
        runtime = SimpleNamespace(lock=threading.RLock(), active=active, children=children or {})
        return SimpleNamespace(lock=threading.RLock(), contexts={"task": runtime})

    def source(self, status="completed", finished=True, snapshot=False):
        class Connection(sqlite3.Connection):
            pass
        def connection():
            conn = sqlite3.connect(":memory:", factory=Connection)
            conn.snapshot = snapshot
            conn.executescript("CREATE TABLE runs(id TEXT,payload TEXT); CREATE TABLE events(run_id TEXT,payload TEXT);")
            conn.execute("INSERT INTO runs VALUES('test',?)", (json.dumps({"execution_status": status}),))
            if finished:
                conn.execute("INSERT INTO events VALUES('test',?)", ('{"type":"run.finished"}',))
            return conn
        return SimpleNamespace(connection=connection)

    def test_terminal_result_with_final_receipt_can_quiesce(self):
        gate = MaintenanceGate()
        self.assertEqual(gate.begin(self.source(), self.chat())["status"], "quiesced")
        self.assertTrue(gate.quiesced)
        gate.resume()
        self.assertFalse(gate.quiesced)

    def test_running_unknown_closeout_and_stale_sources_block(self):
        for source in [self.source(status="running"), self.source(status="unknown"),
                       self.source(finished=False), self.source(snapshot=True)]:
            gate = MaintenanceGate()
            with self.assertRaises(ValueError):
                gate.begin(source, self.chat())
            self.assertFalse(gate.quiesced)

    def test_owned_children_block_even_after_answer_completion(self):
        gate = MaintenanceGate()
        with self.assertRaises(ValueError):
            gate.begin(self.source(), self.chat(children={"run": SimpleNamespace(poll=lambda: None)}))
        self.assertFalse(gate.quiesced)

    def test_connect_can_recover_missing_source_but_never_active_owned_task(self):
        missing = SimpleNamespace(connection=lambda: (_ for _ in ()).throw(FileNotFoundError()))
        self.assertEqual(MaintenanceGate().begin(missing, self.chat(), "connect")["status"], "quiesced")
        with self.assertRaises(ValueError):
            MaintenanceGate().begin(missing, self.chat(active="run"), "connect")
        with self.assertRaises(ValueError):
            MaintenanceGate().begin(missing, self.chat(), "update")

    def test_http_gate_requires_owner_token_blocks_submissions_and_can_resume(self):
        from server import Handler
        from chat_runtime import AgentChatRuntime
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.mutation_token = "test-owner"
        server.maintenance_gate = MaintenanceGate()
        server.chat_runtime = AgentChatRuntime(None)
        server.catalog = SimpleNamespace()
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base = "http://127.0.0.1:" + str(server.server_port)
        def post(path, value, token="test-owner"):
            request = Request(base + path, data=json.dumps(value).encode(),
                              headers={"Content-Type": "application/json", "Origin": base, "X-Host-Action": token})
            try:
                with urlopen(request) as response:
                    return response.status, json.load(response)
            except HTTPError as error:
                return error.code, json.load(error)
        try:
            self.assertEqual(post("/api/app/quiesce", {"purpose": "connect"}, token="wrong")[0], 403)
            self.assertFalse(server.maintenance_gate.quiesced)
            self.assertEqual(post("/api/app/quiesce", {"purpose": "connect"})[0], 200)
            status, result = post("/api/chat", {"request_id": "new", "message": "must not start"})
            self.assertEqual(status, 409)
            self.assertIn("restarting", result["error"])
            self.assertEqual(post("/api/app/resume", {})[0], 200)
            self.assertFalse(server.maintenance_gate.quiesced)
            self.assertEqual(post("/api/app/quiesce", {"purpose": {}})[0], 409)
        finally:
            server.shutdown(); thread.join(); server.server_close(); server.chat_runtime.close()


if __name__ == "__main__":
    unittest.main()
