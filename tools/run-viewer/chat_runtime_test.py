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
        self.assertEqual(midway["active_id"], turn)
        self.assertEqual(self.runtime.start("one", "hello"), turn)
        with self.assertRaises(ChatConflict):
            self.runtime.start("two", "duplicate")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["text"], "First answer")
        self.assertEqual(done["turns"][0]["status"], "completed")
        self.assertEqual(self.runtime.session, "session-test")
        self.assertEqual(self.runtime.start("one", "hello"), turn)
        self.assertEqual(len(self.runtime.snapshot()["turns"]), 1)

    def test_stop_signals_host_and_waits_for_child_exit_without_replay(self):
        turn = self.runtime.start("stop", "slow")
        self.wait_for(lambda s: (self.root / "child.pid").exists())
        pid = int((self.root / "child.pid").read_text())
        self.runtime.stop(turn)
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["status"], "canceled")
        self.assertEqual(done["turns"][0]["text"], "First ")
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
