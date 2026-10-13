#!/usr/bin/env python3
"""Exercise the public-copy boundary without touching the real repository."""

import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("export_public", HERE / "export_public.py")
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


def git(root, *args):
    return subprocess.run(["git", "-C", str(root), *args], check=True, capture_output=True, text=True).stdout


class PublicExportTests(unittest.TestCase):
    def fixture(self):
        temporary = tempfile.TemporaryDirectory(prefix="agent-host-public-export-test-")
        self.addCleanup(temporary.cleanup)
        base = Path(temporary.name).resolve()
        source = base / "source"
        source.mkdir()
        private_host = "company-gateway.fixture.invalid"
        platform_id = "PRIVATE-PLATFORM-FIXTURE"
        files = {
            "README.md": (
                "# Public source fixture\n"
                "[Report](experiments/mvp/results/live.json)\n"
                "[Installation][installation]\n"
                "[installation]: experiments/runtime/installation-2026.json\n"
                "[Public guide](docs/guide.md)\n"
            ),
            "AGENTS.md": "Public development instructions.\n",
            "Makefile": "check:\n\tpython3 -B experiments/history/dws_platform_test.py\n",
            "go.mod": "module agent-host\n\ngo 1.26.0\n",
            "docs/guide.md": "The source-linked tool adapter is implemented.\n",
            "docs/jev-evaluation-detail.md": (
                "# Historical gateway validation\nRuntime endpoint:\n"
                f"`https://{private_host}/type-safe/v1`.\n"
                "[Probe](../experiments/jev/results/probe.json)\n"
            ),
            "docs/service-install-detail.md": (
                "# Service details\nThe current selection is\n"
                f"`https://{private_host}/native` and `claude-fixture`.\n"
                f'{{"owner_user_id": "{platform_id}"}}\n'
            ),
            "docs/runtime-assembly-detail.md": (
                "# Runtime details\nThe owner authorized private-chat validation against\n"
                "`Fixture Bot (Fixture Owner)`.\n"
                "Fixture Owner inspected the returned source.\n"
            ),
            "experiments/mvp/README.md": (
                "# Historical experiment\n"
                f"A private company gateway at {private_host} was selected.\n"
                "python3 experiments/mvp/run.py --credentials alias\n"
                "## Recorded observations\n"
                "[Retained run](results/live.json) remains private evidence.\n"
            ),
            "evals/memory-v1.json": json.dumps({"cases": [{
                "id": "public-regression", "origin": {"kind": "incident", "reference": f"experiments/mvp/results/live.json#{platform_id}"},
                "input": "Synthetic fixture input",
            }]}),
            "internal/harness/auth.go": "package harness\n// Native authentication source is public.\n",
            "internal/harness/auth_test.go": "package harness\n",
            "config/component-environment.example.json": '{"ANTHROPIC_BASE_URL": "https://example.invalid"}\n',
            "experiments/history/dws_platform.py": "# Public platform adapter fixture.\n",
            "experiments/history/dws_platform_test.py": "# Public offline platform regression.\n",
            "tools/run-viewer/fixtures/public.json": '{"synthetic": true}\n',
            "examples/public_example.py": "# Public CLI example.\n",
            ".github/workflows/check.yml": "name: public-check\n",
        }
        hidden = [
            "experiments/mvp/results/live.json", "experiments/jev/results/probe.json",
            "evals/results/private.json", "experiments/runtime/installation-2026.json",
            "experiments/installation.json", "experiments/jev/run.py",
            "experiments/mvp/run.py", "experiments/experience/run.py",
            ".agent-host/state.db", "tools/logs/service.ndjson", "config/environment.json",
            "config/dingtalk.secret", "tools/cache/credentials.json",
        ]
        for name in hidden:
            files[name] = f"{platform_id} https://{private_host} PRIVATE-RUNTIME-FIXTURE\n"
        for name, contents in files.items():
            target = source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(contents)
        git(source, "init", "-q")
        git(source, "config", "user.name", "Export Fixture")
        git(source, "config", "user.email", "export@example.invalid")
        git(source, "add", "--", *files)
        git(source, "commit", "-qm", "Create private source fixture")
        return source, base / "public", files, hidden, private_host, platform_id

    def test_exports_public_checks_sanitizes_evidence_and_preserves_source(self):
        source, output, files, hidden, private_host, platform_id = self.fixture()
        revision = git(source, "rev-parse", "HEAD").strip()
        before = {name: hashlib.sha256((source / name).read_bytes()).digest() for name in files}
        manifest = exporter.export_public(source, output)
        self.assertEqual(manifest["source_commit"], revision)
        self.assertEqual(manifest["omitted_paths"], sorted(hidden))
        self.assertFalse((output / ".git").exists())
        for name in hidden:
            self.assertFalse((output / name).exists(), name)
        for name in ["Makefile", "internal/harness/auth.go", "config/component-environment.example.json", "experiments/history/dws_platform.py", "experiments/history/dws_platform_test.py", "tools/run-viewer/fixtures/public.json", "examples/public_example.py"]:
            self.assertEqual((output / name).read_bytes(), (source / name).read_bytes(), name)
        for path in output.rglob("*"):
            if path.is_file():
                for private in [private_host, platform_id, "Fixture Bot", "Fixture Owner", "PRIVATE-RUNTIME-FIXTURE"]:
                    self.assertNotIn(private.encode(), path.read_bytes(), str(path.relative_to(output)))
        readme = (output / "README.md").read_text()
        self.assertIn("Report (private validation evidence)", readme)
        self.assertIn("Installation (private validation evidence)", readme)
        self.assertIn("[Public guide](docs/guide.md)", readme)
        origin = json.loads((output / "evals/memory-v1.json").read_text())["cases"][0]["origin"]
        self.assertEqual(origin["kind"], "incident")
        self.assertIn("Private historical", origin["reference"])
        self.assertNotIn("results/", origin["reference"])
        self.assertEqual(json.loads((output / exporter.MANIFEST_NAME).read_text()), manifest)
        self.assertEqual(git(source, "rev-parse", "HEAD").strip(), revision)
        self.assertEqual(git(source, "status", "--porcelain"), "")
        self.assertEqual(before, {name: hashlib.sha256((source / name).read_bytes()).digest() for name in files})

    def test_rejects_dirty_source_overwrite_and_symlink_escape(self):
        source, output, _, _, _, _ = self.fixture()
        (source / "README.md").write_text("Uncommitted change.\n")
        with self.assertRaisesRegex(ValueError, "clean Git"):
            exporter.export_public(source, output)
        self.assertFalse(output.exists())
        git(source, "checkout", "--", "README.md")
        (source / "untracked.txt").write_text("Uncommitted file.\n")
        with self.assertRaisesRegex(ValueError, "clean Git"):
            exporter.export_public(source, output)
        (source / "untracked.txt").unlink()
        output.mkdir()
        with self.assertRaisesRegex(ValueError, "already exists"):
            exporter.export_public(source, output)
        output.rmdir()
        with self.assertRaisesRegex(ValueError, "outside the source"):
            exporter.export_public(source, source / "public")
        alias = output.parent / "alias"
        alias.symlink_to(output.parent, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            exporter.export_public(source, alias / "escape")
        tracked = source / "docs/escaped.md"
        tracked.symlink_to(output.parent / "outside-private.txt")
        git(source, "add", "--", "docs/escaped.md")
        git(source, "commit", "-qm", "Add rejected symlink fixture")
        with self.assertRaisesRegex(ValueError, "tracked symlinks"):
            exporter.export_public(source, output)
        self.assertFalse(output.exists())
        for name in ["../escape", "/absolute", "internal/../../escape", "internal\\escape"]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                exporter.safe_path(name)


if __name__ == "__main__":
    unittest.main()
