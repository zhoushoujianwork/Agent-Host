"""Prove local detection, repeatability, and preservation without host writes."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("cli_install", Path(__file__).with_name("install.py"))
INSTALL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INSTALL)


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="agent-host-install-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo, self.home = self.root / "checkout with spaces", self.root / "home"
        self.home.mkdir()
        self.binary = self.repo / "bin" / "agent-host"
        self.binary.parent.mkdir(parents=True)
        self.binary.write_text("#!/bin/sh\nprintf 'fixture-cli\\n'\n")
        self.binary.chmod(0o755)
        self.skill = self.repo / "skills" / INSTALL.SKILL
        (self.skill / "references").mkdir(parents=True)
        (self.skill / "SKILL.md").write_text("fixture skill")
        (self.skill / "references" / "protocol.md").write_text("fixture protocol")
        self.task_skill = self.repo / "skills" / INSTALL.TASK_SKILL
        self.task_skill.mkdir()
        (self.task_skill / "SKILL.md").write_text("fixture task handoff")
        self.bin_dir = self.home / ".local" / "bin"
        self.env = {"PATH": ""}

    def install(self, agents=None, which=lambda *a, **k: None):
        return INSTALL.install(self.repo, self.home, self.env, self.bin_dir, agents, which)

    def test_detects_all_supported_configured_clients_and_repeat_install(self):
        roots = INSTALL.agent_roots(self.home, self.env)
        for root in roots.values():
            root.mkdir(parents=True)
        rows = self.install()
        self.assertTrue(all(status == "installed" for _, status, _ in rows))
        for name, root in roots.items():
            target = (self.home / ".agents" / "skills" if name == "codex" else root / "skills") / INSTALL.SKILL
            self.assertEqual(target.resolve(), self.skill.resolve())
            self.assertEqual((target / "references" / "protocol.md").read_text(), "fixture protocol")
            self.assertEqual((target.parent / INSTALL.TASK_SKILL).resolve(), self.task_skill.resolve())
        cli = self.bin_dir / "agent-host"
        result = subprocess.run([str(cli)], capture_output=True, text=True, check=True)
        self.assertEqual(result.stdout.strip(), "fixture-cli")
        self.assertTrue(all(status == "already-linked" for _, status, _ in self.install()))
        (self.skill / "SKILL.md").write_text("updated source")
        self.assertEqual((roots["claude"] / "skills" / INSTALL.SKILL / "SKILL.md").read_text(), "updated source")

    def test_absent_clients_are_skipped_without_creating_config(self):
        rows = dict((name, status) for name, status, _ in self.install())
        self.assertEqual(rows["cli"], "installed")
        for name, root in INSTALL.agent_roots(self.home, self.env).items():
            self.assertEqual(rows[name], "not-detected")
            self.assertFalse(root.exists())

    def test_older_bundle_reports_missing_task_skill_without_replacing_custom_skill(self):
        shutil.rmtree(self.task_skill)
        rows = self.install(agents={"codex"})
        self.assertEqual(dict((name, status) for name, status, _ in rows)["codex/agent-host-tasks"], "not-bundled")
        self.task_skill.mkdir()
        (self.task_skill / "SKILL.md").write_text("fixture task handoff")
        target = self.home / ".agents/skills" / INSTALL.TASK_SKILL
        target.mkdir()
        (target / "SKILL.md").write_text("custom task handoff")
        rows = self.install(agents={"codex"})
        self.assertEqual(dict((name, status) for name, status, _ in rows)["codex/agent-host-tasks"], "preserved")
        self.assertEqual((target / "SKILL.md").read_text(), "custom task handoff")

    def test_path_detection_and_explicit_agent_selection(self):
        rows = self.install(which=lambda name, **k: "/fake/claude" if name == "claude" else None)
        self.assertEqual(dict((name, status) for name, status, _ in rows)["claude"], "installed")
        rows = self.install(agents={"gemini"})
        statuses = dict((name, status) for name, status, _ in rows)
        self.assertEqual(statuses["gemini"], "installed")
        self.assertEqual(statuses["codex"], "not-selected")

    def test_custom_roots_are_respected(self):
        self.env.update({"CODEX_HOME": str(self.root / "codex-custom"),
                         "CLAUDE_CONFIG_DIR": str(self.root / "claude-custom"),
                         "XDG_CONFIG_HOME": str(self.root / "config-custom")})
        rows = self.install(agents={"codex", "claude", "opencode"})
        self.assertEqual(dict((name, status) for name, status, _ in rows)["codex"], "installed")
        self.assertTrue((self.root / "config-custom" / "opencode" / "skills" / INSTALL.SKILL).is_symlink())
        self.assertFalse((self.home / ".codex").exists())
        self.env["OPENCODE_CONFIG_DIR"] = str(self.root / "opencode-custom")
        self.install(agents={"opencode"})
        self.assertTrue((self.root / "opencode-custom" / "skills" / INSTALL.SKILL).is_symlink())

    def test_foreign_files_directories_and_broken_links_are_preserved(self):
        roots = INSTALL.agent_roots(self.home, self.env)
        targets = [self.home / ".agents" / "skills" / INSTALL.SKILL,
                   roots["claude"] / "skills" / INSTALL.SKILL,
                   roots["opencode"] / "skills" / INSTALL.SKILL]
        roots["codex"].mkdir()
        for target in targets:
            target.parent.mkdir(parents=True)
        targets[0].write_text("custom file")
        targets[1].mkdir()
        (targets[1] / "SKILL.md").write_text("custom skill")
        targets[2].symlink_to(self.root / "missing")
        self.bin_dir.mkdir(parents=True)
        cli = self.bin_dir / "agent-host"
        cli.write_text("custom binary")
        rows = dict((name, status) for name, status, _ in self.install())
        for name in ("cli", "codex", "claude", "opencode"):
            self.assertEqual(rows[name], "preserved")
        self.assertEqual(targets[0].read_text(), "custom file")
        self.assertEqual((targets[1] / "SKILL.md").read_text(), "custom skill")
        self.assertEqual(os.readlink(targets[2]), str(self.root / "missing"))
        self.assertEqual(cli.read_text(), "custom binary")

    def test_filesystem_failures_are_reported_and_other_targets_continue(self):
        (self.home / ".claude").write_text("blocked parent")
        statuses = dict((name, status) for name, status, _ in self.install(agents={"claude", "codex"}))
        self.assertEqual(statuses["claude"], "failed")
        self.assertEqual(statuses["codex"], "installed")

    def test_validation_fails_before_any_install(self):
        self.env["CODEX_HOME"] = "relative-path"
        with self.assertRaisesRegex(ValueError, "absolute"):
            self.install()
        self.assertFalse(self.bin_dir.exists())
        del self.env["CODEX_HOME"]
        self.binary.unlink()
        with self.assertRaisesRegex(ValueError, "build"):
            self.install()

    def test_main_exit_code_distinguishes_preserved_skill_and_cli_conflicts(self):
        # Patch only the install result; main's path and argument validation stay real.
        with patch.object(INSTALL, "install", return_value=[("codex", "preserved", "custom")]), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(INSTALL.main(["--no-autostart"], repo=self.repo), 0)
        with patch.object(INSTALL, "install", return_value=[("cli", "preserved", "custom")]), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(INSTALL.main(["--no-autostart"], repo=self.repo), 1)
        with patch.object(INSTALL, "install", return_value=[("claude", "failed", "error")]), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(INSTALL.main(["--no-autostart"], repo=self.repo), 1)

    def test_main_registers_configured_service_and_reports_failure(self):
        with patch.object(INSTALL, "install", return_value=[]), \
                patch("service.autostart.install", return_value=self.home / "service") as start, \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(INSTALL.main(["--config-dir", str(self.root)], repo=self.repo), 0)
            self.assertEqual(start.call_args.args[1], self.root)
        with patch.object(INSTALL, "install", return_value=[]), \
                patch("service.autostart.install", side_effect=RuntimeError("not ready")), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(INSTALL.main(["--config-dir", str(self.root)], repo=self.repo), 1)

    def test_migrated_install_retains_production_links_and_never_starts_service(self):
        production = self.root / "production"
        production.mkdir()
        (production / "bin").mkdir()
        deployed = production / "bin/agent-host"
        deployed.write_bytes(self.binary.read_bytes())
        deployed.chmod(0o700)
        (production / "skills").mkdir()
        shutil.copytree(self.skill, production / "skills" / INSTALL.SKILL)
        deployed_bytes = deployed.read_bytes()
        self.binary.write_text("#!/bin/sh\nprintf 'development-build\\n'\n")
        source = self.repo.resolve() / ".agent-host"
        source.mkdir()
        (source / "production-migration.json").write_text(json.dumps({
            "version": 1, "status": "active", "production_root": str(production.resolve())}))
        (production / "migration.json").write_text(json.dumps({
            "source_root": str(source), "target_root": str(production.resolve())}))
        output = io.StringIO()
        with patch.object(INSTALL.Path, "home", return_value=self.home), \
                patch.dict(INSTALL.os.environ, self.env, clear=True), \
                patch("service.autostart.install") as start, contextlib.redirect_stdout(output):
            self.assertEqual(INSTALL.main(["--bin-dir", str(self.bin_dir), "--agents", "codex"], repo=self.repo), 0)
            self.assertEqual(INSTALL.main(["--bin-dir", str(self.bin_dir), "--agents", "codex"], repo=self.repo), 0)
            start.assert_not_called()
        self.assertEqual((self.bin_dir / "agent-host").resolve(), deployed.resolve())
        self.assertEqual((self.home / ".agents/skills" / INSTALL.SKILL).resolve(),
                         (production / "skills" / INSTALL.SKILL).resolve())
        self.assertEqual(deployed.read_bytes(), deployed_bytes)
        self.assertNotEqual(deployed.read_bytes(), self.binary.read_bytes())
        self.assertIn("development build was not deployed", output.getvalue())

    def test_invalid_migration_receipt_fails_without_link_writes(self):
        source = self.repo / ".agent-host"
        source.mkdir()
        (source / "production-migration.json").write_text('{"version":1,"status":"active","production_root":"relative"}')
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(INSTALL.main(["--bin-dir", str(self.bin_dir)], repo=self.repo), 1)
        self.assertFalse(self.bin_dir.exists())


if __name__ == "__main__":
    unittest.main()
