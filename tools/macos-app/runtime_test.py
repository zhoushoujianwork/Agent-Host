import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import stat
import sys
import tempfile
import unittest
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("runtime", HERE / "runtime.py")
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)
sys.path.insert(0, str(HERE.parent / "run-viewer"))
import chat_runtime  # noqa: E402

REAL_BINARY = HERE.parents[1] / "bin/agent-host"


def fake_project_host(calls):
    def run(argv, **kwargs):
        calls.append(argv)
        home = Path(argv[argv.index("--home") + 1])
        home.mkdir(exist_ok=True)
        connection = sqlite3.connect(home / "state.db")
        connection.execute("CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY, workspace TEXT)")
        connection.execute("INSERT OR REPLACE INTO projects VALUES(?,?)",
                           (argv[argv.index("--id") + 1], argv[argv.index("--workspace") + 1]))
        connection.commit()
        connection.close()
        return SimpleNamespace(returncode=0, stdout="{}", stderr="")
    return run


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.root = self.base / "support/runtime"
        self.calls = []
        self.run = fake_project_host(self.calls)
        self.binary = self.executable(self.base / "bundle/agent-host")

    @staticmethod
    def executable(path):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!/bin/sh\nexit 0\n")
        path.chmod(0o755)
        return str(path)

    def init(self, binary=None):
        return runtime.initialize(self.root, binary or self.binary, run=self.run)

    def read(self, name):
        return json.loads((self.root / "config" / name).read_text())

    def test_first_init_is_ready_private_and_inherits_offload_default(self):
        result = self.init()
        self.assertEqual((result["status"], result["problems"]), ("ready", []))
        self.assertTrue(result["settings"]["inherits_offload_default"])
        for name in ("", "config", "cli-state", "workspace"):
            self.assertEqual(stat.S_IMODE(os.lstat(self.root / name).st_mode), 0o700, name)
        for name in ("agents.cli.json", "web-chat.json"):
            self.assertEqual(stat.S_IMODE((self.root / "config" / name).stat().st_mode), 0o600)
        self.assertTrue(chat_runtime.is_app_runtime(self.root))
        self.assertEqual(self.calls[0][:3], [self.binary, "project", "add"])

    def test_seeded_handoff_has_only_host_scope_and_no_backend_selection(self):
        self.init()
        handoff = self.read("web-chat.json")
        args = handoff["args"]
        pairs = dict(zip(args[::2], args[1::2]))
        self.assertEqual(set(pairs), set(runtime.DEFAULT_FLAGS))
        self.assertEqual(pairs["--home"], str(self.root / "cli-state"))
        self.assertEqual(pairs["--workspace"], str(self.root / "workspace"))
        for forbidden in ("--model", "--native-profile", "--execution-config", "--claude",
                          "--jev-provider", "--jev-model"):
            self.assertNotIn(forbidden, args)
        self.assertFalse((self.root / "config/execution.acp.json").exists())
        self.assertFalse((self.root / "native").exists())
        self.assertFalse((self.root / "config/credentials").exists())

    def test_seeded_agent_uses_an_exact_empty_skill_allowlist(self):
        self.init()
        catalog = self.read("agents.cli.json")
        self.assertEqual(catalog["agents"][0]["native_skills"], [])
        self.assertEqual(catalog["agents"][0]["knowledge_sources"], [])
        self.assertNotIn("native_skills_root", catalog["agents"][0])

    def test_repeat_init_preserves_agent_catalog_and_unknown_handoff_fields(self):
        self.init()
        catalog = self.read("agents.cli.json")
        catalog["agents"][0]["name"] = "Renamed"
        catalog["agents"][0]["raw_future_field"] = {"keep": True}
        (self.root / "config/agents.cli.json").write_text(json.dumps(catalog))
        handoff = self.read("web-chat.json")
        handoff["note"] = "keep"
        (self.root / "config/web-chat.json").write_text(json.dumps(handoff))
        before_catalog = self.read("agents.cli.json")
        result = self.init()
        self.assertEqual(result["status"], "ready")
        self.assertEqual(self.read("agents.cli.json"), before_catalog)
        self.assertEqual(self.read("web-chat.json")["note"], "keep")
        self.assertEqual(len(self.calls), 1)

    def test_relocated_binary_is_rebased_without_changing_arguments(self):
        self.init()
        before = self.read("web-chat.json")
        moved = self.executable(self.base / "new/agent-host")
        self.init(moved)
        after = self.read("web-chat.json")
        self.assertEqual(after["binary"], moved)
        self.assertEqual(after["args"], before["args"])

    def test_unsupported_legacy_acp_options_fail_closed(self):
        self.init()
        handoff = self.read("web-chat.json")
        handoff["args"] += ["--execution-config", str(self.root / "config/execution.acp.json")]
        (self.root / "config/web-chat.json").write_text(json.dumps(handoff))
        result = self.init()
        self.assertEqual(result["status"], "needs_attention")
        self.assertTrue(any("unsupported" in problem for problem in result["problems"]))
        self.assertIn("--execution-config", self.read("web-chat.json")["args"],
                      "invalid prior configuration is reported, not silently rewritten")

    def test_app_runtime_does_not_pin_an_explicit_offload_profile(self):
        self.init()
        profile = self.root / "config/offload.json"
        profile.write_text("{}")
        profile.chmod(0o600)
        handoff = self.read("web-chat.json")
        handoff["args"] += ["--offload-config", str(profile)]
        (self.root / "config/web-chat.json").write_text(json.dumps(handoff))
        result = self.init()
        self.assertEqual(result["status"], "needs_attention")
        self.assertTrue(any("unsupported" in problem for problem in result["problems"]))

    def test_symlink_and_unmarked_roots_are_refused_without_adoption(self):
        self.root.parent.mkdir(parents=True)
        outside = self.base / "outside"
        outside.mkdir()
        os.symlink(outside, self.root)
        with self.assertRaises(runtime.SetupError):
            self.init()
        self.assertEqual(list(outside.iterdir()), [])
        os.unlink(self.root)
        self.root.mkdir()
        with self.assertRaises(runtime.SetupError):
            self.init()
        self.assertFalse((self.root / runtime.MARKER).exists())

    def test_viewer_arguments_are_bounded_to_app_state(self):
        result = self.init()
        args = result["viewer_arguments"]
        self.assertEqual(args[args.index("--home") + 1], str(self.root / "cli-state"))
        self.assertEqual(args[args.index("--backup-root") + 1], str(self.root))
        self.assertEqual(args[args.index("--chat-config") + 1], str(self.root / "config/web-chat.json"))
        self.assertIn("--all-chats", args)

    @unittest.skipUnless(REAL_BINARY.exists(), "build the Host binary for integration validation")
    def test_seeded_runtime_passes_current_offload_chat_validation(self):
        result = runtime.initialize(self.root, str(REAL_BINARY), run=fake_project_host([]))
        command, cwd, _, _ = chat_runtime.configured_launch(Path(result["config"]))
        self.assertEqual(Path(cwd), self.root / "workspace")
        self.assertNotIn("--offload-config", command)
        self.assertNotIn("--native-profile", command)


if __name__ == "__main__":
    unittest.main()
