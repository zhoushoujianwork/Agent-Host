"""Offline service lifecycle checks; never register services on the test host."""

import json
import contextlib
import io
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from service import autostart as AUTO


class AutostartTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name).resolve() / "checkout with spaces"
        self.config = self.repo / ".agent-host/config"
        self.config.mkdir(parents=True)
        self.home = Path(self.temp.name) / "home"
        self.home.mkdir()
        self.env = {"PATH": "/some tools/bin:/usr/bin", "SECRET_TOKEN": "never-copy"}
        for name in ("dingtalk", "feishu"):
            (self.config / f"channels.{name}.json").write_text(json.dumps({"enabled": True}))
        self.calls = []

    def command(self, args):
        self.calls.append(args)
        missing = args[:2] == ["/bin/launchctl", "print"]
        return subprocess.CompletedProcess(args, int(missing), "", "Could not find service" if missing else "")

    def install(self, platform):
        return AUTO.install(self.repo, self.config, self.home, self.env, platform, self.command)

    def test_macos_repeat_install_checks_config_and_waits_for_both_channels(self):
        with patch.object(AUTO, "await_channels") as wait:
            target = self.install("darwin")
            self.assertEqual(self.install("darwin"), target)
        description = plistlib.loads(target.read_bytes())
        self.assertIn(str(self.config), description["ProgramArguments"])
        self.assertIn("--no-browser", description["ProgramArguments"])
        self.assertTrue(description["RunAtLoad"])
        self.assertEqual(description["KeepAlive"], {"SuccessfulExit": False})
        self.assertNotIn("never-copy", target.read_text())
        self.assertEqual(set(wait.call_args.args[1]), {"feishu", "dingtalk"})
        self.assertEqual(self.calls[0][1:3], ["service", "check"])
        self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_linux_unit_escapes_paths_and_enables_user_service(self):
        self.env["PATH"] = '/tools with "quotes"/$PATH/%bin:/usr/bin'
        with patch.object(AUTO, "await_channels"):
            target = self.install("linux")
        content = target.read_text()
        self.assertIn('$PATH/%%bin', content)
        self.assertEqual(AUTO.systemd_quote('/some/$path/%dir', command=True), '"/some/$$path/%%dir"')
        self.assertIn('\\"quotes\\"', content)
        self.assertIn('WantedBy=default.target', content)
        self.assertNotIn("never-copy", content)
        self.assertIn(["systemctl", "--user", "enable", AUTO.UNIT], self.calls)
        self.assertIn(["systemctl", "--user", "restart", AUTO.UNIT], self.calls)
        with patch.object(AUTO, "await_channels"):
            self.install("linux")
        self.assertIn(["systemctl", "--user", "stop", AUTO.UNIT], self.calls)

    def test_failed_preflight_preserves_existing_definition_without_stopping(self):
        target, _ = AUTO.definition(self.repo, self.config, self.home, self.env, "darwin")
        target.parent.mkdir(parents=True)
        original = plistlib.dumps({"AgentHostCheckout": str(self.repo), "previous": True})
        target.write_bytes(original)
        def fail(args):
            self.calls.append(args)
            return subprocess.CompletedProcess(args, 1, "", "sensitive diagnostic")
        with self.assertRaisesRegex(RuntimeError, "service command failed"):
            AUTO.install(self.repo, self.config, self.home, self.env, "darwin", fail)
        self.assertEqual(target.read_bytes(), original)
        self.assertEqual(len(self.calls), 1)

    def test_foreign_service_and_symlink_are_preserved(self):
        target, _ = AUTO.definition(self.repo, self.config, self.home, self.env, "darwin")
        target.parent.mkdir(parents=True)
        target.write_bytes(plistlib.dumps({"AgentHostCheckout": "/another/checkout"}))
        with self.assertRaisesRegex(ValueError, "another installation"):
            self.install("darwin")
        target.unlink()
        target.symlink_to(self.home / "absent")
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.install("darwin")
        self.assertEqual(self.calls, [])

    def test_start_failure_does_not_report_ready(self):
        def fail(args):
            if "bootstrap" in args:
                return subprocess.CompletedProcess(args, 1, "", "")
            return self.command(args)
        with patch.object(AUTO, "await_channels") as wait:
            with self.assertRaises(RuntimeError):
                AUTO.install(self.repo, self.config, self.home, self.env, "darwin", fail)
            wait.assert_not_called()

    def test_uninstall_removes_only_owned_service(self):
        with patch.object(AUTO, "await_channels"):
            target = self.install("linux")
        (self.config / "host.json").write_text("retained")
        AUTO.uninstall(self.repo, self.config, self.home, self.env, "linux", self.command)
        self.assertFalse(target.exists())
        self.assertEqual((self.config / "host.json").read_text(), "retained")
        self.assertIn(["systemctl", "--user", "disable", "--now", AUTO.UNIT], self.calls)

    def test_readiness_ignores_old_events_and_requires_every_channel(self):
        offsets = {}
        for name in ("feishu", "dingtalk"):
            log = self.config.parent / "channels" / name / "logs/service.ndjson"
            log.parent.mkdir(parents=True)
            log.write_text('{"type":"channel.connected"}\n')
            offsets[name] = log.stat().st_size
        with patch.object(AUTO, "running", return_value=True):
            with self.assertRaisesRegex(RuntimeError, "dingtalk, feishu"):
                AUTO.await_channels(self.config, offsets, "darwin", self.command, timeout=0.01)
        log = self.config.parent / "channels/feishu/logs/service.ndjson"
        with log.open("a") as out:
            out.write('diagnostic line\n{"type":"channel.connected"}\n')
        with patch.object(AUTO, "running", return_value=True):
            with self.assertRaisesRegex(RuntimeError, "dingtalk"):
                AUTO.await_channels(self.config, offsets, "darwin", self.command, timeout=0.01)
        log = self.config.parent / "channels/dingtalk/logs/service.ndjson"
        with log.open("a") as out:
            out.write('{"type":"channel.connected"}\n')
        with patch.object(AUTO, "running", return_value=True):
            AUTO.await_channels(self.config, offsets, "darwin", self.command, timeout=2)

    def test_unsupported_platform_is_explicit(self):
        with self.assertRaisesRegex(ValueError, "supports macOS and Linux"):
            self.install("win32")
        self.assertEqual(self.calls, [])

    def test_runtime_dir_uses_independent_bundle_config(self):
        with patch.object(AUTO, "install", return_value=self.home / "service") as install, \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(AUTO.main(["install", "--runtime-dir", str(self.repo)]), 0)
        self.assertEqual(install.call_args.args[:2], (self.repo, self.repo / "config"))


if __name__ == "__main__":
    unittest.main()
