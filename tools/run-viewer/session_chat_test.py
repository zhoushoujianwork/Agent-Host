"""Exercise existing session selection and the real private-socket client."""
import json
import os
from pathlib import Path
import socketserver
import sqlite3
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from chat_runtime import AgentChatRuntime, ChatConflict
from server import Catalog, Handler, ThreadingHTTPServer
import run_control


class SessionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="agh-session-")
        self.home = Path(self.temp.name).resolve()
        self.source = Catalog(self.home, None, all_chats=True)
        self.chat = AgentChatRuntime(None)
        conn = sqlite3.connect(self.home / "state.db")
        conn.executescript("CREATE TABLE runs(id TEXT,session_id TEXT,scope TEXT,created_at TEXT,payload TEXT);"
            "CREATE TABLE sessions(id TEXT,payload TEXT);CREATE TABLE events(run_id TEXT,seq INTEGER,payload TEXT);"
            "CREATE TABLE memories(id TEXT,scope TEXT,payload TEXT);CREATE TABLE candidates(id TEXT,run_id TEXT,payload TEXT);PRAGMA user_version=2;")
        for index in range(2):
            session = {"id": f"s{index}", "agent_id": "daily", "audience_id": f"group-{index}", "room": {"room_id": f"room-{index}"}}
            run = {"id": f"r{index}", "session_id": session["id"], "agent_id": "daily", "audience_id": session["audience_id"],
                   "scope": "/fixture", "input": f"original-{index}", "output": "original reply", "model": "fixture",
                   "execution_status": "completed", "memory_status": "unchanged", "created_at": f"2026-10-21T00:00:0{index}Z", "room": session["room"]}
            conn.execute("INSERT INTO sessions VALUES(?,?)", (session["id"], json.dumps(session)))
            conn.execute("INSERT INTO runs VALUES(?,?,?,?,?)", (run["id"], session["id"], run["scope"], run["created_at"], json.dumps(run)))
        conn.commit(); conn.close()

    def tearDown(self):
        self.chat.close()
        self.temp.cleanup()

    def test_selection_history_unavailable_service_and_stale_send(self):
        self.chat.attach(self.source, "db:r0")
        first = self.chat.snapshot()
        self.assertEqual(first["turns"][0]["input"], "original-0")
        self.assertEqual(first["selected_session"], "s0")
        self.assertTrue(first["needs_new"])
        self.chat.attach(self.source, "db:r1")
        with self.assertRaises(ChatConflict):
            self.chat.start("stale", "wrong session", context_id=first["context_id"])
        self.assertEqual(self.chat.snapshot()["turns"][0]["input"], "original-1")
        self.chat.attach(self.source, "db:r0")
        self.assertEqual(self.chat.snapshot()["context_id"], first["context_id"])

    def test_http_selection_requires_owner_token_and_preserves_selected_context(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.catalog, server.chat_runtime, server.mutation_token = self.source, self.chat, "owner-token"
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        base = f"http://127.0.0.1:{server.server_port}"
        def select(key, token="owner-token"):
            request = Request(base+"/api/chat/session", json.dumps({"key":key}).encode(),
                              {"Origin":base,"Content-Type":"application/json","X-Host-Action":token})
            return urlopen(request)
        try:
            with self.assertRaises(HTTPError) as error: select("db:r0", "wrong-token")
            self.assertEqual(error.exception.code, 403)
            with select("db:r0") as response: self.assertEqual(response.status, 200)
            with urlopen(base+"/api/chat") as response: snapshot = json.load(response)
            self.assertEqual(snapshot["turns"][0]["record_key"], "db:r0")
            self.assertEqual(snapshot["selected_session"], "s0")
            with self.assertRaises(HTTPError) as error: select("db:missing")
            self.assertEqual(error.exception.code, 404)
            self.assertEqual(self.chat.snapshot()["selected_session"], "s0")
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_private_service_stream_and_second_turn_continue_exact_context(self):
        received = []
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                value = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                received.append(value)
                self.send_response(200); self.end_headers()
                index = len(received)
                for event in [{"type":"run.started", "run_id":f"web-{index}", "session_id":f"web-s{index}"},
                              {"type":"text.delta", "phase":"execute", "delta":"continued reply"},
                              {"type":"run.finished", "phase":"receipt", "status":"completed", "data":{"execution_status":"completed","memory_status":"unchanged"}}]:
                    self.wfile.write((json.dumps(event)+"\n").encode()); self.wfile.flush()
            def log_message(self, *args): pass
        server = socketserver.UnixStreamServer(str(self.home / "memory-control.sock"), Handler)
        os.chmod(self.home / "memory-control.sock", 0o600)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            self.chat.attach(self.source, "db:r0")
            context = self.chat.snapshot()["context_id"]
            for index in range(2):
                self.chat.start(f"request-{index}", "continue", context_id=context)
                deadline = time.monotonic()+10
                while self.chat.snapshot()["active_id"] and time.monotonic()<deadline: time.sleep(.02)
                result = self.chat.snapshot()
                self.assertIsNone(result["active_id"])
                self.assertEqual(result["turns"][-1]["status"], "completed", result)
                self.assertEqual(result["turns"][-1]["text"], "continued reply")
            self.assertEqual([r["run_id"] for r in received], ["r0", "web-1"])
            self.assertEqual(self.chat.snapshot()["selected_session"], "web-s2")
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_attached_live_output_and_scoped_stop_wait_for_durable_outcome(self):
        received = []
        class Control(BaseHTTPRequestHandler):
            def do_POST(self):
                received.append((self.path, self.headers.get("X-Run-Token")))
                self.send_response(200); self.end_headers()
                self.wfile.write(json.dumps({"run_id":"r0", "status":"stopping"}).encode())
            def log_message(self, *args): pass
        server = ThreadingHTTPServer(("127.0.0.1", 0), Control)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        capability = self.home / ".run-control-r0.json"
        capability.write_text(json.dumps({"version":1,"run_id":"r0","port":server.server_port,"token":"a"*64}))
        capability.chmod(0o600)
        def update(status, events):
            with sqlite3.connect(self.home / "state.db") as conn:
                run = json.loads(conn.execute("SELECT payload FROM runs WHERE id='r0'").fetchone()[0])
                run["execution_status"], run["output"] = status, ""
                conn.execute("UPDATE runs SET payload=? WHERE id='r0'", (json.dumps(run),))
                for index, event in events:
                    conn.execute("INSERT INTO events VALUES('r0',?,?)", (index,json.dumps(event)))
        try:
            update("running", [(1,{"type":"text.delta","phase":"execute","delta":"first"})])
            self.chat.attach(self.source, "db:r0")
            snapshot = self.chat.snapshot()
            context = snapshot["context_id"]
            self.assertEqual(snapshot["active_ids"], ["history-r0"])
            self.assertTrue(snapshot["turns"][0]["can_stop"])
            self.assertEqual(snapshot["turns"][0]["text"], "first")
            update("running", [(2,{"type":"text.delta","phase":"execute","delta":" second"})])
            self.assertEqual(self.chat.snapshot()["turns"][0]["text"], "first second")
            self.assertEqual(self.chat.snapshot()["turns"][0]["text"], "first second")
            with self.assertRaises(ChatConflict): self.chat.stop("history-r0", context_id="another-context")
            with self.assertRaises(KeyError): self.chat.stop("history-r1", context_id=context)
            self.assertEqual(received, [])
            self.chat.stop("history-r0", context_id=context)
            self.assertEqual(received, [("/stop", "a"*64)])
            self.assertEqual(self.chat.snapshot()["turns"][0]["status"], "stopping")
            self.assertEqual(self.chat.snapshot()["turns"][0]["execution_status"], "running")
            update("canceled", [(3,{"type":"run.finished","phase":"receipt","status":"canceled","data":{"execution_status":"canceled","memory_status":"skipped"}})])
            snapshot = self.chat.snapshot()
            self.assertIsNone(snapshot["active_id"])
            self.assertEqual(snapshot["turns"][0]["status"], "canceled")
            self.assertFalse(snapshot["turns"][0]["can_stop"])
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_stop_capability_rejects_wrong_run_public_files_and_symlinks(self):
        path = self.home / ".run-control-r0.json"
        value = {"version":1,"run_id":"r0","port":12345,"token":"a"*64}
        path.write_text(json.dumps(value)); path.chmod(0o600)
        self.assertTrue(run_control.available(self.home, "r0"))
        path.chmod(0o644)
        self.assertFalse(run_control.available(self.home, "r0"))
        path.chmod(0o600)
        value["run_id"] = "r1"
        path.write_text(json.dumps(value))
        self.assertFalse(run_control.available(self.home, "r0"))
        path.unlink(); path.symlink_to(self.home / "state.db")
        self.assertFalse(run_control.available(self.home, "r0"))
        self.assertFalse(run_control.available(self.home, "../r0"))


if __name__ == "__main__": unittest.main()
