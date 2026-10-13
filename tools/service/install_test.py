#!/usr/bin/env python3
"""Offline boundary tests for the production user-service installer."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("agent_host_installer", Path(__file__).with_name("install.py"))
INSTALLER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INSTALLER)
SECRET = "test-credential-never-disclose"


def result(code=0, stdout="", stderr=""):
    return SimpleNamespace(returncode=code, stdout=stdout, stderr=stderr)


def running(pid=12345):
    return result(stdout=f"state = running\npid = {pid}\n")


def absent():
    return result(113, stderr="Could not find service com.agent-host.production")


class Clock:
    def __init__(self):
        self.now = 0.0

    def monotonic(self):
        return self.now

    def sleep(self, duration):
        self.now += duration


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.root = self.base / "user/.agent-host/production"
        self.launch_agents = self.base / "user/Library/LaunchAgents"
        self.binary = self.base / "checkout/bin/agent-host"
        self.binary.parent.mkdir(parents=True)
        self.binary.write_bytes(b"new-native-binary")
        self.binary.chmod(0o700)
        (self.root / "config").mkdir(parents=True)
        for name in ("service.json", "environment.json", "dingtalk.json"):
            (self.root / "config" / name).write_text('{"private":"' + SECRET + '"}')
        self.calls = []

    def checker(self, code=0, body=None):
        if body is None:
            body = json.dumps({"status": "local_configuration_valid"})

        def run(args, **kwargs):
            self.calls.append(args)
            if args[0] != "/bin/launchctl":
                return result(code, body, SECRET)
            raise AssertionError("invalid configuration must not touch launchctl")
        return run

    def fake_launchd(self, failed_operation=None, old_loaded=False):
        loaded = old_loaded

        def run(args, **kwargs):
            nonlocal loaded
            self.calls.append(args)
            if args[0] != "/bin/launchctl":
                return result(stdout=json.dumps({"status": "local_configuration_valid"}))
            operation = args[1]
            if operation == failed_operation:
                return result(1, SECRET, SECRET)
            if operation == "print":
                return running() if loaded else absent()
            if operation == "enable":
                return result()
            if operation == "bootstrap":
                loaded = True
                with (self.root / "logs/service.ndjson").open("a") as log:
                    log.write(json.dumps({"type": "channel.connected"}) + "\n")
                return result()
            if operation == "bootout":
                loaded = False
                return result()
            raise AssertionError(f"unexpected launchctl operation: {operation}")
        return run

    def test_plist_uses_installed_runtime_and_has_no_credentials(self):
        description = INSTALLER.launch_description(self.root)
        self.assertEqual(description["ProgramArguments"], [str(self.root / "bin/agent-host"), "service", "run", "--root", str(self.root), "--viewer-python", INSTALLER.sys.executable])
        self.assertEqual(description["WorkingDirectory"], str(self.root / "workspace"))
        self.assertEqual(description["StandardOutPath"], str(self.root / "logs/service.ndjson"))
        self.assertEqual(description["StandardErrorPath"], str(self.root / "logs/service.stderr.log"))
        self.assertTrue(description["RunAtLoad"])
        self.assertEqual(description["KeepAlive"], {"SuccessfulExit": False})
        self.assertEqual(description["ThrottleInterval"], 30)
        self.assertEqual(description["ExitTimeOut"], 30)
        self.assertEqual(description["Umask"], 0o077)
        self.assertEqual(set(description["EnvironmentVariables"]), {"PATH"})
        serialized = plistlib.dumps(description)
        self.assertNotIn(SECRET.encode(), serialized)
        self.assertNotIn(str(self.binary.parent.parent).encode(), serialized)

    def test_failed_configuration_never_stops_existing_service_or_leaks_checker_output(self):
        with mock.patch.object(INSTALLER, "stop_existing") as stop:
            with self.assertRaises(RuntimeError) as raised:
                INSTALLER.install(self.binary, self.root, self.launch_agents, self.checker(code=1, body=SECRET))
            stop.assert_not_called()
        self.assertNotIn(SECRET, str(raised.exception))
        self.assertFalse(list((self.root / "bin").glob(".agent-host-*")))
        self.assertFalse((self.root / "bin/agent-host").exists())

    def test_invalid_checker_reports_never_stop_existing_service(self):
        for body in ("not-json " + SECRET, "[]", "null", '{"status":"failed"}'):
            with self.subTest(body=body), mock.patch.object(INSTALLER, "stop_existing") as stop:
                with self.assertRaises(RuntimeError) as raised:
                    INSTALLER.install(self.binary, self.root, self.launch_agents, self.checker(body=body))
                stop.assert_not_called()
                self.assertNotIn(SECRET, str(raised.exception))

    def test_missing_production_configuration_never_stops_service(self):
        (self.root / "config/environment.json").unlink()
        with mock.patch.object(INSTALLER, "stop_existing") as stop:
            with self.assertRaises(ValueError):
                INSTALLER.install(self.binary, self.root, self.launch_agents, self.checker())
            stop.assert_not_called()
        self.assertEqual(self.calls, [])

    def test_symlink_install_targets_are_rejected_before_service_stop(self):
        cases = ("binary", "launch_agents", "plist", "receipt", "viewer")
        for kind in cases:
            with self.subTest(kind=kind):
                target = self.base / ("external-" + kind)
                if kind == "binary":
                    (self.root / "bin").mkdir(exist_ok=True)
                    target.write_bytes(b"untouched")
                    link = self.root / "bin/agent-host"
                elif kind == "launch_agents":
                    target.mkdir()
                    self.launch_agents.parent.mkdir(parents=True, exist_ok=True)
                    link = self.launch_agents
                elif kind == "plist":
                    self.launch_agents.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(b"untouched")
                    link = self.launch_agents / (INSTALLER.LABEL + ".plist")
                elif kind == "receipt":
                    target.write_bytes(b"untouched")
                    link = self.root / "installation.json"
                else:
                    (self.root / "viewer").mkdir(exist_ok=True)
                    target.write_bytes(b"untouched")
                    link = self.root / "viewer/server.py"
                link.symlink_to(target)
                try:
                    with mock.patch.object(INSTALLER, "stop_existing") as stop:
                        with self.assertRaises(ValueError):
                            INSTALLER.install(self.binary, self.root, self.launch_agents, self.checker())
                        stop.assert_not_called()
                finally:
                    link.unlink()

    def test_install_preserves_production_state_and_does_not_copy_checkout_data(self):
        for directory in ("state", "native", "workspace"):
            (self.root / directory).mkdir()
            (self.root / directory / "existing").write_bytes(b"production-evidence")
        test_state = self.binary.parent.parent / ".agent-host/state.db"
        test_state.parent.mkdir()
        test_state.write_bytes(b"test-evidence-do-not-import")
        old_config = {p.name: p.read_bytes() for p in (self.root / "config").iterdir()}
        clock = Clock()
        with mock.patch.object(INSTALLER.time, "monotonic", clock.monotonic), mock.patch.object(INSTALLER.time, "sleep", clock.sleep):
            # Inject the fake clock explicitly because default callables bind at import time.
            real_await = INSTALLER.await_connection
            with mock.patch.object(INSTALLER, "await_connection", side_effect=lambda target, log, offset, run: real_await(target, log, offset, run=run, sleep=clock.sleep)):
                receipt = INSTALLER.install(self.binary, self.root, self.launch_agents, self.fake_launchd())
        self.assertEqual(receipt["status"], "connected")
        self.assertFalse(receipt["platform_round_trip_verified"])
        self.assertEqual((self.root / "bin/agent-host").read_bytes(), self.binary.read_bytes())
        self.assertEqual(stat.S_IMODE((self.root / "bin/agent-host").stat().st_mode), 0o700)
        for name in INSTALLER.VIEWER_FILES:
            installed_viewer = self.root / "viewer" / name
            self.assertEqual(installed_viewer.read_bytes(), (Path(__file__).parent.parent / "run-viewer" / name).read_bytes())
            self.assertEqual(stat.S_IMODE(installed_viewer.stat().st_mode), 0o600)
        for directory in ("state", "native", "workspace"):
            self.assertEqual((self.root / directory / "existing").read_bytes(), b"production-evidence")
        self.assertFalse((self.root / "state/state.db").exists())
        self.assertEqual(old_config, {p.name: p.read_bytes() for p in (self.root / "config").iterdir()})
        plist = self.launch_agents / (INSTALLER.LABEL + ".plist")
        self.assertEqual(plistlib.loads(plist.read_bytes()), INSTALLER.launch_description(self.root))
        self.assertNotIn(SECRET, json.dumps(receipt))
        self.assertEqual(json.loads((self.root / "installation.json").read_text()), receipt)
        self.assertEqual([call[1] for call in self.calls if call[0] == "/bin/launchctl"][:3], ["print", "enable", "bootstrap"])

    def test_launchctl_failure_is_sanitized_and_does_not_claim_installation(self):
        for operation in ("enable", "bootstrap"):
            with self.subTest(operation=operation):
                with self.assertRaises(RuntimeError) as raised:
                    INSTALLER.install(self.binary, self.root, self.launch_agents, self.fake_launchd(failed_operation=operation))
                self.assertNotIn(SECRET, str(raised.exception))
                self.assertFalse((self.root / "installation.json").exists())

    def test_stop_failure_preserves_installed_binary(self):
        (self.root / "bin").mkdir()
        installed = self.root / "bin/agent-host"
        installed.write_bytes(b"previous-binary")
        with self.assertRaises(RuntimeError) as raised:
            INSTALLER.install(self.binary, self.root, self.launch_agents, self.fake_launchd(failed_operation="bootout", old_loaded=True))
        self.assertEqual(installed.read_bytes(), b"previous-binary")
        self.assertNotIn(SECRET, str(raised.exception))
        self.assertNotIn("bootstrap", [call[1] for call in self.calls if call[0] == "/bin/launchctl"])

    def test_main_returns_nonzero_for_failed_installation(self):
        with mock.patch.object(INSTALLER.sys, "argv", ["install.py", "--binary", str(self.binary)]), mock.patch.object(INSTALLER.sys, "platform", "darwin"), mock.patch.object(INSTALLER, "install", side_effect=RuntimeError("cannot bootstrap Agent-Host launchd job")):
            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = INSTALLER.main()
        self.assertEqual(code, 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertIn("installation failed", stderr.getvalue())


class LaunchReadinessTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.log = Path(self.temporary.name) / "events.ndjson"
        self.log.touch()
        self.clock = Clock()
        patcher = mock.patch.object(INSTALLER.time, "monotonic", self.clock.monotonic)
        patcher.start()
        self.addCleanup(patcher.stop)

    def await_connection(self, run, offset=0):
        return INSTALLER.await_connection("gui/501/test", self.log, offset, run=run, sleep=self.clock.sleep, timeout=2)

    def test_requires_fresh_connection_and_stable_running_pid(self):
        self.log.write_text('{"type":"channel.connected"}\n')
        run = mock.Mock(return_value=running())
        self.assertEqual(self.await_connection(run), 12345)
        self.assertGreaterEqual(run.call_count, 2, "one process observation cannot confirm startup stability")
        self.assertGreaterEqual(self.clock.now, 1)

    def test_starting_with_pid_waits_for_running_connection(self):
        transient = result(stdout="state = starting\npid = 12345\nresource coalition = {\n    state = running\n}\n")
        self.assertFalse(INSTALLER.launch_state("gui/501/test", lambda args: transient)["running"])
        calls = 0

        def run(args):
            nonlocal calls
            calls += 1
            if calls <= 2:
                state = "spawn scheduled" if calls == 1 else "starting"
                return result(stdout=f"state = {state}\npid = 12345\nresource coalition = {{\n    state = running\n}}\n")
            if calls == 3:
                with self.log.open("a") as stream:
                    stream.write('{"type":"channel.connected"}\n')
            return running()
        self.assertEqual(self.await_connection(run), 12345)
        self.assertGreaterEqual(calls, 4)
        self.assertGreaterEqual(self.clock.now, 1.4)

    def test_exit_before_first_pid_observation_fails_immediately(self):
        with self.assertRaisesRegex(RuntimeError, "stopped"):
            self.await_connection(mock.Mock(return_value=result(stdout="state = not running\nlast exit code = 1\n")))
        self.assertEqual(self.clock.now, 0)

    def test_partial_event_is_buffered_until_complete(self):
        self.log.write_text('{"type":"channel.')
        calls = 0

        def run(args):
            nonlocal calls
            calls += 1
            if calls == 2:
                with self.log.open("a") as stream:
                    stream.write('connected"}\n')
            return running()
        self.assertEqual(self.await_connection(run), 12345)

    def test_historical_connected_event_cannot_prove_new_startup(self):
        self.log.write_text('{"type":"channel.connected"}\n')
        with self.assertRaisesRegex(RuntimeError, "deadline"):
            self.await_connection(mock.Mock(return_value=running()), offset=self.log.stat().st_size)

    def test_connection_followed_by_process_exit_fails(self):
        self.log.write_text('{"type":"channel.connected"}\n')
        run = mock.Mock(side_effect=[running(), result(stdout="state = exited\n")])
        with self.assertRaisesRegex(RuntimeError, "stopped"):
            self.await_connection(run)

    def test_connection_followed_by_error_fails_without_disclosing_log_text(self):
        self.log.write_text('{"type":"channel.connected"}\n')
        calls = 0

        def run(args):
            nonlocal calls
            calls += 1
            if calls == 2:
                with self.log.open("a") as stream:
                    stream.write(json.dumps({"type": "channel.error", "message": SECRET}) + "\n")
            return running()
        with self.assertRaises(RuntimeError) as raised:
            self.await_connection(run)
        self.assertNotIn(SECRET, str(raised.exception))

    def test_pid_replacement_during_startup_is_not_success(self):
        run = mock.Mock(side_effect=[running(111), running(222)])
        with self.assertRaisesRegex(RuntimeError, "process changed"):
            self.await_connection(run)

    def test_malformed_log_event_is_explicit_and_sanitized(self):
        self.log.write_text(SECRET + "\n")
        with self.assertRaises(RuntimeError) as raised:
            self.await_connection(mock.Mock(return_value=running()))
        self.assertNotIn(SECRET, str(raised.exception))

    def test_launchctl_access_errors_are_not_absence(self):
        with self.assertRaisesRegex(RuntimeError, "cannot inspect") as raised:
            INSTALLER.launch_state("gui/501/test", lambda args: result(1, SECRET, "permission denied " + SECRET))
        self.assertNotIn(SECRET, str(raised.exception))
        self.assertEqual(INSTALLER.launch_state("gui/501/test", lambda args: absent()), {"loaded": False, "running": False, "pid": None})

    def test_stop_waits_for_prior_pid_before_confirming_unloaded(self):
        states = iter([running(111), result(), absent()])
        run = mock.Mock(side_effect=lambda args: next(states))
        alive = mock.Mock(side_effect=[True, True, False])
        INSTALLER.stop_existing("gui/501/test", run=run, alive=alive, sleep=self.clock.sleep)
        self.assertGreaterEqual(self.clock.now, 0.2)
        self.assertEqual([call.args[0][1] for call in run.call_args_list], ["print", "bootout", "print"])
        self.assertEqual([call.args[0] for call in alive.call_args_list], [111, 111, 111])


if __name__ == "__main__":
    unittest.main(verbosity=2)
