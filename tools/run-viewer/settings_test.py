"""Exercise configuration boundaries, checked writes and owner-local HTTP actions."""

import json
from pathlib import Path
import tempfile
import threading
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server
from settings_store import SettingsStore, Conflict, KEEP_SECRET


class SettingsTests(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
