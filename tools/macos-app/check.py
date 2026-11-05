#!/usr/bin/env python3
"""Headless model regressions and native API compilation; never launch the app."""
from pathlib import Path
import platform
import subprocess
import sys
import tempfile

source = Path(__file__).resolve().parent
# Updater backend and build-metadata tests use fake bundles and processes; they run on any platform.
subprocess.run([sys.executable, "-B", str(source / "update_test.py")], check=True)
subprocess.run([sys.executable, "-B", str(source / "connection_test.py")], check=True)
subprocess.run([sys.executable, "-B", str(source / "runtime_test.py")], check=True)

if sys.platform != "darwin":
    print("macOS app checks skipped: AppKit/WebKit require macOS")
    sys.exit(0)

with tempfile.TemporaryDirectory(prefix="agh-macos-check-") as directory:
    root = Path(directory)
    flags = ["swiftc", "-target", platform.machine() + "-apple-macos13.0", "-module-cache-path", str(root / "cache"), "-swift-version", "5"]
    subprocess.run(flags + [str(source / "TaskState.swift"), str(source / "TaskStateTests.swift"),
                            "-o", str(root / "state-tests")], check=True)
    subprocess.run([str(root / "state-tests")], check=True)
    app_sources = [str(path) for path in sorted(source.glob("*.swift")) if not path.name.endswith("Tests.swift")]
    subprocess.run(flags + app_sources + ["-o", str(root / "app-check")], check=True)
print("macOS native API compilation passed; desktop smoke acceptance remains separate")
