#!/usr/bin/env python3
"""Build a local, source-derived macOS bundle; never install or restart services."""
import hashlib
import json
from pathlib import Path
import plistlib
import platform
import shutil
import subprocess
import sys
import tempfile


def main():
    root = Path(__file__).resolve().parents[2]
    arguments = sys.argv[1:]
    if not arguments or not any(flag in arguments for flag in ("--home", "--channel-home")):
        sys.exit("Usage: python3 tools/macos-app/build.py --home /absolute/state [viewer options]\n"
                 "Select explicit development or existing Host state; this does not start channel services.")
    # Accept explicit options supported by the bundled viewer without starting it.
    allowed = {"--home", "--channel-home", "--backup-root", "--chat-config", "--agent", "--audience", "--project", "--workspace"}
    for offset in range(0, len(arguments), 2):
        if arguments[offset] not in allowed or offset + 1 >= len(arguments):
            sys.exit("Expected explicit viewer option/value pairs; unsupported option: " + arguments[offset])
        if arguments[offset] in {"--home", "--backup-root", "--chat-config"} and not Path(arguments[offset + 1]).is_absolute():
            sys.exit("State/config paths must be absolute")
        if arguments[offset] == "--channel-home":
            name, sep, path = arguments[offset + 1].partition("=")
            if not name or not sep or not Path(path).is_absolute():
                sys.exit("Channel sources require NAME=/absolute/state")
    bundle = root / "dist/Agent-Host.app/Contents"
    binary = bundle / "MacOS/Agent-Host"
    resources = bundle / "Resources"
    binary.parent.mkdir(parents=True, exist_ok=True)
    resources.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="agh-app-build-") as cache:
        subprocess.run(["swiftc", "-target", platform.machine() + "-apple-macos13.0", "-module-cache-path", cache, "-swift-version", "5", "-O", str(root / "tools/macos-app/TaskState.swift"),
                        str(root / "tools/macos-app/main.swift"), "-o", str(binary)], check=True)
    shutil.copytree(root / "tools/run-viewer", resources / "tools/run-viewer", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__", "*_test.*", "fixtures"))
    shutil.copytree(root / "skills/agent-host-tasks", resources / "skills/agent-host-tasks", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__", "*_test.*"))
    (resources / "bin").mkdir(exist_ok=True)
    shutil.copy2(root / "bin/agent-host", resources / "bin/agent-host")
    journal_id = hashlib.sha256(json.dumps(arguments).encode()).hexdigest()[:16]
    with (bundle / "Info.plist").open("wb") as target:
        plistlib.dump({"CFBundleIdentifier": "local.agent-host.workspace", "CFBundleName": "Agent-Host",
                      "CFBundleExecutable": "Agent-Host", "CFBundlePackageType": "APPL",
                      "CFBundleShortVersionString": "0.1.0", "LSMinimumSystemVersion": "13.0",
                      "NSHighResolutionCapable": True, "AGHPython": sys.executable,
                      "NSAppTransportSecurity": {"NSAllowsLocalNetworking": True},
                      "AGHViewerArguments": arguments, "AGHJournalID": journal_id}, target)
    subprocess.run(["codesign", "--force", "--sign", "-", str(bundle.parent)], check=True)
    print(bundle.parent)


if __name__ == "__main__":
    main()
