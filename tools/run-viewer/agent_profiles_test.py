"""Prove destination, current-route and historical-evidence boundaries."""

import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import server
from agent_profiles import profiles
from settings_store import SettingsStore


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        (self.root / "config").mkdir()
        self.store = SettingsStore(self.root, "/bin/true", validator=lambda: None)
        self.write("host.json", {"default_profile": "main", "profiles": {
            "main": {"execution": {"agents_config": "agents.json", "model": "service-model"}},
            "feishu": {"execution": {"agents_config": "agents.feishu.json"}}}})
        self.agents = {"default_agent": "default", "agents": [{"id": "default", "name": "General"},
                       {"id": "ops", "name": "Operations", "model": "ops-model", "projects": []}],
                       "group_bindings": [{"conversation_id": "explicit", "agent_id": "default"},
                                          {"conversation_id": "moved", "agent_id": "ops"}]}
        self.write("agents.json", self.agents)
        self.write("agents.feishu.json", {"default_agent": "default", "agents": [{"id": "default", "name": "Feishu"}]})
        self.write("channels.dingtalk.json", {"enabled": True, "settings": {"bot": {"client_id": "app", "corp_id": "tenant"}}})
        self.write("channels.feishu.json", {"enabled": True, "execution_profile": "feishu", "settings": {"app_id": "other"}})
        self.write("datasource.plugins.docs.json", {"enabled": True, "settings": {"api_key": "secret"}})
        self.home = self.root / "state"
        self.home.mkdir()
        self.catalog = SimpleNamespace(catalogs={"dingtalk": server.Catalog(self.home, None, all_chats=True)})
        self.db = sqlite3.connect(self.home / "state.db")
        self.addCleanup(self.db.close)
        self.db.executescript("CREATE TABLE runs(id TEXT,created_at TEXT,payload BLOB); CREATE TABLE channel_messages(channel TEXT,account_id TEXT,received_at TEXT,payload BLOB); CREATE TABLE events(run_id TEXT,seq INTEGER,payload BLOB);")

    def write(self, name, value):
        path = self.root / "config" / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def observe(self, cid, account="app", tenant="tenant", agent="default", run_id="r1"):
        m = {"conversation_id": cid, "tenant_id": tenant, "agent_id": agent, "conversation_type": "2",
             "received_at": "2026-10-20", "run_id": run_id, "reply_status": "delivered", "text": "private-body"}
        self.db.execute("INSERT INTO channel_messages VALUES(?,?,?,?)", ("dingtalk", account, "2026-10-20", json.dumps(m)))
        self.db.commit()

    def result(self, agent="default", destination="dingtalk"):
        return next(p for p in profiles(self.store, self.catalog)["profiles"] if p["id"] == agent and p["destination"] == destination)

    def test_offload_web_profile_is_not_reported_as_claude_or_legacy_tools(self):
        profile=self.write("offload.json",{"version":1,"profile":"local","command":"/bin/true"})
        config=self.write("web-chat.json",{"binary":"/bin/true","cwd":str(self.root),"args":["--home",str(self.home),"--workspace",str(self.root),"--agent","daily","--offload-config",str(profile)]})
        result=profiles(self.store,self.catalog,web_config=config)
        self.assertFalse(result["web_error"])
        web=next(p for p in result["profiles"] if p["destination"]=="web")
        self.assertEqual(web["engine"],"Offload")
        self.assertEqual(web["id"],"daily")
        self.assertEqual(web["connectors"],[])

    def test_destinations_inheritance_and_shared_connectors(self):
        p = self.result()
        self.assertEqual(p["model"], "service-model")
        self.assertEqual(self.result("ops")["model"], "ops-model")
        self.assertEqual(self.result(destination="feishu")["name"], "Feishu")
        self.assertEqual(p["loaded_state"], "unknown")
        self.assertEqual(p["connectors"][0]["scope"], "shared_beta")
        self.assertNotIn("secret", json.dumps(profiles(self.store, self.catalog)))

    def test_current_routing_and_account_tenant_separation(self):
        for group in ("fallback", "explicit", "moved"):
            self.observe(group)
        self.observe("wrong-account", account="other")
        self.observe("wrong-tenant", tenant="other")
        groups = {g["conversation_id"]: g for g in self.result()["groups"]}
        self.assertEqual(set(groups), {"explicit", "fallback"})
        self.assertEqual(groups["fallback"]["routing"], "default")
        moved = self.result("ops")["groups"][0]
        self.assertEqual(moved["conversation_id"], "moved")
        self.assertEqual(moved["last_agent"], "default")

    def test_bounded_activity_and_skill_assembly_are_historical(self):
        self.observe("fallback")
        run = {"id": "r1", "agent_id": "default", "created_at": "2026-10-20", "execution_status": "completed"}
        self.db.execute("INSERT INTO runs VALUES(?,?,?)", ("r1", run["created_at"], json.dumps(run)))
        event = {"type": "workspace.assembled", "data": {"skill_manifest": {"agent_id": "default", "sources": [{"name": "diagnose", "revision": 2}]}}}
        self.db.execute("INSERT INTO events VALUES(?,?,?)", ("r1", 1, json.dumps(event)))
        self.db.commit()
        p = self.result()
        self.assertEqual(p["activity"][0]["key"], "dingtalk:db:r1")
        self.assertEqual(p["activity"][0]["acceptance"], "unverified")
        self.assertEqual(p["skills"][0]["state"], "observed_assembled")
        self.assertNotIn("private-body", json.dumps(p))
        self.assertFalse(self.result(destination="feishu")["activity"])

    def test_platform_snapshot_keeps_destination_revision_and_load_evidence_separate(self):
        self.observe("fallback")
        run = {"id": "r1", "agent_id": "default", "audience_id": "owner", "created_at": "2026-10-23"}
        self.db.execute("INSERT INTO runs VALUES(?,?,?)", ("r1", run["created_at"], json.dumps(run)))
        manifest = {"version": "host-native-skills-v2", "agent_id": "default", "audience_id": "owner", "digest": "snapshot-one", "scopes": ["project:a"],
                    "sources": [{"skill_id": "sftp", "kind": "authored", "title": "SFTP delivery", "revision": 2, "content_digest": "revision-two", "private_body": "never-expose"},
                                {"skill_id": "plugin-dingtalk", "kind": "plugin_usage", "revision": 1}]}
        events = [{"type": "skills.catalog.provided", "at": "2026-10-23", "data": {"manifest": manifest, "unready": {"dependent": "required plugin unavailable"}}},
                  {"type": "skills.load.observed", "status": "completed", "data": {"snapshot_digest": "snapshot-one", "source": {"skill_id": "sftp"}, "input": {"private": "never-expose"}}},
                  {"type": "skills.reference.observed", "status": "failed", "data": {"snapshot_digest": "snapshot-one", "file": {"path": "skills/a/references/us.md"}}},
                  {"type": "skills.load.observed", "status": "completed", "data": {"snapshot_digest": "wrong-snapshot", "source": {"skill_id": "wrong-skill"}}}]
        for seq, event in enumerate(events):
            self.db.execute("INSERT INTO events VALUES(?,?,?)", ("r1", seq, json.dumps(event)))
        self.db.commit()
        p = self.result()
        snapshot = p["platform_catalog"]
        self.assertEqual(p["platform_policy"], "automatic_default")
        self.assertEqual(self.result("ops")["platform_policy"], "existing_selection")
        self.assertEqual(snapshot["run_key"], "dingtalk:db:r1")
        self.assertEqual(snapshot["sources"][0]["revision"], 2)
        self.assertEqual([e["status"] for e in snapshot["loads"]], ["completed", "failed"])
        self.assertNotIn("never-expose", json.dumps(p))
        self.assertNotIn("wrong-skill", json.dumps(p))
        self.assertIsNone(self.result(destination="feishu")["platform_catalog"])
        self.assertIsNone(self.result("ops")["platform_catalog"])
        # A newer tool-free run with an empty manifest must not revive an older catalog.
        self.observe("fallback", run_id="r2")
        self.db.execute("INSERT INTO runs VALUES(?,?,?)", ("r2", "2026-10-24", json.dumps({**run, "id": "r2", "created_at": "2026-10-24"})))
        self.db.execute("INSERT INTO events VALUES(?,?,?)", ("r2", 0, json.dumps({"type": "skills.catalog.provided", "data": {"manifest": {"version": "", "sources": None}}})))
        self.db.commit()
        self.assertEqual(self.result()["platform_catalog"]["sources"], [])
        self.assertEqual(self.result()["platform_catalog"]["run_key"], "dingtalk:db:r2")

    def test_no_history_is_not_no_bound_groups_and_errors_remain_visible(self):
        p = self.result()
        self.assertEqual(p["groups"][0]["conversation_id"], "explicit")
        self.assertIsNone(p["groups"][0]["last_seen"])
        self.db.close()
        (self.home / "state.db").write_bytes(b"broken")
        self.assertEqual(self.result()["evidence_state"], "read_failed")

    def test_role_preview_does_not_read_private_user_context_or_symlinks(self):
        role = self.root / "role.md"
        role.write_text("Handle operational questions.")
        role.chmod(0o600)
        self.agents["agents"][0].update(instructions_file=str(role), user_file=str(self.root / "USER.md"))
        self.write("agents.json", self.agents)
        p = self.result()
        self.assertEqual(p["instructions"][0]["text"], "Handle operational questions.")
        self.assertNotIn("text", p["instructions"][1])
        link = self.root / "link.md"
        link.symlink_to(role)
        self.agents["agents"][0]["instructions_file"] = str(link)
        self.write("agents.json", self.agents)
        self.assertEqual(self.result()["instructions"][0]["state"], "unavailable")

    def test_implicit_default_is_visible_without_creating_an_agent_catalog(self):
        self.write("host.json", {"default_profile": "main", "profiles": {"main": {"execution": {"model": "implicit-model"}}}})
        result = profiles(self.store, self.catalog)
        implicit = next(p for p in result["profiles"] if p["file_id"] == "implicit-dingtalk")
        self.assertEqual(implicit["id"], "default")
        self.assertEqual(implicit["model"], "implicit-model")
        self.assertTrue(result["virtual_files"][0]["read_only"])

    def test_cli_destination_requires_explicit_handoff_catalog_reference(self):
        self.store.handoff = self.write("handoff.json", {"args": ["--agents-config", str(self.root / "config/agents.json"),
                                                                  "--workspace", str(self.root)]})
        p = self.result(destination="cli")
        self.assertEqual(p["workspace"], str(self.root))
        self.assertFalse(p["activity"])
        self.assertFalse(p["groups"])
        self.assertNotEqual(p["key"], self.result()["key"])

    def test_cli_history_requires_the_configured_state_home(self):
        self.catalog.catalogs["cli"] = self.catalog.catalogs["dingtalk"]
        run = {"id": "cli-run", "agent_id": "default", "created_at": "2026-10-20"}
        self.db.execute("INSERT INTO runs VALUES(?,?,?)", (run["id"], run["created_at"], json.dumps(run)))
        self.db.commit()
        self.store.handoff = self.write("handoff.json", {"args": ["--agents-config", str(self.root / "config/agents.json"),
                                                                  "--home", str(self.root / "another-state")]})
        self.assertFalse(self.result(destination="cli")["activity"])
        self.write("handoff.json", {"args": ["--agents-config", str(self.root / "config/agents.json"), "--home", str(self.home)]})
        self.assertEqual(self.result(destination="cli")["activity"][0]["id"], "cli-run")

    def test_engine_projection_does_not_expose_launch_arguments_or_infer_health(self):
        engine = self.write("execution.json", {"engine": "claude", "transport": "acp", "command": "private-command"})
        self.agents["agents"][0]["execution_config"] = str(engine)
        self.write("agents.json", self.agents)
        p = self.result()
        self.assertEqual(p["transport"], "acp")
        self.assertNotIn("private-command", json.dumps(p))
        engine.unlink()
        self.assertIsNone(self.result()["engine"])

    def test_web_uses_actual_connection_and_latest_native_inventory(self):
        engine = self.write("execution.json", {"engine":"claude", "transport":"acp", "command":"PRIVATE_COMMAND"})
        web = self.write("web.json", {"args":["--agent","default","--agents-config",str(self.root/"config/agents.json"),"--home",str(self.home),"--workspace",str(self.root),"--execution-config",str(engine)]})
        self.catalog.catalogs["cli"] = self.catalog.catalogs["dingtalk"]
        for run_id, date, skills in [("older","2026-10-19",["removed-skill"]),("newer","2026-10-20",["jms-ssh"] )]:
            self.db.execute("INSERT INTO runs VALUES(?,?,?)",(run_id,date,json.dumps({"id":run_id,"agent_id":"default","created_at":date})))
            native = {"type":"native.evidence","phase":"execute","component":"harness.acp","model":"actual-model","at":date,"data":{"raw":{"native_initialization":True,"admitted":True,"skills":skills,"mcp_servers":[{"name":"confluence","status":"connected"}],"PRIVATE_SECRET":"SECRET"}}}
            self.db.execute("INSERT INTO events VALUES(?,?,?)",(run_id,1,json.dumps(native)))
            for seq in range(2, 66):
                transcript = {"type": "native.evidence", "phase": "execute", "component": "harness.acp",
                              "data": {"raw": {"output": "TRANSCRIPT_SENTINEL" + "x" * 32768}}}
                self.db.execute("INSERT INTO events VALUES(?,?,?)", (run_id, seq, json.dumps(transcript)))
            rejected = {**native, "data": {"raw": {"native_initialization": True, "admitted": False, "skills": ["rejected"]}}}
            self.db.execute("INSERT INTO events VALUES(?,?,?)", (run_id, 66, json.dumps(rejected)))
        self.db.commit()
        with patch("agent_profiles.json.loads", wraps=json.loads) as decode:
            result=profiles(self.store,self.catalog,web,"ops")
        self.assertFalse(any("TRANSCRIPT_SENTINEL" in str(call.args[0]) for call in decode.call_args_list))
        p=next(p for p in result["profiles"] if p["destination"]=="web" and p["id"]=="default")
        self.assertEqual(p["transport"],"acp")
        self.assertFalse(p["chat_selected"])
        self.assertTrue(next(p for p in result["profiles"] if p["destination"]=="web" and p["id"]=="ops")["chat_selected"])
        self.assertEqual(p["native_inventory"]["skills"],["jms-ssh"])
        self.assertEqual(p["native_inventory"]["services"][0]["status"],"connected")
        self.assertEqual(p["native_inventory"]["run_key"],"dingtalk:db:newer")
        self.assertNotIn("PRIVATE_COMMAND",json.dumps(result))
        self.assertNotIn("PRIVATE_SECRET",json.dumps(result))


if __name__ == "__main__":
    unittest.main()
