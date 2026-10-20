"""Exercise live event projection, process cancellation and owner HTTP boundaries."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from chat_runtime import ChatRuntime, ChatConflict, configured_launch, default_config
from server import Handler, Catalog, ThreadingHTTPServer

HOST = r'''
import json, os, signal, subprocess, sys, time
message = sys.argv[sys.argv.index('-m') + 1]
def emit(kind, **value):
 print(json.dumps(dict(type=kind,run_id='run-test',session_id='session-test',**value)),flush=True)
if message == 'fail': sys.exit(3)
child = None
def stop(sig, frame):
 if child:
  child.terminate(); child.wait()
 emit('run.finished',status='canceled'); sys.exit(1)
signal.signal(signal.SIGTERM, stop)
emit('run.started')
emit('phase.started',phase='execute',status='running')
emit('text.delta',delta='First ')
if message == 'slow':
 emit('tool.activity',phase='execute',data={'tool':{'id':'waiting-tool','name':'Bash','stage':'call','input':{'command':'sleep 60'}}})
 child = subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'])
 open(os.path.join(sys.argv[sys.argv.index('--home')+1],'child.pid'),'w').write(str(child.pid))
 time.sleep(60)
else: time.sleep(.3)
emit('text.delta',delta='answer')
emit('text.delta',agent='memory-extractor',delta='not the answer')
emit('tool.activity',data={'tool':{'name':'Read','stage':'result'}})
emit('run.finished',status='completed')
'''


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.script = self.root / "host.py"
        self.script.write_text(HOST)
        self.runtime = ChatRuntime(self.root / "config.json", self.launch)

    def launch(self, config):
        return [sys.executable,"-u",str(self.script),"--home",str(self.root)],str(self.root),os.environ.copy(),"destination"

    def tearDown(self):
        self.runtime.close()
        self.temp.cleanup()

    def wait_for(self, predicate):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            snapshot = self.runtime.snapshot()
            if predicate(snapshot):
                return snapshot
            time.sleep(.02)
        self.fail("timed out waiting for observed process state")

    def test_streaming_is_observed_before_completion_and_deduplicated(self):
        turn = self.runtime.start("one", "hello")
        midway = self.wait_for(lambda s: s["turns"][0]["text"] == "First ")
        self.assertGreaterEqual(midway["turns"][0]["elapsed_ms"], 0)
        self.assertNotIn("started_at", midway["turns"][0])
        self.assertEqual(midway["active_id"], turn)
        self.assertEqual(self.runtime.start("one", "hello"), turn)
        with self.assertRaises(ChatConflict):
            self.runtime.start("two", "duplicate")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["text"], "First answer")
        self.assertEqual(done["turns"][0]["status"], "completed")
        self.assertGreater(done["turns"][0]["elapsed_ms"], 0)
        self.assertEqual(self.runtime.session, "session-test")
        self.assertEqual(self.runtime.start("one", "hello"), turn)
        self.assertEqual(len(self.runtime.snapshot()["turns"]), 1)

    def test_native_final_duration_survives_phase_filter(self):
        turn = {"id":"native", "text":"", "output":[]}
        self.runtime._event(turn, {"type":"run.finished", "phase":"memory", "status":"completed", "elapsed_ms":87234})
        self.assertEqual(turn["elapsed_ms"], 87234)
        self.assertEqual(turn["output"], [])

    def test_stop_signals_host_and_waits_for_child_exit_without_replay(self):
        turn = self.runtime.start("stop", "slow")
        self.wait_for(lambda s: (self.root / "child.pid").exists())
        pid = int((self.root / "child.pid").read_text())
        self.runtime.stop(turn)
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["status"], "canceled")
        self.assertEqual(done["turns"][0]["text"], "First ")
        calls = [row for row in done["turns"][0]["output"] if row["kind"] == "tool"]
        self.assertEqual(calls[0]["content"], "sleep 60")
        self.assertEqual(calls[0]["status"], "canceled")
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)
        with self.assertRaises(ChatConflict):
            self.runtime.start("next", "hello")
        self.runtime.reset()
        self.runtime.start("next", "hello")
        self.wait_for(lambda s: s["active_id"] is None)

    def test_failed_launch_is_never_reported_as_success(self):
        self.runtime.start("fail", "fail")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["status"], "failed")
        self.assertEqual(done["turns"][0]["exit_code"], 3)
        self.assertTrue(done["turns"][0]["error"])

    def test_prelaunch_stop_cannot_race_into_execution(self):
        ready = threading.Event()
        original = self.launch
        def delayed(config):
            ready.wait(3)
            return original(config)
        self.runtime.launcher = delayed
        turn = self.runtime.start("queued", "slow")
        self.runtime.stop(turn)
        ready.set()
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["status"], "canceled")
        self.assertFalse((self.root / "child.pid").exists())

    def test_changed_destination_requires_explicit_new_conversation(self):
        self.runtime.start("first", "hello")
        self.wait_for(lambda s: s["active_id"] is None)
        self.runtime.launcher = lambda config: (*self.launch(config)[:3],"changed")
        self.runtime.start("second", "hello")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][-1]["status"], "failed")
        self.assertIn("配置已变化",done["turns"][-1]["error"])

    def test_default_never_selects_an_unrelated_host(self):
        self.assertIsNone(default_config(self.root))

    def test_native_stream_keeps_text_calls_results_and_status_in_order(self):
        turn = {"id":"native", "text":"", "output":[]}
        def emit(kind, **value):
            self.runtime._event(turn, {"type":kind, "phase":"execute", **value})
        def partial(kind, **value):
            emit("native.evidence", data={"raw":{"type":"stream_event", "event":{"type":kind, **value}}})
        emit("agent.started", data={"raw":{"type":"system", "subtype":"init", "model":"claude-fixture", "apiKey":"SECRET_SENTINEL"}})
        emit("text.delta", delta="Before call.")
        emit("native.evidence", data={"raw":{"type":"assistant", "message":{"content":[{"type":"text","text":"Before call."}]}}})
        partial("content_block_start", index=1, content_block={"type":"tool_use", "id":"bash-1", "name":"Bash"})
        partial("content_block_delta", index=1, delta={"type":"input_json_delta", "partial_json":'{"command":"printf'})
        self.assertEqual(turn["output"][-1]["content"], '{"command":"printf')
        emit("tool.activity", data={"tool":{"id":"bash-1", "name":"Bash", "stage":"call", "input":{"command":"printf '<script>literal</script>'", "description":"Print fixture"}}})
        emit("native.evidence", data={"raw":{"type":"tool_progress","tool_use_id":"bash-1","tool_name":"Bash","elapsed_time_seconds":2}})
        emit("text.delta", delta="During tool.")
        emit("tool.activity", data={"tool":{"id":"bash-1", "name":"Bash", "stage":"result", "result":[{"type":"text","text":"<script>literal</script>"}], "is_error":False}})
        emit("text.delta", delta="After tool.")
        emit("agent.result", data={"raw":{"type":"result","subtype":"success","result":"Before call.During tool.After tool."}})
        rows = turn["output"]
        self.assertEqual([row["kind"] for row in rows], ["status","text","tool","text","tool","text","status"])
        self.assertEqual(rows[2]["content"], "printf '<script>literal</script>'")
        self.assertIn("Print fixture",rows[2]["parameters"])
        self.assertEqual(rows[2]["status"], "completed")
        self.assertIn('"elapsed_time_seconds": 2',rows[2]["progress"])
        self.assertEqual(rows[4]["content"], "<script>literal</script>")
        self.assertEqual(turn["text"], "Before call.During tool.After tool.")
        self.assertNotIn("SECRET_SENTINEL",json.dumps(rows))

    def test_host_phases_extractor_and_nontext_native_deltas_do_not_become_chat_output(self):
        turn = {"id":"native", "text":"", "output":[]}
        events = [{"type":"phase.started","phase":"recall","message":"Host recall"},
                  {"type":"text.delta","phase":"extract","agent":"memory-extractor","delta":"Extractor output"},
                  {"type":"native.evidence","phase":"execute","data":{"raw":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Not answer text"}}}}}]
        for event in events:
            self.runtime._event(turn,event)
        self.assertEqual(turn["output"],[])
        self.assertNotIn("steps",turn)

    def test_native_tool_errors_keep_the_original_result(self):
        turn = {"id":"native", "text":"", "output":[]}
        self.runtime._event(turn,{"type":"tool.activity","phase":"execute","data":{"tool":{"id":"read-1","name":"Read","stage":"call","input":{"file_path":"missing.txt"}}}})
        self.runtime._event(turn,{"type":"tool.activity","phase":"execute","data":{"tool":{"id":"read-1","name":"Read","stage":"result","is_error":True,"result":"File does not exist"}}})
        self.assertEqual(turn["output"][0]["status"],"failed")
        self.assertEqual(turn["output"][1]["content"],"File does not exist")

    def test_full_native_messages_backfill_missing_deltas_once(self):
        turn = {"id":"native", "text":"", "output":[]}
        event = {"type":"native.evidence","phase":"execute","data":{"raw":{"type":"assistant","uuid":"message-1","message":{"content":[{"type":"text","text":"Original answer"}]}}}}
        self.runtime._event(turn,event)
        self.runtime._event(turn,{"type":"text.delta","phase":"execute","delta":" next"})
        self.runtime._event(turn,event)
        self.assertEqual(turn["text"],"Original answer next")

    def test_private_config_validator_reports_no_credentials(self):
        config = self.root / "config.json"
        config.write_text(json.dumps({"binary":"SECRET_SENTINEL"}))
        config.chmod(0o600)
        with self.assertRaises(ValueError) as caught:
            configured_launch(config)
        self.assertNotIn("SECRET_SENTINEL", str(caught.exception))

    def test_http_requires_same_origin_token_and_bounded_schema(self):
        server = ThreadingHTTPServer(("127.0.0.1",0),Handler)
        server.chat_runtime = self.runtime
        server.catalog = Catalog(self.root,None)
        server.mutation_token = "owner-token"
        thread = threading.Thread(target=server.serve_forever,daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{server.server_port}"
        try:
            with urlopen(base + "/api/chat") as response:
                self.assertEqual(json.load(response)["mutation_token"],"owner-token")
            payload = json.dumps({"message":"hello","request_id":"http"}).encode()
            for headers in ({}, {"Origin":"https://foreign.example","X-Host-Action":"owner-token"}):
                with self.assertRaises(HTTPError) as error:
                    urlopen(Request(base+"/api/chat",data=payload,headers={"Content-Type":"application/json",**headers}))
                self.assertEqual(error.exception.code,403)
            headers = {"Content-Type":"application/json","Origin":base,"X-Host-Action":"owner-token"}
            with urlopen(Request(base+"/api/chat",data=payload,headers=headers)) as response:
                self.assertEqual(response.status,202)
            self.wait_for(lambda s: s["active_id"] is None)
            with urlopen(base+"/api/chat") as response:
                turn = json.load(response)["turns"][0]
                self.assertEqual(turn["record_key"],"db:run-test")
        finally:
            server.shutdown(); server.server_close(); thread.join()


if __name__ == "__main__":
    unittest.main()
