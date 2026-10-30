"""Exercise live event projection, process cancellation and owner HTTP boundaries."""
import json
import os
import sqlite3
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from chat_runtime import ChatRuntime, AgentChatRuntime, ChatConflict, configured_launch, default_config
from server import Handler, Catalog, ThreadingHTTPServer

HOST = r'''
import json, os, signal, subprocess, sys, time
message = sys.argv[sys.argv.index('-m') + 1]
def emit(kind, **value):
 print(json.dumps(dict(type=kind,run_id='run-test',session_id='session-test',**value)),flush=True)
if message == 'busy':
 print('another host owns this state home: resource temporarily unavailable',file=sys.stderr); sys.exit(1)
if message == 'session-busy':
 print('another CLI owns this conversation; independent conversations can run concurrently',file=sys.stderr); sys.exit(1)
if message == 'fail':
 print('private-token-must-not-leak',file=sys.stderr); sys.exit(3)
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
if message == 'memory-fail':
 emit('phase.finished',phase='execute',status='completed')
 emit('run.finished',phase='receipt',status='failed',data={'execution_status':'completed','error':'','memory_status':'failed','memory_error':'Jev provider custom returned HTTP 404 (Not Found)'})
 sys.exit(1)
emit('run.finished',status='completed')
'''


class RuntimeTests(unittest.TestCase):
    def test_successful_reply_with_failed_memory_retains_separate_durable_outcomes(self):
        room_file = self.root / "room.json"
        runtime = ChatRuntime(self.root / "config.json", self.launch, room_file)
        self.runtime.close()
        self.runtime = runtime
        runtime.start("memory-failure", "memory-fail")
        turn = self.wait_for(lambda s: s["active_id"] is None)["turns"][0]
        self.assertEqual(turn["status"], "failed")
        self.assertEqual(turn["exit_code"], 1)
        self.assertEqual(turn["execution_status"], "completed")
        self.assertEqual(turn["memory_status"], "failed")
        self.assertIn("HTTP 404", turn["memory_error"])
        self.assertEqual(turn["text"], "First answer")
        self.assertEqual(turn["error"], "")
        restored = ChatRuntime(self.root / "config.json", self.launch, room_file)
        self.addCleanup(restored.close)
        saved = restored.snapshot()["turns"][0]
        for field in ("execution_status", "memory_status", "memory_error", "execution_error"):
            self.assertEqual(saved[field], turn[field])
        self.assertFalse(restored.workers)

    def test_old_receipt_recovers_only_its_own_recorded_outcomes(self):
        self.runtime.state_home = self.root
        self.runtime.turns = [{"id": "old", "run_id": "recorded", "status": "failed"},
                              {"id": "missing", "run_id": "missing", "status": "failed"}]
        event = {"type": "run.finished", "data": {"execution_status": "completed", "memory_status": "failed", "memory_error": "Recorded 404"}}
        with sqlite3.connect(self.root / "state.db") as conn:
            conn.execute("CREATE TABLE events(run_id TEXT, seq INTEGER, payload TEXT)")
            conn.execute("INSERT INTO events VALUES(?,?,?)", ("recorded", 1, json.dumps(event)))
        turns = self.runtime.snapshot()["turns"]
        self.assertEqual(turns[0]["execution_status"], "completed")
        self.assertEqual(turns[0]["memory_error"], "Recorded 404")
        self.assertNotIn("execution_status", turns[1])

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
            self.runtime.start("one", "different content")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["text"], "First answer")
        self.assertEqual(done["turns"][0]["status"], "completed")
        self.assertGreater(done["turns"][0]["elapsed_ms"], 0)
        self.assertEqual(self.runtime.session, "session-test")
        self.assertEqual(self.runtime.start("one", "hello"), turn)
        self.assertEqual(len(self.runtime.snapshot()["turns"]), 1)

    def test_switching_agents_preserves_rooms_without_resuming_a_mutable_native_session(self):
        catalog=self.root/"agents.json"
        catalog.write_text(json.dumps({"agents":[{"id":"a","name":"Agent A"},{"id":"b","name":"Agent B"}]})); catalog.chmod(0o600)
        config=self.root/"chat.json"
        config.write_text(json.dumps({"args":["--agent","a","--agents-config",str(catalog),"--home",str(self.root),"--workspace",str(self.root)]})); config.chmod(0o600)
        launched=[]
        self.script.write_text(HOST.replace("'session-test'", "('session-'+sys.argv[sys.argv.index('--agent')+1])") + "\nopen(os.path.join(sys.argv[sys.argv.index('--home')+1],'resume-evidence.jsonl'),'a').write(json.dumps({'agent':sys.argv[sys.argv.index('--agent')+1],'resume':sys.argv[sys.argv.index('--resume')+1] if '--resume' in sys.argv else None})+'\\n')\n")
        def launch(path,agent):
            command,cwd,env,identity=self.launch(path)
            launched.append(agent)
            return [*command,"--agent",agent],cwd,env,agent
        self.runtime.close()
        self.runtime=AgentChatRuntime(config,launch)
        self.assertEqual(self.runtime.snapshot()["selected_agent"],"a")
        self.runtime.start("a1","hello","a")
        with self.assertRaises(ChatConflict):self.runtime.select("b")
        a=self.wait_for(lambda s:s["active_id"] is None)["turns"][0]
        self.runtime.select("b")
        self.assertEqual(self.runtime.snapshot()["turns"],[])
        with self.assertRaises(ChatConflict):self.runtime.start("stale","hello","a")
        self.runtime.start("b1","hello","b")
        self.wait_for(lambda s:s["active_id"] is None)
        b_context=self.runtime.current
        self.runtime.select("a")
        self.assertEqual(self.runtime.snapshot()["turns"][0]["id"],a["id"])
        self.assertEqual(self.runtime.current.session,"session-a")
        self.runtime.start("a2","hello","a")
        self.wait_for(lambda s:s["active_id"] is None)
        self.assertEqual(len(self.runtime.snapshot()["turns"]),2)
        self.assertEqual(launched,["a","b","a"])
        observed=[json.loads(line) for line in (self.root/"resume-evidence.jsonl").read_text().splitlines()]
        self.assertEqual(observed,[{"agent":"a","resume":None},{"agent":"b","resume":None},{"agent":"a","resume":None}])
        self.runtime.reset()
        self.runtime.select("b")
        self.assertIs(self.runtime.current,b_context)
        self.assertEqual(len(self.runtime.snapshot()["turns"]),1)
        with self.assertRaises(ValueError):self.runtime.select("channel-only-agent")

    def test_configured_launcher_selects_only_an_agent_in_the_explicit_catalog(self):
        catalog=self.root/"agents.json"
        catalog.write_text(json.dumps({"default_agent":"a","agents":[{"id":"a","name":"A","projects":[]},{"id":"b","name":"B","projects":[]}]}));catalog.chmod(0o600)
        environment=self.root/"env.json";environment.write_text("{}");environment.chmod(0o600)
        config=self.root/"destination.json"
        config.write_text(json.dumps({"binary":sys.executable,"cwd":str(self.root),"environment_file":str(environment),"args":["--home",str(self.root),"--agents-config",str(catalog),"--agent","a","--workspace",str(self.root),"--native-profile",str(self.root)]}));config.chmod(0o600)
        command_a,_,_,identity_a=configured_launch(config,"a")
        command_b,_,_,identity_b=configured_launch(config,"b")
        self.assertEqual(command_a[command_a.index("--agent")+1],"a")
        self.assertEqual(command_b[command_b.index("--agent")+1],"b")
        self.assertEqual(command_b[command_b.index("--home")+1],str(self.root))
        self.assertEqual(command_a[-1],"--feedback-other-users-only=true")
        self.assertEqual(command_b[-1],"--feedback-other-users-only=true")
        self.assertNotEqual(identity_a,identity_b)
        with self.assertRaises(ValueError):configured_launch(config,"unconfigured")

    def test_offload_launcher_uses_profile_without_legacy_agent_or_provider(self):
        profile=self.root/"offload.json"
        profile.write_text(json.dumps({"version":1,"command":sys.executable,"profile":"fixture"}));profile.chmod(0o600)
        config=self.root/"offload-web.json"
        value={"binary":sys.executable,"cwd":str(self.root),"args":["--home",str(self.root),"--workspace",str(self.root),"--agent","daily","--offload-config",str(profile)]}
        config.write_text(json.dumps(value));config.chmod(0o600)
        command,_,env,identity=configured_launch(config,"daily")
        self.assertNotIn("--renew-runtime",command)
        self.assertNotIn("--feedback-other-users-only=true",command)
        self.assertNotIn("ANTHROPIC_API_KEY",env)
        self.assertNotIn("JEV_API_KEY",env)
        profile.write_text(json.dumps({"version":1,"command":sys.executable,"profile":"changed"}))
        self.assertNotEqual(configured_launch(config,"daily")[3],identity)
        with self.assertRaises(ValueError):configured_launch(config,"other")
        value["args"] += ["--no-tools","true"]
        config.write_text(json.dumps(value))
        with self.assertRaises(ValueError):configured_launch(config)

    def test_native_final_duration_survives_phase_filter(self):
        turn = {"id":"native", "text":"", "output":[]}
        self.runtime._event(turn, {"type":"run.finished", "phase":"memory", "status":"completed", "elapsed_ms":87234})
        self.assertEqual(turn["elapsed_ms"], 87234)
        self.assertEqual(turn["output"], [])

    def test_acp_projects_real_tool_patches_and_plan_without_duplicate_calls(self):
        turn = {"id":"acp", "text":"", "output":[]}
        def event(kind, raw=None, tool=None, **fields):
            data = {"raw":raw or {}}
            if tool is not None:
                data["tool"] = tool
            self.runtime._event(turn, {"component":"harness.acp", "phase":"execute", "type":kind, "data":data, **fields})
        event("agent.started", model="observed-acp-model")
        event("tool.activity", tool={"id":"one", "name":"Bash", "stage":"call", "input":{}})
        event("tool.activity", tool={"id":"one", "name":"Bash", "stage":"update", "input":{"command":"printf acp"}})
        event("tool.activity", tool={"id":"one", "name":"Bash", "stage":"result", "result":[{"type":"content", "content":{"type":"text", "text":"acp"}}]})
        event("native.evidence", {"sessionUpdate":"plan", "entries":[{"content":"Check", "status":"in_progress"}]})
        event("native.evidence", {"sessionUpdate":"plan", "entries":[{"content":"Check", "status":"completed"}]})
        event("text.delta", delta="**Done**")
        calls = [row for row in turn["output"] if row.get("stage") == "call"]
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["content"], "printf acp")
        self.assertEqual(calls[0]["status"], "completed")
        self.assertEqual(turn["output"][2]["content"], "acp")
        plans = [row for row in turn["output"] if row.get("label") == "原生计划"]
        self.assertEqual(len(plans), 1)
        self.assertEqual(plans[0]["content"], "✓ Check")
        self.assertEqual(turn["text"], "**Done**")

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
        self.runtime.start("next", "hello")
        self.wait_for(lambda s: s["active_id"] is None)

    def test_failed_launch_is_never_reported_as_success(self):
        self.runtime.start("fail", "fail")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["status"], "failed")
        self.assertEqual(done["turns"][0]["exit_code"], 3)
        self.assertIn("退出码 3", done["turns"][0]["error"])
        self.assertNotIn("private-token", done["turns"][0]["error"])

    def test_same_room_overlap_and_stop_only_the_selected_message(self):
        first = self.runtime.start("first", "slow")
        self.wait_for(lambda s: any(r.get("tool_id") == "waiting-tool" for r in s["turns"][0]["output"]))
        second = self.runtime.start("second", "slow")
        snapshot = self.wait_for(lambda s: len(s["active_ids"]) == 2 and any(r.get("tool_id") == "waiting-tool" for r in s["turns"][1]["output"]))
        self.assertEqual(snapshot["turns"][0]["room_id"], snapshot["turns"][1]["room_id"])
        self.runtime.stop(first)
        snapshot = self.wait_for(lambda s: s["turns"][0]["status"] == "canceled")
        self.assertEqual(snapshot["active_ids"], [second])
        self.assertEqual(snapshot["turns"][1]["status"], "running")

    def test_room_capacity_is_bounded_and_all_children_are_joined_on_close(self):
        for i in range(4):
            self.runtime.start(str(i), "slow")
        with self.assertRaises(ChatConflict):
            self.runtime.start("overflow", "hello")
        self.runtime.close()
        self.assertEqual(self.runtime.snapshot()["active_ids"], [])
        self.assertFalse(self.runtime.children)

    def test_room_survives_viewer_restart_and_reset_persists_new_room(self):
        room_file = self.root / "rooms/default.json"
        first = ChatRuntime(self.root / "config.json", self.launch, room_file)
        room = first.room_id
        first.close()
        second = ChatRuntime(self.root / "config.json", self.launch, room_file)
        self.assertEqual(second.room_id, room)
        second.reset()
        self.assertNotEqual(second.room_id, room)
        third = ChatRuntime(self.root / "config.json", self.launch, room_file)
        self.assertEqual(third.room_id, second.room_id)
        second.close()
        third.close()

    def test_accepted_receipt_is_durable_and_restart_never_replays_it(self):
        self.runtime.close()
        room_file = self.root / "rooms/default.json"
        self.runtime = ChatRuntime(self.root / "config.json", self.launch, room_file)
        turn = self.runtime.start("receipt", "slow")
        self.wait_for(lambda s: bool(s["turns"][0]["run_id"]))
        restored = ChatRuntime(self.root / "config.json", self.launch, room_file)
        try:
            snapshot = restored.snapshot()
            self.assertEqual(snapshot["turns"][0]["id"], turn)
            self.assertEqual(snapshot["turns"][0]["status"], "unknown")
            self.assertEqual(restored.start("receipt", "slow"), turn)
            self.assertFalse(restored.workers)
        finally:
            restored.close()

    def test_busy_state_is_actionable_and_allows_manual_send(self):
        self.runtime.start("busy", "busy")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["error_code"], "state_busy")
        self.assertIn("尚未开始", done["turns"][0]["error"])
        self.assertFalse(done["needs_new"])
        self.assertEqual(done["turns"][0]["run_id"], "")
        self.runtime.start("manual", "hello")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][-1]["status"], "completed")

    def test_busy_session_does_not_block_independent_conversations(self):
        self.runtime.start("same-session", "session-busy")
        done = self.wait_for(lambda s: s["active_id"] is None)
        self.assertEqual(done["turns"][0]["error_code"], "session_busy")
        self.assertIn("独立对话可并发", done["turns"][0]["error"])
        self.assertFalse(done["needs_new"])

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

    def test_execution_ready_shows_nonstreamed_answer_once_before_closeout(self):
        turn = {"id":"ready", "text":"", "output":[]}
        ready = {"type":"execution.ready", "phase":"execute", "data":{"output":"Final answer", "execution_status":"completed", "closeout":"pending"}}
        self.runtime._event(turn, ready)
        self.runtime._event(turn, ready)
        self.assertEqual(turn["text"], "Final answer")
        self.assertNotIn("run.finished", [row.get("label") for row in turn["output"]])
        streamed = {"id":"streamed", "text":"", "output":[]}
        self.runtime._event(streamed, {"type":"text.delta", "phase":"execute", "delta":"Streamed"})
        self.runtime._event(streamed, {"type":"execution.ready", "phase":"execute", "data":{"output":"Streamed"}})
        self.assertEqual(streamed["text"], "Streamed")

    def test_native_tool_errors_keep_the_original_result(self):
        turn = {"id":"native", "text":"", "output":[]}
        self.runtime._event(turn,{"type":"tool.activity","phase":"execute","data":{"tool":{"id":"read-1","name":"Read","stage":"call","input":{"file_path":"missing.txt"}}}})
        self.runtime._event(turn,{"type":"tool.activity","phase":"execute","data":{"tool":{"id":"read-1","name":"Read","stage":"result","is_error":True,"result":"File does not exist"}}})
        self.assertEqual(turn["output"][0]["status"],"failed")
        self.assertEqual(turn["output"][1]["content"],"File does not exist")

    def test_acp_reports_observed_capabilities_even_when_empty(self):
        turn = {"id":"native", "text":"", "output":[]}
        self.runtime._event(turn,{"type":"native.evidence","phase":"execute","component":"harness.acp","data":{"native_type":"native_configuration","raw":{"native_initialization":True,"skills":["jms-ssh"],"mcp_servers":[{"name":"confluence","status":"connected"}]}}})
        self.assertEqual(turn["output"][0]["label"],"原生能力")
        self.assertIn("1 个技能",turn["output"][0]["content"])
        self.assertIn("jms-ssh",turn["output"][0]["content"])
        self.assertIn("confluence",turn["output"][0]["content"])
        self.runtime._event(turn,{"type":"conversation.runtime_renewed","phase":"execute","data":{"previous_session_id":"old"}})
        self.assertEqual(turn["output"][-1]["label"],"执行上下文已更新")
        self.assertIn("原生历史未续接",turn["output"][-1]["content"])

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

    def test_agent_selection_http_requires_owner_origin_and_fixed_schema(self):
        selected=[]
        class Selection:
            def select(self, identifier):
                if identifier != "configured":
                    raise ValueError("unknown Agent")
                selected.append(identifier)
        server=ThreadingHTTPServer(("127.0.0.1",0),Handler)
        server.chat_runtime=Selection();server.mutation_token="owner-token"
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        base=f"http://127.0.0.1:{server.server_port}"
        try:
            payload=json.dumps({"agent_id":"configured"}).encode()
            with self.assertRaises(HTTPError) as caught:
                urlopen(Request(base+"/api/chat/agent",data=payload,headers={"Content-Type":"application/json"}))
            self.assertEqual(caught.exception.code,403);self.assertEqual(selected,[])
            headers={"Content-Type":"application/json","Origin":base,"X-Host-Action":"owner-token"}
            with urlopen(Request(base+"/api/chat/agent",data=payload,headers=headers)) as response:
                self.assertEqual(response.status,200)
            self.assertEqual(selected,["configured"])
            with self.assertRaises(HTTPError) as caught:
                urlopen(Request(base+"/api/chat/agent",data=json.dumps({"agent_id":"configured","config":"outside"}).encode(),headers=headers))
            self.assertEqual(caught.exception.code,400)
        finally:
            server.shutdown();server.server_close();thread.join()


if __name__ == "__main__":
    unittest.main()
