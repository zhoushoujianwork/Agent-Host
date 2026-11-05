"""Exercise configuration boundaries, checked writes and owner-local HTTP actions."""

import json
import sys
from pathlib import Path
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server
from settings_store import SettingsStore, Conflict, KEEP_SECRET, validate_business_runtime


class SettingsTests(unittest.TestCase):
    def test_business_runtime_path_policy_validation_and_roundtrip(self):
        source = self.root / "fixture-credential"
        source.write_text("synthetic-business-auth")
        binding = {"home_files": [{"source": str(source), "target": ".tccli/test.credential"}], "bin_directory": str(self.root)}
        row = self.row("agents.cli.json")
        row["value"]["agents"][0]["business_runtime"] = binding
        self.save(row, row["value"])
        saved = json.loads((self.config / "agents.cli.json").read_text())
        self.assertEqual(saved["agents"][0]["business_runtime"], binding)
        binding["home_files"][0]["target"] = ".claude/build-hub/token"
        row = self.row("agents.cli.json")
        row["value"]["agents"][0]["business_runtime"] = binding
        self.save(row, row["value"])
        saved = json.loads((self.config / "agents.cli.json").read_text())
        self.assertEqual(saved["agents"][0]["business_runtime"], binding)
        for target in ("../escape", ".claude/settings.json", ".claude/skills/x/SKILL.md", ".claude/build-hub/other", ".claude/build-hub", ".claude/build-hub/token/child", ".Claude/build-hub/token", ".claude/Build-Hub/token", ".claude/build-hub/Token", ".claude/build-hub/./token", ".claude//build-hub/token", ".zshenv", "a//b", ".profile", ".bash_profile", ".bashrc"):
            with self.assertRaises(ValueError):
                validate_business_runtime({"home_files": [{"source": str(source), "target": target}]})
        alias = self.root / "credential-alias"
        alias.symlink_to(source)
        with self.assertRaises(ValueError):
            validate_business_runtime({"home_files": [{"source": str(alias), "target": "credential"}]})
        with self.assertRaises(ValueError):
            validate_business_runtime({"token": "must-not-be-configured"})

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="settings-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.config = self.root / "config"
        self.config.mkdir(mode=0o700)
        self.write("host.json", {"profiles": {"main": {"environment_file": "credentials/provider.json"}}})
        self.write("credentials/provider.json", {"ANTHROPIC_AUTH_TOKEN": "private-token", "ANTHROPIC_BASE_URL": "https://example.test"})
        self.agents = {"default_agent": "daily", "agents": [{"id": "daily", "name": "Daily assistant", "projects": []}]}
        self.write("agents.cli.json", self.agents)
        self.store = SettingsStore(self.root, "/bin/true", validator=lambda: None)

    def write(self, name, value):
        path = self.config / name
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def row(self, name):
        return next(row for row in self.store.index()["files"] if row["path"].endswith("/" + name))

    def save(self, row, value):
        return self.store.save(row["id"], row["revision"], value)

    def test_credentials_are_masked_retained_replaced_and_backed_up(self):
        row = self.row("provider.json")
        self.assertNotIn("private-token", json.dumps(self.store.index()))
        self.assertTrue(row["value"]["ANTHROPIC_AUTH_TOKEN"].startswith(KEEP_SECRET))
        row["value"]["ANTHROPIC_BASE_URL"] = "https://new.example.test"
        result = self.save(row, row["value"])
        path = self.config / "credentials/provider.json"
        self.assertEqual(json.loads(path.read_text())["ANTHROPIC_AUTH_TOKEN"], "private-token")
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertTrue(list((self.config / ".settings-history").glob("*.bak")))
        fresh = result["file"]
        fresh["value"]["ANTHROPIC_AUTH_TOKEN"] = "replacement"
        self.save(fresh, fresh["value"])
        self.assertEqual(json.loads(path.read_text())["ANTHROPIC_AUTH_TOKEN"], "replacement")

    def test_reordered_accounts_keep_their_own_secrets(self):
        path = self.write("datasource.plugins.email.json", {"settings": {"accounts": [
            {"id": "first", "password": "first-secret"}, {"id": "second", "password": "second-secret"}]}})
        row = self.row("datasource.plugins.email.json")
        row["value"]["settings"]["accounts"].pop(0)
        self.save(row, row["value"])
        self.assertEqual(json.loads(path.read_text())["settings"]["accounts"], [{"id": "second", "password": "second-secret"}])

    def test_stale_revision_and_invalid_agent_do_not_overwrite(self):
        row = self.row("agents.cli.json")
        self.write("agents.cli.json", {**self.agents, "group_bindings": []})
        before = (self.config / "agents.cli.json").read_bytes()
        with self.assertRaises(Conflict):
            self.save(row, row["value"])
        fresh = self.row("agents.cli.json")
        fresh["value"]["default_agent"] = "missing"
        with self.assertRaises(ValueError):
            self.save(fresh, fresh["value"])
        self.assertEqual((self.config / "agents.cli.json").read_bytes(), before)

    def test_native_validation_failure_restores_exact_bytes(self):
        path = self.config / "host.json"
        before = path.read_bytes()
        row = self.row("host.json")
        row["value"]["default_profile"] = "invalid"
        def reject():
            self.assertIn("invalid", path.read_text())
            raise ValueError("native failure with private-token")
        self.store.validator = reject
        with self.assertRaisesRegex(ValueError, "previous file was restored") as raised:
            self.save(row, row["value"])
        self.assertNotIn("private-token", str(raised.exception))
        self.assertEqual(path.read_bytes(), before)

    def test_unexpected_checker_failure_restores_exact_bytes(self):
        path = self.config / "agents.cli.json"
        before = path.read_bytes()
        row = self.row("agents.cli.json")
        row["value"]["agents"][0]["name"] = "Changed name"
        def reject():
            raise ImportError("private-checker-detail")
        self.store.validator = reject
        with self.assertRaisesRegex(ValueError, "previous file was restored") as raised:
            self.save(row, row["value"])
        self.assertNotIn("private-checker-detail", str(raised.exception))
        self.assertEqual(path.read_bytes(), before)

    def test_real_handoff_checker_resolves_sibling_imports_without_execution(self):
        # /usr/bin/false is deliberately non-runnable as a task: --check must
        # validate the catalog without invoking the configured Host executable.
        handoff = self.root / "handoff.json"
        handoff.write_text(json.dumps({"binary": "/usr/bin/false", "cwd": str(self.root),
            "environment_file": str(self.config / "credentials/provider.json"),
            "args": ["--home", str(self.root / "cli-state"), "--agents-config", str(self.config / "agents.cli.json"),
                     "--agent", "daily", "--workspace", str(self.root), "--native-profile", str(self.root / "native")]}))
        handoff.chmod(0o600)
        self.store.handoff = handoff
        self.store.validator = self.store.check_service
        row = self.row("agents.cli.json")
        row["value"]["agents"][0]["name"] = "Checked name"
        self.assertEqual(self.save(row, row["value"])["status"], "saved")
        self.assertFalse((self.root / "cli-state").exists())

    def test_symlinks_unknown_files_and_unrecognized_secret_markers_are_rejected(self):
        target = self.config / "agents.link.json"
        target.symlink_to(self.config / "agents.cli.json")
        row = self.row("agents.link.json")
        self.assertIn("error", row)
        with self.assertRaises(ValueError):
            self.store.save(row["id"], "anything", self.agents)
        with self.assertRaises(KeyError):
            self.store.save("../../outside", "anything", {})
        row = self.row("agents.cli.json")
        row["value"]["agents"][0]["name"] = KEEP_SECRET + "unknown"
        with self.assertRaises(ValueError):
            self.save(row, row["value"])

    def test_plaintext_referenced_credentials_never_appear_in_responses(self):
        secret = self.config / "credentials/client-secret"
        secret.write_text("plain-secret")
        secret.chmod(0o600)
        self.write("channels.test.json", {"settings": {"client_secret_file": str(secret)}})
        row = self.row("client-secret")
        self.assertTrue(row["secret"])
        self.assertNotIn("plain-secret", json.dumps(self.store.index()))
        self.save(row, "new-secret")
        self.assertEqual(secret.read_text(), "new-secret")

    def test_opaque_credential_json_is_masked_as_a_whole_and_preserved(self):
        path = self.write("credentials/oauth.json", {"opaque_account_data": "opaque-secret"})
        self.write("channels.test.json", {"settings": {"credentials_file": str(path)}})
        before = path.read_bytes()
        row = self.row("oauth.json")
        self.assertTrue(row["secret"])
        self.assertNotIn("opaque-secret", json.dumps(self.store.index()))
        self.save(row, row["value"])
        self.assertEqual(path.read_bytes(), before)

    def test_web_agent_create_edit_and_invalid_capability_rollback(self):
        profile = self.root / "native"
        skill = profile / "skills" / "example"
        skill.mkdir(parents=True)
        (skill / "SKILL.md").write_text("installed fixture")
        sources = self.write("datasource.plugins.local-documents.json", {"enabled": True, "settings": {"sources": [{"name": "notes", "token": "never-expose"}]}})
        web = self.write("web-chat.json", {"args": ["--agents-config", str(self.config / "agents.cli.json"), "--agent", "daily", "--home", str(self.root / "cli-state"), "--workspace", str(self.root), "--native-profile", str(profile), "--knowledge-config", str(sources)]})
        self.store.web_config = web
        options = self.store.agent_options()
        self.assertEqual(options["skills"], [{"id": "example", "name": "example"}])
        self.assertEqual(options["knowledge_sources"][0]["id"], "local-documents:notes")
        self.assertNotIn("never-expose", json.dumps(options))
        row = self.row("agents.cli.json")
        row["value"]["agents"].append({"id": "new", "name": "New", "workspace": str(self.root), "native_skills": ["example"], "knowledge_sources": ["local-documents:notes"]})
        result = self.save(row, row["value"])
        self.assertEqual(result["file"]["value"]["agents"][1]["native_skills"], ["example"])
        before = (self.config / "agents.cli.json").read_bytes()
        for key, values in (("native_skills", ["../escape"]), ("knowledge_sources", ["missing"]), ("native_skills", ["example", "example"])):
            changed = json.loads(before)
            changed["agents"][1][key] = values
            with self.assertRaises(ValueError):
                self.save(result["file"], changed)
            self.assertEqual((self.config / "agents.cli.json").read_bytes(), before)
        result["file"]["value"]["agents"][1]["native_skills"] = []
        result["file"]["value"]["agents"][1]["knowledge_sources"] = []
        self.assertEqual(self.save(result["file"], result["file"]["value"])["status"], "saved")

    def test_web_agent_explicit_skill_root_preserves_raw_fields_on_ordinary_edits(self):
        profile = self.root / "native"
        (profile / "skills").mkdir(parents=True)
        explicit = self.root / "operations-capabilities"
        skill = explicit / "skills" / "explicit-only"
        skill.mkdir(parents=True)
        explicit.chmod(0o700)
        (explicit / "skills").chmod(0o700)
        (skill / "SKILL.md").write_text("explicit fixture")
        web = self.write("web-chat.json", {"args": ["--agents-config", str(self.config / "agents.cli.json"), "--agent", "daily", "--home", str(self.root / "cli-state"), "--workspace", str(self.root), "--native-profile", str(profile)]})
        self.store.web_config = web
        catalog = {"default_agent": "daily", "agents": [
            {"id": "daily", "name": "Daily", "native_skills_root": "../operations-capabilities", "native_skills": ["explicit-only"], "memory_offload_config": "memory.json", "business_runtime": {"bin_directory": str(explicit)}, "raw_future_field": {"keep": True}},
            {"id": "other", "name": "Other"}]}
        self.write("agents.cli.json", catalog)
        row = self.row("agents.cli.json")
        row["value"]["default_agent"] = "other"
        row["value"]["agents"][0]["name"] = "Renamed daily"
        self.save(row, row["value"])
        saved = json.loads((self.config / "agents.cli.json").read_text())
        self.assertEqual(saved["default_agent"], "other")
        self.assertEqual(saved["agents"][0]["native_skills_root"], "../operations-capabilities")
        self.assertEqual(saved["agents"][0]["native_skills"], ["explicit-only"])
        self.assertEqual(saved["agents"][0]["memory_offload_config"], "memory.json")
        self.assertEqual(saved["agents"][0]["business_runtime"], {"bin_directory": str(explicit)})
        self.assertEqual(saved["agents"][0]["raw_future_field"], {"keep": True})

    def test_web_agent_explicit_skill_root_and_legacy_inventory_fail_closed(self):
        profile = self.root / "native"
        global_skill = profile / "skills" / "global-only"
        global_skill.mkdir(parents=True)
        (global_skill / "SKILL.md").write_text("global fixture")
        explicit = self.root / "operations-capabilities"
        explicit_skill = explicit / "skills" / "explicit-only"
        explicit_skill.mkdir(parents=True)
        explicit.chmod(0o700)
        (explicit / "skills").chmod(0o700)
        (explicit_skill / "SKILL.md").write_text("explicit fixture")
        web = self.write("web-chat.json", {"args": ["--agents-config", str(self.config / "agents.cli.json"), "--agent", "daily", "--home", str(self.root / "cli-state"), "--workspace", str(self.root), "--native-profile", str(profile)]})
        self.store.web_config = web
        base = {"default_agent": "daily", "agents": [{"id": "daily", "name": "Daily", "native_skills_root": str(explicit), "native_skills": ["explicit-only"]}]}
        self.write("agents.cli.json", base)
        row = self.row("agents.cli.json")

        cases = []
        missing = json.loads(json.dumps(row["value"]))
        missing["agents"][0]["native_skills"] = ["missing"]
        cases.append(missing)
        linked_skill = explicit / "skills" / "linked-skill"
        linked_skill.mkdir()
        (linked_skill / "SKILL.md").symlink_to(explicit_skill / "SKILL.md")
        linked_definition = json.loads(json.dumps(row["value"]))
        linked_definition["agents"][0]["native_skills"] = ["linked-skill"]
        cases.append(linked_definition)
        alias = self.root / "capability-alias"
        alias.symlink_to(explicit, target_is_directory=True)
        symlinked = json.loads(json.dumps(row["value"]))
        symlinked["agents"][0]["native_skills_root"] = str(alias)
        cases.append(symlinked)
        nonprivate = self.root / "nonprivate-capabilities"
        nonprivate_skill = nonprivate / "skills" / "explicit-only"
        nonprivate_skill.mkdir(parents=True)
        (nonprivate / "skills").chmod(0o700)
        (nonprivate_skill / "SKILL.md").write_text("fixture")
        nonprivate.chmod(0o755)
        exposed = json.loads(json.dumps(row["value"]))
        exposed["agents"][0]["native_skills_root"] = str(nonprivate)
        cases.append(exposed)
        legacy = json.loads(json.dumps(row["value"]))
        legacy["agents"][0].pop("native_skills_root")
        legacy["agents"][0]["native_skills"] = ["missing"]
        cases.append(legacy)

        before = (self.config / "agents.cli.json").read_bytes()
        for changed in cases:
            with self.subTest(changed=changed["agents"][0]):
                with self.assertRaises(ValueError):
                    self.save(row, changed)
                self.assertEqual((self.config / "agents.cli.json").read_bytes(), before)

        for selection in ([], None, "absent"):
            row = self.row("agents.cli.json")
            changed = json.loads(json.dumps(row["value"]))
            if selection == "absent":
                changed["agents"][0].pop("native_skills")
            else:
                changed["agents"][0]["native_skills"] = selection
            self.assertEqual(self.save(row, changed)["status"], "saved")

        row = self.row("agents.cli.json")
        unsafe_empty = json.loads(json.dumps(row["value"]))
        unsafe_empty["agents"][0]["native_skills_root"] = str(alias)
        unsafe_empty["agents"][0]["native_skills"] = []
        with self.assertRaises(ValueError):
            self.save(row, unsafe_empty)

    def test_owner_http_checks_conflicts_and_settings_assets(self):
        http = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        http.settings_store = self.store
        http.mutation_token = "owner-token"
        thread = threading.Thread(target=http.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(http.server_close)
        self.addCleanup(http.shutdown)
        base = "http://127.0.0.1:" + str(http.server_port)
        with urlopen(base + "/api/settings") as response:
            catalog = json.load(response)
        self.assertNotIn("private-token", json.dumps(catalog))
        row = next(r for r in catalog["files"] if r["path"].endswith("/agents.cli.json"))
        row["value"]["agents"][0]["name"] = "Changed in browser"
        payload = json.dumps({"file": row["id"], "revision": row["revision"], "value": row["value"]}).encode()
        headers = {"Content-Type": "application/json", "Origin": base, "X-Host-Action": "owner-token"}
        for bad in ({**headers, "Origin": "http://outside.test"}, {**headers, "X-Host-Action": "wrong"}):
            with self.assertRaises(HTTPError) as error:
                urlopen(Request(base + "/api/settings", payload, bad))
            self.assertEqual(error.exception.code, 403)
        with urlopen(Request(base + "/api/settings", payload, headers)) as response:
            self.assertEqual(json.load(response)["status"], "saved")
        with self.assertRaises(HTTPError) as error:
            urlopen(Request(base + "/api/settings", payload, headers))
        self.assertEqual(error.exception.code, 409)
        with urlopen(base + "/api/agent-profiles") as response:
            profile = json.load(response)["profiles"][0]
            self.assertEqual(profile["id"], "daily")
            self.assertEqual(profile["loaded_state"], "unknown")
        for asset in ("settings.html", "settings.js", "settings.css", "agent-profiles.js", "agent-profiles.css"):
            with urlopen(base + "/" + asset) as response:
                self.assertEqual(response.status, 200)


class AppRuntimeSettingsTests(unittest.TestCase):
    """An app-owned root exposes Host/Agent settings without inventing a backend."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="settings-app-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve() / "runtime"
        for name in ("config", "cli-state", "workspace"):
            (self.root / name).mkdir(parents=True, mode=0o700)
        self.write(self.root / ".agh-app-runtime.json", {"schema": 1, "owner": "agent-host-macos-app"})
        self.agents = self.write(self.root / "config/agents.cli.json", {
            "default_agent": "default", "agents": [{
                "id": "default", "name": "Local assistant", "projects": [],
                "native_skills": [], "knowledge_sources": []}]})
        self.web = self.write(self.root / "config/web-chat.json", {
            "binary": sys.executable, "cwd": str(self.root / "workspace"),
            "args": ["--home", str(self.root / "cli-state"), "--agents-config", str(self.agents),
                     "--agent", "default", "--workspace", str(self.root / "workspace")]})
        self.validations = 0
        def validator():
            self.validations += 1
        self.store = SettingsStore(self.root, "/bin/true", handoff=self.web,
                                   validator=validator, web_config=self.web)

    def write(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def rows(self):
        return {Path(row["path"]).name: row for row in self.store.index()["files"]}

    def test_app_root_lists_handoff_and_exact_agent_catalog_without_acp_rows(self):
        rows = self.rows()
        self.assertTrue(self.store.app_root)
        self.assertEqual(rows["web-chat.json"]["category"], "local-runtime")
        self.assertEqual(rows["web-chat.json"]["application"], "cli")
        self.assertEqual(rows["agents.cli.json"]["application"], "cli")
        self.assertNotIn("execution.acp.json", rows)
        self.assertNotIn("environment.json", rows)
        self.assertEqual(rows["agents.cli.json"]["value"]["agents"][0]["native_skills"], [])
        self.assertEqual(self.store.agent_options()["skills"], [])

    def test_agent_edit_preserves_empty_skill_allowlist_and_future_fields(self):
        catalog = json.loads(self.agents.read_text())
        catalog["agents"][0]["raw_future_field"] = {"keep": True}
        self.write(self.agents, catalog)
        row = self.rows()["agents.cli.json"]
        row["value"]["agents"][0]["name"] = "Renamed"
        result = self.store.save(row["id"], row["revision"], row["value"])
        saved = json.loads(self.agents.read_text())
        self.assertEqual(saved["agents"][0]["name"], "Renamed")
        self.assertEqual(saved["agents"][0]["native_skills"], [])
        self.assertEqual(saved["agents"][0]["raw_future_field"], {"keep": True})
        self.assertEqual(result["status"], "saved")
        self.assertEqual(self.validations, 1)

    def test_non_app_root_keeps_generic_handoff_and_no_synthetic_rows(self):
        (self.root / ".agh-app-runtime.json").unlink()
        store = SettingsStore(self.root, "/bin/true", handoff=self.web, validator=lambda: None)
        rows = {Path(row["path"]).name: row for row in store.index()["files"]}
        self.assertFalse(store.app_root)
        self.assertEqual(rows["web-chat.json"]["category"], "handoff")
        self.assertNotIn("execution.acp.json", rows)

    def test_active_task_blocks_http_save_before_write_and_releases_gate(self):
        from http.server import ThreadingHTTPServer
        from types import SimpleNamespace
        from maintenance import MaintenanceGate
        runtime = SimpleNamespace(lock=threading.RLock(), active="run", children={})
        chat = SimpleNamespace(lock=threading.RLock(), contexts={"task": runtime})
        http = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        http.settings_store, http.mutation_token = self.store, "owner-token"
        http.maintenance_gate, http.chat_runtime = MaintenanceGate(), chat
        http.catalog = SimpleNamespace(connection=lambda: (_ for _ in ()).throw(FileNotFoundError()))
        threading.Thread(target=http.serve_forever, daemon=True).start()
        self.addCleanup(http.server_close)
        self.addCleanup(http.shutdown)
        base = "http://127.0.0.1:" + str(http.server_port)
        headers = {"Content-Type": "application/json", "Origin": base, "X-Host-Action": "owner-token"}
        row = self.rows()["agents.cli.json"]
        row["value"]["agents"][0]["name"] = "Blocked edit"
        payload = json.dumps({"file": row["id"], "revision": row["revision"], "value": row["value"]}).encode()
        before = self.agents.read_bytes()
        with self.assertRaises(HTTPError) as error:
            urlopen(Request(base + "/api/settings", payload, headers))
        self.assertEqual(error.exception.code, 409)
        self.assertEqual(self.agents.read_bytes(), before)
        self.assertFalse(http.maintenance_gate.quiesced)
        runtime.active = None
        with urlopen(Request(base + "/api/settings", payload, headers)) as response:
            self.assertEqual(json.load(response)["status"], "saved")
        self.assertFalse(http.maintenance_gate.quiesced)

    def test_non_app_settings_saves_do_not_use_gate(self):
        from http.server import ThreadingHTTPServer
        from maintenance import MaintenanceGate
        (self.root / ".agh-app-runtime.json").unlink()
        store = SettingsStore(self.root, "/bin/true", validator=lambda: None)
        http = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        gate = MaintenanceGate()
        gate.begin = lambda *args: (_ for _ in ()).throw(AssertionError("gate must not be used"))
        http.settings_store, http.mutation_token, http.maintenance_gate = store, "owner-token", gate
        threading.Thread(target=http.serve_forever, daemon=True).start()
        self.addCleanup(http.server_close)
        self.addCleanup(http.shutdown)
        base = "http://127.0.0.1:" + str(http.server_port)
        row = next(item for item in store.index()["files"] if item["path"].endswith("agents.cli.json"))
        payload = json.dumps({"file": row["id"], "revision": row["revision"], "value": row["value"]}).encode()
        headers = {"Content-Type": "application/json", "Origin": base, "X-Host-Action": "owner-token"}
        with urlopen(Request(base + "/api/settings", payload, headers)) as response:
            self.assertEqual(response.status, 200)


if __name__ == "__main__":
    unittest.main()
