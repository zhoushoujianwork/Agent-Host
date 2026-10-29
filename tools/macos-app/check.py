#!/usr/bin/env python3
"""Headless model regressions and native API compilation; never launch the app."""
from pathlib import Path
import platform
import subprocess
import sys
import tempfile

if sys.platform != "darwin":
    print("macOS app checks skipped: AppKit/WebKit require macOS")
    sys.exit(0)

source = Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix="agh-macos-check-") as directory:
    root = Path(directory)
    flags = ["swiftc", "-target", platform.machine() + "-apple-macos13.0", "-module-cache-path", str(root / "cache"), "-swift-version", "5"]
    subprocess.run(flags + [str(source / "TaskState.swift"), str(source / "TaskStateTests.swift"),
                            "-o", str(root / "state-tests")], check=True)
    subprocess.run([str(root / "state-tests")], check=True)
    subprocess.run(flags + [str(source / "TaskState.swift"), str(source / "main.swift"),
                            "-o", str(root / "app-check")], check=True)
print("macOS native API compilation passed; desktop smoke acceptance remains separate")
