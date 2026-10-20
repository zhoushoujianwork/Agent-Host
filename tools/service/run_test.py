#!/usr/bin/env python3
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

SPEC = importlib.util.spec_from_file_location("local_run", Path(__file__).with_name("run.py"))
RUN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUN)


class MakeRunTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="make run with spaces ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.checkout = self.root / "checkout"
        (self.checkout / "bin").mkdir(parents=True)
        binary = self.checkout / "bin/agent-host"
        binary.write_text("#!/usr/bin/env python3\nimport json,sys\nfrom pathlib import Path\n"
                          "Path('captured.json').write_text(json.dumps(sys.argv[1:]))\n")
        binary.chmod(0o700)
        self.makefile = Path(__file__).resolve().parents[2] / "Makefile"
        self.env = dict(os.environ)
        for key in ("CONFIG_DIR", "RUNTIME_ROOT", "RUN_ARGS", "MAKEFLAGS", "MFLAGS"):
            self.env.pop(key, None)

    def run_make(self, *options):
        return subprocess.run(["make", "-f", str(self.makefile), "-o", "build", "run", *options],
                              cwd=self.checkout, env=self.env, capture_output=True, text=True, timeout=10)

    def captured(self):
        return json.loads((self.checkout / "captured.json").read_text())

    def test_plain_run_follows_linked_home_and_keeps_browser_enabled(self):
        runtime = self.root / "live runtime"
        (runtime / "config").mkdir(parents=True)
        (self.checkout / ".agent-host").symlink_to(runtime, target_is_directory=True)
        result = self.run_make()
        self.assertEqual(result.returncode, 0, result.stderr)
        args = self.captured()
        self.assertEqual(args[args.index("--config-dir") + 1], str(runtime / "config"))
        self.assertNotIn("--no-browser", args)
        self.assertIn("--restart", args)

    def test_explicit_config_link_and_browser_opt_out_are_preserved(self):
        config = self.root / "selected runtime/config"
        config.mkdir(parents=True)
        alias = self.root / "selected alias"
        alias.symlink_to(config.parent, target_is_directory=True)
        result = self.run_make("CONFIG_DIR=" + str(alias / "config"), "RUN_ARGS=--no-browser")
        self.assertEqual(result.returncode, 0, result.stderr)
        args = self.captured()
        self.assertEqual(args[args.index("--config-dir") + 1], str(config))
        self.assertIn("--no-browser", args)

    def test_missing_config_does_not_invoke_host_or_create_a_runtime(self):
        result = self.run_make()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.checkout / "captured.json").exists())
        self.assertFalse((self.checkout / ".agent-host").exists())

    def test_explicit_legacy_runtime_keeps_its_existing_selection(self):
        runtime = self.root / "legacy runtime"
        result = self.run_make("RUNTIME_ROOT=" + str(runtime))
        self.assertEqual(result.returncode, 0, result.stderr)
        args = self.captured()
        self.assertEqual(args[args.index("--root") + 1], str(runtime))
        self.assertNotIn("--config-dir", args)
        self.assertNotIn("--no-browser", args)


class ProjectRunTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory).resolve() / "runtime"
        (root / "config").mkdir(parents=True)
        (root / "config/service.json").write_text("{}")
        viewer = Path(directory).resolve() / "viewer-source"
        viewer.mkdir()
        for name in RUN.VIEWER_FILES:
            (viewer / name).write_text("fixture")
        binary = Path(directory).resolve() / "host"
        binary.write_text("#!/usr/bin/env python3\nimport signal,sys,time\n"
                          "if sys.argv[2]=='check': sys.exit(0)\n"
                          "signal.signal(signal.SIGTERM,lambda *_:sys.exit(1))\n"
                          "print('fixture.receiver.ready',flush=True)\n"
                          "while True: time.sleep(.05)\n")
        binary.chmod(0o700)
        return root, viewer, binary

    def test_missing_config_does_not_create_runtime(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve() / "absent"
            with self.assertRaisesRegex(ValueError, "missing runtime"):
                RUN.prepare(root, root / "bin", root / "viewer")
            self.assertFalse(root.exists())

    def test_prepare_preserves_state_and_copies_private_viewer(self):
        with tempfile.TemporaryDirectory() as directory:
            root, viewer, binary = self.fixture(directory)
            (root / "state").mkdir()
            evidence = root / "state/evidence"
            evidence.write_text("retained")
            RUN.prepare(root, binary, viewer)
            self.assertEqual(evidence.read_text(), "retained")
            for name in RUN.VIEWER_FILES:
                target = root / "viewer" / name
                self.assertEqual(target.read_text(), "fixture")
                self.assertEqual(target.stat().st_mode & 0o077, 0)
            self.assertEqual((root / "native").stat().st_mode & 0o077, 0)

    def test_foreground_log_and_signal_stop_child(self):
        with tempfile.TemporaryDirectory() as directory:
            root, viewer, binary = self.fixture(directory)
            runner = subprocess.Popen([sys.executable, str(Path(__file__).with_name("run.py")),
                                       "--root", str(root), "--binary", str(binary),
                                       "--viewer-source", str(viewer), "--no-browser"],
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    log = root / "logs/service.ndjson"
                    if log.exists() and "fixture.receiver.ready" in log.read_text():
                        break
                    if runner.poll() is not None:
                        self.fail("runner exited before startup")
                    time.sleep(.05)
                else:
                    self.fail("runner did not relay child output")
                child = int((root / "service.pid").read_text())
                duplicate = subprocess.run([sys.executable, str(Path(__file__).with_name("run.py")),
                                            "--root", str(root), "--binary", str(binary),
                                            "--viewer-source", str(viewer), "--no-browser"],
                                           capture_output=True, text=True, timeout=5)
                self.assertNotEqual(duplicate.returncode, 0)
                self.assertIn("already running", duplicate.stderr)
                self.assertEqual(int((root / "service.pid").read_text()), child)
                runner.send_signal(signal.SIGTERM)
                stdout, stderr = runner.communicate(timeout=5)
                self.assertEqual(runner.returncode, 0, stderr)
                self.assertIn("fixture.receiver.ready", stdout)
                self.assertFalse((root / "service.pid").exists())
                with self.assertRaises(ProcessLookupError):
                    os.kill(child, 0)
                self.assertEqual(log.stat().st_mode & 0o077, 0)
            finally:
                if runner.poll() is None:
                    runner.kill()
                    runner.wait()
                runner.stdout.close()
                runner.stderr.close()

    def test_symlink_viewer_rejected_before_launch(self):
        with tempfile.TemporaryDirectory() as directory:
            root, viewer, binary = self.fixture(directory)
            (root / "viewer").mkdir()
            (root / "viewer/server.py").symlink_to(viewer / "server.py")
            with self.assertRaisesRegex(ValueError, "regular"):
                RUN.prepare(root, binary, viewer)


if __name__ == "__main__":
    unittest.main()
