#!/usr/bin/env python3
import importlib.util
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
