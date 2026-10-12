#!/usr/bin/env python3
"""Exercise local package identity, inclusion boundaries and clean-Git checks."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("release_package", HERE / "package.py")
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)
ROOT = HERE.parents[1]


def run(args, root):
    return subprocess.run(args, cwd=root, check=True, capture_output=True, text=True).stdout


class PackageTests(unittest.TestCase):
    def fixture(self):
        temporary = tempfile.TemporaryDirectory(prefix="agent-host-package-test-")
        self.addCleanup(temporary.cleanup)
        base = Path(temporary.name)
        root = base / "source"
        root.mkdir()
        for name in package.REQUIRED_FILES:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("Public package fixture.\n")
        (root / ".gitignore").write_text("/dist/\n/.agent-host/\n/production/\n")
        (root / "go.mod").write_text("module agent-host\n\ngo 1.26.0\n")
        (root / "docs/beta-release.md").write_text(
            "See [installation](service-install.md) and [audiences](agent-instances.md).\n")
        (root / "docs/agent-instances.md").write_text("Additional public guide fixture.\n")
        info = root / "internal/buildinfo/buildinfo.go"
        info.parent.mkdir(parents=True)
        info.write_bytes((ROOT / "internal/buildinfo/buildinfo.go").read_bytes())
        main = root / "cmd/agent-host/main.go"
        main.parent.mkdir(parents=True)
        main.write_text('''package main
import ("fmt"; "agent-host/internal/buildinfo")
func main() {
    i := buildinfo.Current()
    fmt.Printf("agent-host %s (commit %s, dirty=%t)\\n", i.Version, i.Revision, i.Modified)
}
''')
        # Even tracked private-looking files must stay outside the package.
        (root / "config/private.json").write_text("PRIVATE-CONFIG-FIXTURE")
        private_doc = root / "docs/private-investigation.md"
        private_doc.write_text("PRIVATE-DOCUMENT-FIXTURE")
        (root / "experiments").mkdir()
        (root / "experiments/private-report.json").write_text("PRIVATE-REPORT-FIXTURE")
        run(["git", "init", "-q"], root)
        run(["git", "config", "user.name", "Package Fixture"], root)
        run(["git", "config", "user.email", "package@example.invalid"], root)
        paths = [str(p.relative_to(root)) for p in root.rglob("*") if p.is_file() and ".git" not in p.parts]
        run(["git", "add", "--", *paths], root)
        run(["git", "commit", "-qm", "Create public package fixture"], root)
        for name in [".agent-host/state.db", "production/config/environment.json", "dist/prior.log"]:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("PRIVATE-RUNTIME-FIXTURE")
        return root, base / "artifacts"

    @unittest.skipUnless(sys.platform == "darwin", "native macOS archive acceptance")
    def test_native_archive_identity_checksums_and_public_files(self):
        root, output = self.fixture()
        result = package.create_package(root, output, "0.2.0-beta.1")
        revision = run(["git", "rev-parse", "HEAD"], root).strip()
        manifest = json.loads(Path(result["manifest"]).read_text())
        self.assertEqual(manifest["version"], "0.2.0-beta.1")
        self.assertEqual(manifest["revision"], revision)
        self.assertFalse(manifest["modified"])
        self.assertEqual(manifest["goos"], "darwin")
        with tarfile.open(result["archive"], "r:gz") as archive:
            names = archive.getnames()
            top = names[0].split("/")[0]
            self.assertIn(top + "/bin/agent-host", names)
            self.assertIn(top + "/licenses/go/LICENSE", names)
            self.assertIn(top + "/tools/run-viewer/memory.css", names)
            self.assertIn(top + "/internal/knowledge/memgov-LICENSE", names)
            for hidden in ["config/private.json", "private-investigation", "experiments", ".agent-host", "production", "prior.log", ".git/"]:
                self.assertFalse(any(hidden in name for name in names), hidden)
            stored = json.load(archive.extractfile(top + "/manifest.json"))
            self.assertEqual(stored, manifest)
            readme = archive.extractfile(top + "/README.md").read().decode()
            self.assertIn("not a source checkout", readme)
            guide = archive.extractfile(top + "/docs/beta-release.md").read().decode()
            self.assertIn("](service-install.md)", guide)
            self.assertIn(package.PUBLIC_REPOSITORY + "/blob/" + revision + "/docs/agent-instances.md", guide)
            self.assertNotIn(top + "/docs/agent-instances.md", names)
            for record in manifest["files"]:
                data = archive.extractfile(top + "/" + record["path"]).read()
                self.assertEqual(package.sha256(data), record["sha256"])
                self.assertNotIn(b"PRIVATE-", data)
            self.assertEqual(archive.getmember(top + "/bin/agent-host").mode, 0o755)
        for line in Path(result["checksums"]).read_text().splitlines():
            digest, name = line.split("  ", 1)
            self.assertEqual(package.sha256((output / name).read_bytes()), digest)
        with self.assertRaisesRegex(ValueError, "already exists"):
            package.create_package(root, output, "0.2.0-beta.1")

    def test_requires_explicit_version_clean_git_and_committed_guides(self):
        root, output = self.fixture()
        with patch.object(package.sys, "platform", "darwin"):
            for version in ["", "../escape", "0.2.0;command"]:
                with self.subTest(version=version), self.assertRaisesRegex(ValueError, "explicit version"):
                    package.create_package(root, output, version)
            (root / "README.md").write_text("Modified fixture.\n")
            with self.assertRaisesRegex(ValueError, "clean Git"):
                package.create_package(root, output, "0.2.0-beta.1")
            run(["git", "checkout", "--", "README.md"], root)
            run(["git", "rm", "-q", "--", "docs/feedback.md"], root)
            run(["git", "commit", "-qm", "Remove required fixture guide"], root)
            with self.assertRaisesRegex(ValueError, "missing committed package files"):
                package.create_package(root, output, "0.2.0-beta.1")
        self.assertFalse(output.exists())

    def test_dependency_notices_are_exact_and_missing_licenses_fail(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        module = root / "module"
        module.mkdir()
        goroot = root / "go"
        goroot.mkdir()
        (goroot / "LICENSE").write_bytes(b"Go runtime license\n")
        (module / "LICENSE").write_bytes(b"Dependency license\n")
        (module / "NOTICE").write_bytes(b"Upstream notice\n")
        (module / "private-config.json").write_bytes(b"PRIVATE-FIXTURE")
        objects = json.dumps({"Path": "main", "Main": True}) + json.dumps({
            "Path": "example.invalid/dependency", "Version": "v1.2.3", "Dir": str(module),
        })
        with patch.object(package, "command", return_value=objects):
            files, notices = package.module_licenses(root, goroot)
            prefix = "licenses/example.invalid/dependency@v1.2.3/"
            self.assertEqual(files[prefix + "LICENSE"], b"Dependency license\n")
            self.assertEqual(files[prefix + "NOTICE"], b"Upstream notice\n")
            self.assertEqual(files["licenses/go/LICENSE"], b"Go runtime license\n")
            self.assertEqual(notices[0]["files"], [prefix + "LICENSE", prefix + "NOTICE"])
            self.assertEqual(len(files), 3)
            (module / "LICENSE").unlink()
            (module / "NOTICE").unlink()
            with self.assertRaisesRegex(ValueError, "example.invalid/dependency@v1.2.3"):
                package.module_licenses(root, goroot)

    def test_build_without_correct_commit_is_rejected(self):
        with patch.object(package, "command", side_effect=["", "fixture:\tgo1.26\n\tbuild\tvcs.revision=wrong\n\tbuild\tvcs.modified=false\n"]):
            with self.assertRaisesRegex(ValueError, "clean selected Git revision"):
                package.build_binary(HERE, Path("/unused/fixture"), "0.2.0-beta.1", "correct", "darwin", "arm64")

    def test_homebrew_runtime_license_layout_is_explicit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            goroot = root / "Cellar/go/1.26.fixture/libexec"
            goroot.mkdir(parents=True)
            (goroot.parent / "LICENSE").write_text("Go license fixture")
            self.assertEqual(package.runtime_license_path(goroot), goroot.parent / "LICENSE")
            other = root / "unrelated/libexec"
            other.mkdir(parents=True)
            (other.parent / "LICENSE").write_text("Unrelated license fixture")
            with self.assertRaisesRegex(ValueError, "Go runtime LICENSE"):
                package.runtime_license_path(other)


if __name__ == "__main__":
    unittest.main()
