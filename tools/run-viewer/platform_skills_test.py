"""Check explicit destinations, private previews and read-only HTTP behavior."""

import json
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import server
from platform_skills import cli_root, destinations, inspect, preview
from settings_store import SettingsStore


class PlatformSkillsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        (self.root / "config").mkdir()
        self.web = self.write("config/web-chat.json", {"args": ["--home", str(self.root / "cli-state")]})
        self.store = SettingsStore(self.root, "/bin/true", web_config=self.web)

    def write(self, name, value):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))
        path.chmod(0o600)
        return path

    def test_explicit_cli_override_file_memory_and_home_precedence(self):
        home = self.root / "cli-state"
        self.assertEqual(cli_root(self.web), home / "managed-skills")
        marker = self.write("cli-state/file-memory.json", {"version": 1, "state_home": str(home), "memory_root": str(self.root / "shared/memory")})
        self.assertEqual(cli_root(self.web), self.root / "shared/managed-skills")
        self.write("config/web-chat.json", {"args": ["--home", str(home), "--skills-root=" + str(self.root / "explicit")]})
        self.assertEqual(cli_root(self.web), self.root / "explicit")
        self.write("config/web-chat.json", {"args": ["--home", str(home)]})
        marker.write_text("invalid")
        with self.assertRaises(ValueError):
            cli_root(self.web)

    def test_no_host_no_inferred_production_and_bad_destination_remains_visible(self):
        self.assertFalse(inspect(None)["enabled"])
        self.store.web_config = None
        self.assertFalse(inspect(self.store)["enabled"])
        self.store.web_config = self.web
        self.write("config/web-chat.json", {"args": ["--home", "relative"]})
        self.assertIn("error", destinations(self.store)[0])
        with patch("platform_skills.subprocess.run") as run:
            self.assertIn("error", inspect(self.store))
            run.assert_not_called()
        with self.assertRaises(KeyError):
            inspect(self.store, "/unconfigured/root")

    def test_channel_roots_follow_the_selected_execution_profile(self):
        self.write("config/host.json", {"default_profile": "main", "profiles": {"main": {"execution": {}}, "other": {"execution": {"skills_root": str(self.root / "separate")}}}})
        self.write("config/channels.dingtalk.json", {"enabled": True})
        self.write("config/channels.feishu.json", {"enabled": False, "execution_profile": "other"})
        roots = {d["id"]: d.get("root") for d in destinations(self.store)}
        self.assertEqual(roots["dingtalk"], str(self.root / "managed-skills"))
        self.assertEqual(roots["feishu"], str(self.root / "separate"))
        self.assertEqual(roots["web"], str(self.root / "cli-state/managed-skills"))

    def test_ambiguous_and_symlinked_config_is_not_followed(self):
        self.write("config/web-chat.json", {"args": ["--home", str(self.root), "--home=" + str(self.root)]})
        with self.assertRaises(ValueError):
            cli_root(self.web)
        link = self.root / "link.json"
        link.symlink_to(self.web)
        with self.assertRaises(ValueError):
            cli_root(link)

    def test_inspection_uses_only_read_command_and_hides_failure_output(self):
        result = {"version": "managed-skills-v1", "records": [], "catalog_digest": "digest"}
        with patch("platform_skills.subprocess.run", return_value=SimpleNamespace(returncode=0, stdout=json.dumps(result).encode())) as run:
            self.assertEqual(inspect(self.store)["catalog_digest"], "digest")
            args = run.call_args.args[0]
            self.assertEqual(args, ["/bin/true", "skills", "inspect", "--root", str(self.root / "cli-state/managed-skills")])
        with patch("platform_skills.subprocess.run", return_value=SimpleNamespace(returncode=1, stderr=b"private-error-secret")):
            failed = inspect(self.store)
            self.assertIn("error", failed)
            self.assertNotIn("private-error-secret", json.dumps(failed))

    def test_full_pinned_reference_read_redacts_credentials(self):
        body = "US reference\n" * 15000 + "password=private-value\nAuthorization: Bearer confidential\n"
        selectors = {"id": "sftp", "revision": "2", "digest": "fixed", "file": "references/us.md"}
        result = {**selectors, "content": body, "path": selectors["file"], "bytes": len(body)}
        with patch("platform_skills.subprocess.run", return_value=SimpleNamespace(returncode=0, stdout=json.dumps(result).encode())) as run:
            data = inspect(self.store, "web", selectors)
            self.assertEqual(data["content"].count("US reference"), 15000)
            self.assertTrue(data["complete"])
            self.assertTrue(data["redacted"])
            self.assertNotIn("private-value", json.dumps(data))
            self.assertNotIn("confidential", json.dumps(data))
            self.assertIn("--digest=fixed", run.call_args.args[0])
        with self.assertRaises(ValueError):
            inspect(self.store, "web", {"file": "references/us.md"})
        self.assertNotIn("hidden", json.dumps(preview({"api_key": "hidden", "text": "https://user:hidden@example.test"})))

    def test_wrong_file_revision_response_is_not_reported_as_a_complete_read(self):
        selectors = {"id": "sftp", "revision": "2", "digest": "fixed", "file": "SKILL.md"}
        data = {**selectors, "digest": "different", "path": "SKILL.md", "content": "wrong revision"}
        with patch("platform_skills.subprocess.run", return_value=SimpleNamespace(returncode=0, stdout=json.dumps(data).encode())):
            result = inspect(self.store, "web", selectors)
        self.assertIn("error", result)
        self.assertNotIn("complete", result)
        self.assertNotIn("wrong revision", json.dumps(result))

    def test_http_rejects_mutations_unknown_destinations_and_arbitrary_roots(self):
        http = server.ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        http.settings_store = self.store
        self.addCleanup(http.server_close)
        thread = threading.Thread(target=http.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(http.shutdown)
        base = "http://127.0.0.1:" + str(http.server_port)
        for suffix, status in [("?root=/tmp", 400), ("?file=SKILL.md", 400), ("?destination=web&destination=cli", 400), ("?destination=unknown", 404)]:
            with self.assertRaises(HTTPError) as error:
                urlopen(base + "/api/platform-skills" + suffix)
            self.assertEqual(error.exception.code, status)
        with self.assertRaises(HTTPError) as error:
            urlopen(Request(base + "/api/platform-skills", b"{}"))
        self.assertEqual(error.exception.code, 405)
        for asset in ("platform-skills.js", "platform-skills.css"):
            with urlopen(base + "/" + asset) as response:
                self.assertEqual(response.status, 200)


if __name__ == "__main__":
    unittest.main()
