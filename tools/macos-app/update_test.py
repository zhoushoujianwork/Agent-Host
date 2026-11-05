#!/usr/bin/env python3
"""Headless updater tests: fake bundles, repositories, clock and process runner only."""
import argparse
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import plistlib
import shutil
import signal
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent))
import build  # noqa: E402
import update  # noqa: E402

A, B, C = "a" * 40, "b" * 40, "c" * 40
NOW = 1_800_000_000.0


class FinderEnvironment(unittest.TestCase):
    def test_dependency_lookup_and_execution_share_a_tool_path_without_shell_setup(self):
        with patch.dict(os.environ, {"PATH": "/usr/bin:/bin"}), patch("shutil.which", return_value="/opt/homebrew/bin/go") as which:
            self.assertEqual(update.find_tool("go"), "/opt/homebrew/bin/go")
            lookup_path = which.call_args.kwargs["path"]
            with patch.object(update.subprocess, "run") as run:
                run.return_value.returncode = 0
                run.return_value.stdout = b""
                run.return_value.stderr = b""
                self.assertEqual(update.default_runner(["go", "version"]).returncode, 0)
                self.assertEqual(run.call_args.kwargs["env"]["PATH"], lookup_path)
            self.assertIn("/opt/homebrew/bin", lookup_path.split(os.pathsep))


def metadata(version="1.0.0", revision=A, channel="stable", **extra):
    base = {"version": version, "revision": revision, "channel": channel, "dirty": False, "architecture": "arm64",
            "min_os": "13.0", "repository": update.OFFICIAL_REPO, "development_ref": "main"}
    base.update(extra)
    return base


def make_bundle(path, meta, viewer=("--home", "/state"), signed=True):
    path = Path(path)
    (path / "Contents/MacOS").mkdir(parents=True)
    (path / "Contents/Resources/bin").mkdir(parents=True)
    (path / "Contents/MacOS/Agent-Host").write_text("binary-" + meta["revision"])
    (path / "Contents/Resources/bin/agent-host").write_text("host")
    with (path / "Contents/Info.plist").open("wb") as handle:
        plistlib.dump({"CFBundleIdentifier": update.BUNDLE_ID, "CFBundleExecutable": "Agent-Host", "AGHBuildMetadata": meta,
                       "AGHViewerArguments": list(viewer), "AGHSigned": signed}, handle)
    return path


class FakeSystem:
    """Scripts git, make, build.py, codesign, ditto, ps, open and the clock."""

    def __init__(self, directory):
        self.directory = Path(directory)
        self.calls = []
        self.tags = {}
        self.heads = {}
        self.ls_error = None
        self.fetch_sha = None
        self.build_meta = None
        self.build_fail = False
        self.host_override = None
        self.processes = {}
        self.exit_owner_after = None
        self.owner_pid = 4000
        self.launch_pid = 5000
        self.health = "ok"
        self.kill_works = True
        self.launch_fail = False
        self.killed = []
        self.clock = 0.0
        self.sleeps = 0
        self.tools = {"git", "make", "go", "swiftc", "codesign"}
        self.cache = None
        self.token = None
        self.install = None
        self.viewer = ("--home", "/state")

    # --- clock
    def monotonic(self):
        return self.clock

    def sleep(self, seconds):
        self.clock += max(seconds, 1.0)
        self.sleeps += 1
        if self.exit_owner_after is not None and self.sleeps >= self.exit_owner_after:
            self.processes.pop(self.owner_pid, None)

    def kill(self, pid, sig):
        self.killed.append((pid, sig))
        if self.kill_works:
            self.processes.pop(pid, None)

    def context(self, **overrides):
        values = dict(runner=self.run, which=lambda name: "/usr/bin/" + name if name in self.tools else None, sleep=self.sleep,
                      monotonic=self.monotonic, wall=lambda: NOW, kill=self.kill, machine="arm64", mac_version=lambda: "14.2",
                      python="/usr/bin/python3", poll=1.0, owner_wait=20.0, health_wait=20.0, term_wait=5.0)
        values.update(overrides)
        return update.Context(**values)

    # --- process runner
    def run(self, argv, cwd=None, timeout=60):
        self.calls.append(list(argv))
        R = update.Result
        program = os.path.basename(argv[0])
        if program == "git":
            return self.git(argv, R)
        if program == "make":
            return R(0, "", "", False)
        if program == "python3" or argv[0] == "/usr/bin/python3":
            return self.build_app(argv, R)
        if program == "codesign":
            info = plistlib.loads((Path(argv[-1]) / "Contents/Info.plist").read_bytes())
            return R(0 if info.get("AGHSigned", True) else 1, "", "invalid signature", False)
        if program == "agent-host":
            return self.host_version(argv, R)
        if program == "ditto":
            shutil.copytree(argv[1], argv[2], symlinks=True)
            return R(0, "", "", False)
        if program == "ps":
            return self.ps(argv, R)
        if program == "open":
            return self.open(argv, R)
        raise AssertionError("unexpected command " + " ".join(argv))

    def git(self, argv, R):
        if "ls-remote" in argv:
            if self.ls_error:
                return R(128, "", self.ls_error, False)
            pattern = argv[-1]
            lines = []
            if pattern == "refs/tags/v*":
                lines = ["%s\trefs/tags/%s" % (sha, name) for name, sha in self.tags.items()]
            elif pattern.startswith("refs/heads/") and pattern[11:] in self.heads:
                lines = ["%s\t%s" % (self.heads[pattern[11:]], pattern)]
            return R(0, "\n".join(lines) + "\n", "", False)
        if "init" in argv:
            Path(argv[-1]).mkdir(parents=True)
        if "rev-parse" in argv:
            if "FETCH_HEAD^{commit}" in argv:
                return R(0, self.fetch_sha + "\n", "", False)
            return R(0, self.fetch_sha + "\n", "", False)
        return R(0, "", "", False)

    def build_app(self, argv, R):
        if self.build_fail:
            return R(1, "", "boom", False)
        flags = argv[argv.index("--output"):]
        values = dict(zip(flags[::2], flags[1::2]))
        meta = self.build_meta or metadata(values["--version"], values["--revision"], values["--channel"])
        make_bundle(values["--output"], meta, viewer=self.viewer_from(flags))
        return R(0, "", "", False)

    def viewer_from(self, flags):
        index = flags.index("--revision") + 2
        return tuple(flags[index:])

    def host_version(self, argv, R):
        info = plistlib.loads((Path(argv[0]).parents[2] / "Info.plist").read_bytes())["AGHBuildMetadata"]
        if self.host_override:
            return R(0, self.host_override, "", False)
        return R(0, "agent-host %s (commit %s, dirty=false, darwin/arm64, go1.22)\n" % (info["version"], info["revision"]), "", False)

    def ps(self, argv, R):
        if "-p" in argv:
            pid = int(argv[argv.index("-p") + 1])
            if pid not in self.processes:
                return R(1, "", "", False)
            return R(0, "%s %s\n" % self.processes[pid], "", False)
        return R(0, "".join("%d %s %s\n" % ((pid,) + value) for pid, value in self.processes.items()), "", False)

    def open(self, argv, R):
        if self.launch_fail:
            return R(1, "", "no", False)
        if "--agh-update-health" in argv:
            token = argv[-1]
            executable = os.path.realpath(argv[argv.index("-n") + 1]) + "/Contents/MacOS/Agent-Host"
            self.processes[self.launch_pid] = ("S", executable)
            if self.health != "never_started":
                private_write(self.cache / ("started-%s.json" % token), {"pid": self.launch_pid})
            if self.health == "ok":
                revision = plistlib.loads((Path(argv[argv.index("-n") + 1]) / "Contents/Info.plist").read_bytes())["AGHBuildMetadata"]["revision"]
                private_write(self.cache / ("health-%s.json" % token), {"pid": self.launch_pid, "revision": revision})
        else:
            self.processes[self.launch_pid + 1] = ("S", os.path.realpath(argv[-1]) + "/Contents/MacOS/Agent-Host")
        return R(0, "", "", False)


def private_write(path, value):
    path.write_text(json.dumps(value))
    os.chmod(path, 0o600)


class Base(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="agh-update-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(os.path.realpath(self.temporary.name))
        self.system = FakeSystem(self.root)
        self.ctx = self.system.context()
        (self.root / "apps").mkdir()
        self.install = make_bundle(self.root / "apps/Agent-Host.app", metadata())
        self.system.install = self.install
        self.cache = update.ensure_cache(self.ctx, self.root / "cache")
        self.system.cache = self.cache

    def installed(self, **meta):
        shutil.rmtree(self.install)
        self.install = make_bundle(self.install, metadata(**meta))


class MetadataAndCheck(Base):
    def test_metadata_validation_is_strict(self):
        update.validate_metadata(metadata())
        for bad in (dict(metadata(), extra=1), {k: v for k, v in metadata().items() if k != "min_os"},
                    metadata(version="1.0"), metadata(revision="abc"), metadata(dirty="no"), metadata(channel="beta"),
                    metadata(development_ref="../x")):
            with self.assertRaises(update.UpdateError):
                update.validate_metadata(bad)

    def test_stable_selection_excludes_prereleases_and_orders_numerically(self):
        self.system.tags = {"v1.9.0": A, "v1.10.0": B, "v2.0.0-beta.1": C, "v1.11": C, "v01.2.3": C, "vx": C}
        self.assertEqual(update.latest_stable(self.ctx)["version"], "1.10.0")

    def test_annotated_tag_uses_peeled_commit(self):
        self.system.tags = {"v1.2.0": C, "v1.2.0^{}": B}
        self.assertEqual(update.latest_stable(self.ctx)["revision"], B)

    def test_stable_available_up_to_date_and_no_published_version(self):
        self.assertEqual(update.check(self.ctx, self.install)["status"], "no_published_version")
        self.system.tags = {"v2.0.0-rc.1": B}
        self.assertEqual(update.check(self.ctx, self.install)["status"], "no_published_version")
        self.system.tags = {"v1.0.0": A}
        self.assertEqual(update.check(self.ctx, self.install)["status"], "up_to_date")
        self.system.tags = {"v1.0.1": B}
        result = update.check(self.ctx, self.install)
        self.assertEqual((result["status"], result["target"]["revision"], result["automatic_allowed"]), ("available", B, True))
        self.assertTrue(result["ok"])

    def test_check_does_not_clone_or_modify(self):
        self.system.tags = {"v1.0.1": B}
        before = update.tree_digest(self.install)
        update.check(self.ctx, self.install)
        self.assertEqual(before, update.tree_digest(self.install))
        self.assertTrue(all("ls-remote" in call for call in self.system.calls))

    def test_offline_and_remote_failures_are_explicit(self):
        self.system.ls_error = "fatal: unable to access 'https://user:tok@github.com/': Could not resolve host"
        result = update.check(self.ctx, self.install)
        self.assertEqual((result["status"], result["ok"]), ("offline", False))
        self.assertNotIn("tok", json.dumps(result))
        self.system.ls_error = "fatal: repository not found"
        self.assertEqual(update.check(self.ctx, self.install)["status"], "remote_error")

    def test_incompatible_and_unupdatable_bundles_fail_closed(self):
        cases = [(dict(repository="https://example.com/x.git"), "incompatible"), (dict(architecture="x86_64"), "incompatible"),
                 (dict(min_os="15.0"), "incompatible"), (dict(revision="unknown"), "incompatible"), (dict(dirty=True), "not_updatable")]
        for meta, status in cases:
            self.installed(**meta)
            result = update.check(self.ctx, self.install)
            self.assertEqual((result["status"], result["ok"]), (status, False), meta)
        shutil.rmtree(self.install)
        (self.install / "Contents").mkdir(parents=True)
        with (self.install / "Contents/Info.plist").open("wb") as handle:
            plistlib.dump({}, handle)
        self.assertEqual(update.check(self.ctx, self.install)["status"], "incompatible")
        self.assertEqual(update.check(self.ctx, self.root / "missing.app")["status"], "invalid_bundle")

    def test_official_url_forms(self):
        for url in (update.OFFICIAL_REPO, "https://github.com/zhoushoujianwork/Agent-Host", "git@github.com:zhoushoujianwork/Agent-Host.git"):
            self.assertTrue(update.is_official_url(url))
        for url in ("https://github.com/other/Agent-Host.git", "https://evil.com/zhoushoujianwork/Agent-Host.git", ""):
            self.assertFalse(update.is_official_url(url))

    def test_dev_new_revision_is_available_but_needs_consent(self):
        self.installed(version="dev-aaaaaaaaaaaa", channel="dev")
        self.system.heads = {"main": A}
        self.assertEqual(update.check(self.ctx, self.install)["status"], "up_to_date")
        self.system.heads = {"main": B}
        result = update.check(self.ctx, self.install)
        self.assertEqual((result["status"], result["automatic_allowed"], result["requires_consent"]), ("available", False, True))
        self.system.heads = {}
        self.assertEqual(update.check(self.ctx, self.install)["status"], "remote_error")


class Prepare(Base):
    def setUp(self):
        super().setUp()
        self.system.tags = {"v1.1.0": B}
        self.system.fetch_sha = B

    def test_prepare_builds_pinned_candidate_without_touching_install(self):
        before = update.tree_digest(self.install)
        result = update.prepare(self.ctx, self.install, self.cache)
        self.assertEqual(result["status"], "prepared", result)
        candidate = result["candidate"]
        self.assertEqual((candidate["version"], candidate["revision"], candidate["channel"]), ("1.1.0", B, "stable"))
        self.assertTrue(Path(candidate["path"]).is_dir())
        self.assertEqual(candidate["content_digest"], update.tree_digest(Path(candidate["path"])))
        manifest = Path(result["cache_manifest"]["path"])
        self.assertEqual(hashlib.sha256(manifest.read_bytes()).hexdigest(), result["cache_manifest"]["digest"])
        self.assertEqual(before, update.tree_digest(self.install))
        self.assertFalse((Path(candidate["path"]).parent / "src").exists())
        build_call = next(call for call in self.system.calls if "--output" in call)
        self.assertEqual(build_call[-2:], ["--home", "/state"])
        self.assertIn("VERSION=1.1.0", next(call for call in self.system.calls if call[0] == "make"))
        fetch = next(call for call in self.system.calls if "fetch" in call)
        self.assertIn("refs/tags/v1.1.0", fetch)
        self.assertIn(update.OFFICIAL_REPO, next(call for call in self.system.calls if "remote" in call))

    def test_nothing_to_prepare_when_up_to_date(self):
        self.system.tags = {"v1.0.0": A}
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "up_to_date")
        self.assertFalse(any("fetch" in call for call in self.system.calls))

    def test_moved_ref_is_rejected_and_staging_removed(self):
        self.system.fetch_sha = C
        result = update.prepare(self.ctx, self.install, self.cache)
        self.assertEqual((result["status"], result["ok"]), ("ref_moved", False))
        self.assertEqual(list((self.cache / "staging").iterdir()), [])
        result = update.prepare(self.ctx, self.install, self.cache, expect_revision=A)
        self.assertEqual(result["status"], "ref_moved")

    def test_dev_requires_consent_flag(self):
        self.installed(version="dev-aaaaaaaaaaaa", channel="dev")
        self.system.heads = {"main": B}
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "consent_required")
        result = update.prepare(self.ctx, self.install, self.cache, allow_dev=True)
        self.assertEqual((result["status"], result["candidate"]["channel"]), ("prepared", "dev"))
        self.assertEqual(result["candidate"]["version"], "dev-" + "b" * 12)

    def test_dev_rejects_a_target_that_does_not_contain_installed_revision(self):
        self.installed(version="dev-aaaaaaaaaaaa", channel="dev")
        self.system.heads = {"main": B}
        original = self.ctx.runner
        def divergent(argv, **kwargs):
            if "merge-base" in argv:
                return update.Result(1, "", "", False)
            return original(argv, **kwargs)
        self.ctx.runner = divergent
        before = update.tree_digest(self.install)
        result = update.prepare(self.ctx, self.install, self.cache, allow_dev=True)
        self.assertEqual(result["status"], "unsupported_upgrade")
        self.assertEqual(update.tree_digest(self.install), before)

    def test_missing_dependency_lists_names(self):
        self.system.tools.discard("swiftc")
        result = update.prepare(self.ctx, self.install, self.cache)
        self.assertEqual((result["status"], result["missing"]), ("missing_dependencies", ["swiftc"]))

    def test_build_failure_and_metadata_mismatch_are_rejected(self):
        self.system.build_fail = True
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "build_failed")
        self.system.build_fail = False
        self.system.build_meta = metadata("1.1.0", C)
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "invalid_candidate")
        self.system.build_meta = None
        self.system.host_override = "agent-host 0.2.0-dev (commit %s, dirty=false, darwin/arm64, go)\n" % B
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "invalid_candidate")
        self.assertEqual(list((self.cache / "staging").iterdir()), [])

    def test_fetch_failure_reports_offline(self):
        original = self.system.git

        def failing(argv, R):
            if "fetch" in argv:
                return R(128, "", "fatal: unable to access: Could not resolve host", False)
            return original(argv, R)

        self.system.git = failing
        self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "offline")

    def test_concurrent_updater_is_refused(self):
        with update.updater_lock(self.cache):
            self.assertEqual(update.prepare(self.ctx, self.install, self.cache)["status"], "locked")


class Apply(Base):
    def prepare_candidate(self, tag="v1.1.0", **overrides):
        self.system.tags = {tag: B}
        self.system.fetch_sha = B
        result = update.prepare(self.ctx, self.install, self.cache, allow_dev=True)
        self.assertEqual(result["status"], "prepared", result)
        return result

    def handoff(self, prepared, **overrides):
        data = {"schema": 1, "install_bundle": str(self.install), "owner_pid": self.system.owner_pid, "timestamp": NOW,
                "health_token": "tok_" + "x" * 20,
                "candidate": dict(prepared["candidate"], manifest_path=prepared["cache_manifest"]["path"],
                                  manifest_digest=prepared["cache_manifest"]["digest"])}
        data.update(overrides)
        path = self.cache / "handoff.json"
        path.write_text(json.dumps(data))
        os.chmod(path, 0o600)
        self.system.token = data["health_token"]
        return path

    def run_apply(self, path):
        self.system.processes = {self.system.owner_pid: ("S", str(self.install / "Contents/MacOS/Agent-Host"))}
        return update.apply_update(self.ctx, self.install, self.cache, path)

    def revision(self):
        return update.read_manifest(self.install)["revision"]

    def siblings(self):
        return sorted(item.name for item in self.install.parent.iterdir())

    def test_successful_update_keeps_previous_and_relaunches_with_token(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 2
        result = self.run_apply(path)
        self.assertEqual(result["status"], "updated", result)
        self.assertEqual(self.revision(), B)
        previous = Path(result["previous_bundle"])
        self.assertEqual(update.read_manifest(previous)["revision"], A)
        self.assertIn(["open", "-g", "-n", str(self.install), "--args", "--agh-update-health", "tok_" + "x" * 20], self.system.calls)
        self.assertEqual(json.loads((self.cache / "transaction.json").read_text())["state"], "committed")
        self.assertEqual(json.loads((self.cache / ("apply-result-tok_" + "x" * 20 + ".json")).read_text())["status"], "updated")
        self.assertEqual(self.system.killed, [])

    def test_waits_for_owner_exit_and_times_out_without_changes(self):
        path = self.handoff(self.prepare_candidate())
        result = self.run_apply(path)
        self.assertEqual(result["status"], "owner_timeout")
        self.assertEqual(self.revision(), A)
        self.assertEqual(self.siblings(), ["Agent-Host.app"])
        ready = json.loads((self.cache / ("ready-" + self.system.token + ".json")).read_text())
        self.assertEqual(ready["owner_pid"], self.system.owner_pid)
        self.assertEqual(ready["revision"], B)

    def test_candidate_prepared_for_a_different_install_is_rejected(self):
        prepared = self.prepare_candidate()
        self.installed(version="1.0.1", revision=C)
        path = self.handoff(prepared)
        result = self.run_apply(path)
        self.assertEqual(result["status"], "invalid_candidate")
        self.assertEqual(self.revision(), C)
        self.assertFalse((self.cache / ("ready-" + self.system.token + ".json")).exists())

    def test_owner_pid_must_be_the_installed_app(self):
        path = self.handoff(self.prepare_candidate())
        self.system.processes = {self.system.owner_pid: ("S", "/bin/sleep")}
        self.assertEqual(update.apply_update(self.ctx, self.install, self.cache, path)["status"], "owner_mismatch")

    def test_other_running_instance_blocks(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        original = self.system.ps

        def with_extra(argv, R):
            result = original(argv, R)
            if "-axo" in argv:
                return R(0, result.stdout + "777 S %s\n" % (self.install / "Contents/MacOS/Agent-Host"), "", False)
            return result

        self.system.ps = with_extra
        self.assertEqual(self.run_apply(path)["status"], "other_instances_running")
        self.assertEqual(self.revision(), A)

    def test_health_timeout_terminates_candidate_and_rolls_back(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        self.system.health = "started_only"
        result = self.run_apply(path)
        self.assertEqual(result["status"], "rolled_back", result)
        self.assertEqual(result["reason"], "health_timeout")
        self.assertEqual(self.revision(), A)
        self.assertEqual(self.system.killed, [(5000, signal.SIGTERM)])
        self.assertTrue(result["previous_relaunched"])
        self.assertTrue(any(name.startswith(".Agent-Host.failed-") for name in self.siblings()))
        self.assertEqual(json.loads((self.cache / "transaction.json").read_text())["state"], "rolled_back")

    def test_unreliable_process_identity_requires_manual_recovery_and_kills_nothing(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        self.system.health = "never_started"
        result = self.run_apply(path)
        # The candidate process is visible but has no start marker: it is not killed, and rollback is refused.
        self.assertEqual(result["status"], "manual_recovery_required", result)
        self.assertEqual(self.system.killed, [])
        self.assertEqual(self.revision(), B)
        self.assertTrue(any(name.startswith(".Agent-Host.previous-") for name in self.siblings()))
        self.assertEqual(json.loads((self.cache / "transaction.json").read_text())["state"], "manual_recovery")

    def test_term_that_does_not_stop_candidate_requires_manual_recovery(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        self.system.health = "started_only"
        self.system.kill_works = False
        result = self.run_apply(path)
        self.assertEqual(result["status"], "manual_recovery_required")
        self.assertEqual(self.revision(), B)

    def test_health_from_wrong_process_is_not_accepted(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        original = self.system.open

        def wrong(argv, R):
            result = original(argv, R)
            if "--agh-update-health" in argv:
                private_write(self.cache / ("health-%s.json" % argv[-1]), {"pid": 9999, "revision": B})
            return result

        self.system.open = wrong
        result = self.run_apply(path)
        self.assertEqual((result["status"], result["reason"]), ("rolled_back", "health_invalid"))
        self.assertEqual(self.revision(), A)

    def test_launch_failure_rolls_back(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        original = self.system.open

        def flaky(argv, R):
            if "--agh-update-health" in argv:
                return R(1, "", "no", False)
            return original(argv, R)

        self.system.open = flaky
        result = self.run_apply(path)
        self.assertEqual((result["status"], result["reason"]), ("rolled_back", "launch_failed"))
        self.assertEqual(self.revision(), A)

    def test_switch_failure_restores_install(self):
        path = self.handoff(self.prepare_candidate())
        self.system.exit_owner_after = 1
        real_rename = os.rename
        names = update.candidate_names(self.install, "tok_" + "x" * 20)

        def rename(source, target):
            if str(target) == str(self.install) and str(source) == str(names["new"]):
                raise OSError("denied")
            return real_rename(source, target)

        original = update.os.rename
        update.os.rename = rename
        try:
            result = self.run_apply(path)
        finally:
            update.os.rename = original
        self.assertEqual(result["status"], "switch_failed")
        self.assertEqual(self.revision(), A)
        self.assertEqual(self.siblings(), ["Agent-Host.app"])

    def test_dev_requires_explicit_allow_dev_in_handoff(self):
        self.installed(version="dev-aaaaaaaaaaaa", channel="dev")
        self.system.heads = {"main": B}
        self.system.tags = {}
        self.system.fetch_sha = B
        prepared = update.prepare(self.ctx, self.install, self.cache, allow_dev=True)
        path = self.handoff(prepared)
        self.assertEqual(self.run_apply(path)["status"], "consent_required")
        self.assertEqual(self.revision(), A)
        path = self.handoff(prepared, allow_dev=True)
        self.system.exit_owner_after = 1
        self.assertEqual(self.run_apply(path)["status"], "updated")

    def test_handoff_validation_fails_closed(self):
        prepared = self.prepare_candidate()
        good = self.handoff(prepared)
        data = json.loads(good.read_text())
        cases = {"stale": dict(timestamp=NOW - 1000), "future": dict(timestamp=NOW + 1000), "bad_token": dict(health_token="../x"),
                 "short_token": dict(health_token="abc"), "pid": dict(owner_pid=1), "bool_pid": dict(owner_pid=True),
                 "extra": dict(unknown=1), "dev_type": dict(allow_dev="yes"), "other_install": dict(install_bundle=str(self.root / "x.app"))}
        for name, change in cases.items():
            path = self.handoff(prepared, **change)
            self.system.exit_owner_after = 1
            result = self.run_apply(path)
            self.assertFalse(result["ok"], name)
            self.assertEqual(self.revision(), A, name)
        path = self.handoff(prepared)
        os.chmod(path, 0o644)
        self.assertEqual(update.apply_update(self.ctx, self.install, self.cache, path)["status"], "invalid_handoff")
        outside = self.root / "outside.json"
        outside.write_text(json.dumps(data))
        os.chmod(outside, 0o600)
        self.assertEqual(update.apply_update(self.ctx, self.install, self.cache, outside)["status"], "invalid_handoff")
        link = self.cache / "link.json"
        link.symlink_to(good)
        self.assertEqual(update.apply_update(self.ctx, self.install, self.cache, link)["status"], "invalid_handoff")
        self.assertEqual(self.revision(), A)

    def test_candidate_pin_mismatches_are_rejected(self):
        prepared = self.prepare_candidate()
        for key, value in (("content_digest", "0" * 64), ("revision", C), ("version", "9.9.9")):
            changed = json.loads(json.dumps(prepared))
            changed["candidate"][key] = value
            self.system.exit_owner_after = 1
            self.assertEqual(self.run_apply(self.handoff(changed))["status"], "invalid_candidate", key)
        changed = json.loads(json.dumps(prepared))
        changed["cache_manifest"]["digest"] = "0" * 64
        self.assertEqual(self.run_apply(self.handoff(changed))["status"], "invalid_candidate")
        self.assertEqual(self.revision(), A)

    def test_tampered_candidate_content_and_signature_are_rejected(self):
        prepared = self.prepare_candidate()
        app = Path(prepared["candidate"]["path"])
        (app / "Contents/Resources/bin/agent-host").write_text("tampered")
        self.assertEqual(self.run_apply(self.handoff(prepared))["status"], "invalid_candidate")
        (app / "Contents/Resources/bin/agent-host").write_text("host")
        (app / "Contents/Resources/link").symlink_to("/etc/hosts")
        self.assertEqual(self.run_apply(self.handoff(prepared))["status"], "invalid_candidate")
        self.assertEqual(self.revision(), A)

    def test_candidate_outside_cache_is_rejected(self):
        prepared = self.prepare_candidate()
        outside = make_bundle(self.root / "Other.app", metadata("1.1.0", B))
        prepared["candidate"]["path"] = str(outside)
        self.assertEqual(self.run_apply(self.handoff(prepared))["status"], "invalid_candidate")

    def test_symlink_install_is_rejected(self):
        prepared = self.prepare_candidate()
        link = self.root / "apps/Link.app"
        link.symlink_to(self.install)
        path = self.handoff(prepared, install_bundle=str(link))
        self.system.processes = {self.system.owner_pid: ("S", str(self.install / "Contents/MacOS/Agent-Host"))}
        self.assertEqual(update.apply_update(self.ctx, link, self.cache, path)["status"], "invalid_install")

    def test_token_reuse_is_refused(self):
        path = self.handoff(self.prepare_candidate())
        private_write(self.cache / ("started-tok_" + "x" * 20 + ".json"), {"pid": 1234})
        self.system.exit_owner_after = 1
        self.assertEqual(self.run_apply(path)["status"], "invalid_handoff")

    def test_no_secrets_in_results(self):
        self.system.ls_error = "fatal: https://user:hunter2@github.com/x unable to access"
        self.assertNotIn("hunter2", json.dumps(update.check(self.ctx, self.install)))

    def test_interrupted_transaction_blocks_apply_and_recover_restores(self):
        prepared = self.prepare_candidate()
        token = "tok_" + "x" * 20
        names = update.candidate_names(self.install, token)
        os.rename(self.install, names["previous"])
        make_bundle(self.install, metadata("1.1.0", B))
        update.write_transaction(self.cache, "switching", token=token, install=str(self.install), old_revision=A, new_revision=B, started=NOW)
        self.assertEqual(self.run_apply(self.handoff(prepared))["status"], "interrupted_transaction")
        self.system.processes = {}
        result = update.recover(self.ctx, self.install, self.cache)
        self.assertEqual(result["status"], "recovered", result)
        self.assertEqual(self.revision(), A)
        self.assertEqual(update.recover(self.ctx, self.install, self.cache)["status"], "nothing_to_recover")

    def test_recover_refuses_while_candidate_runs(self):
        token = "tok_" + "x" * 20
        names = update.candidate_names(self.install, token)
        os.rename(self.install, names["previous"])
        make_bundle(self.install, metadata("1.1.0", B))
        update.write_transaction(self.cache, "launched", token=token, install=str(self.install), old_revision=A, new_revision=B, started=NOW)
        self.system.processes = {5000: ("S", str(self.install / "Contents/MacOS/Agent-Host"))}
        result = update.recover(self.ctx, self.install, self.cache)
        self.assertEqual(result["status"], "manual_recovery_required")
        self.assertEqual(self.revision(), B)
        self.assertTrue(os.path.lexists(names["previous"]))

    def test_recover_rejects_foreign_transaction(self):
        update.write_transaction(self.cache, "switching", token="tok_" + "x" * 20, install=str(self.root / "other.app"))
        self.assertEqual(update.recover(self.ctx, self.install, self.cache)["status"], "invalid_transaction")


class CommandLine(Base):
    def run_main(self, *args):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = update.main(["--bundle", str(self.install), "--cache", str(self.cache)] + list(args), self.ctx)
        return code, json.loads(out.getvalue())

    def test_json_output_and_exit_codes(self):
        self.system.tags = {"v1.0.1": B}
        code, result = self.run_main("check")
        self.assertEqual((code, result["status"]), (0, "available"))
        self.system.ls_error = "Could not resolve host"
        code, result = self.run_main("check")
        self.assertEqual((code, result["status"]), (1, "offline"))
        code, result = self.run_main("recover")
        self.assertEqual((code, result["status"]), (0, "nothing_to_recover"))

    def test_relative_bundle_and_bad_revision_are_rejected(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(update.main(["--bundle", "rel.app", "check"], self.ctx), 1)
            self.assertEqual(update.main(["--bundle", str(self.install), "--cache", str(self.cache), "prepare", "--revision", "main"], self.ctx), 1)

    def test_cache_must_be_private(self):
        shared = self.root / "shared"
        shared.mkdir()
        os.chmod(shared, 0o755)
        with self.assertRaises(update.UpdateError):
            update.ensure_cache(self.ctx, shared)
        with self.assertRaises(update.UpdateError):
            update.ensure_cache(self.ctx, "relative")


class BuildMetadata(unittest.TestCase):
    def options(self, **values):
        base = dict(output=None, version=None, channel=None, revision=None)
        base.update(values)
        return argparse.Namespace(**base)

    def state(self, **values):
        base = dict(revision=A, dirty=False, stable_tags=[], repository=update.OFFICIAL_REPO)
        base.update(values)
        return base

    def host(self, version="0.2.0-dev", revision=A, dirty="false"):
        return {"version": version, "revision": revision, "dirty": dirty}

    def test_viewer_pairs_are_preserved_with_updater_flags(self):
        options, arguments = build.parse_arguments(["--output", "/tmp/agh-x-%d/A.app" % os.getpid(), "--version", "1.2.3", "--channel", "stable",
                                                    "--revision", A, "--home", "/h", "--channel-home", "x=/y"])
        self.assertEqual(arguments, ["--home", "/h", "--channel-home", "x=/y"])
        self.assertEqual((options.version, options.channel), ("1.2.3", "stable"))
        self.assertEqual(build.parse_arguments(["--home", "/h"])[1], ["--home", "/h"])
        # No viewer options selects the app-owned runtime; the updater preserves the empty list.
        self.assertEqual(build.parse_arguments([])[1], [])

    def test_invalid_arguments_exit(self):
        for argv in (["--home", "rel"], ["--version", "1.0.0", "--home", "/h"], ["--channel", "stable", "--version", "1.0", "--home", "/h"],
                     ["--output", "rel.app", "--home", "/h"], ["--revision", "main", "--home", "/h"], ["--bogus", "1", "--home", "/h"],
                     ["--home", "/h", "--agent"], ["--agent", "x"]):
            with self.assertRaises(SystemExit, msg=argv):
                build.parse_arguments(argv)

    def test_default_metadata_clean_exact_tag_is_stable(self):
        meta = build.build_metadata(self.options(), self.state(stable_tags=["1.0.0"]), self.host("1.0.0"))
        self.assertEqual((meta["channel"], meta["version"], meta["dirty"]), ("stable", "1.0.0", False))
        self.assertEqual(set(meta), update.METADATA_KEYS)
        update.validate_metadata(meta)

    def test_default_metadata_without_tag_or_when_dirty_is_dev(self):
        meta = build.build_metadata(self.options(), self.state(), self.host())
        self.assertEqual((meta["channel"], meta["version"]), ("dev", "0.2.0-dev"))
        meta = build.build_metadata(self.options(), self.state(stable_tags=["1.0.0"], dirty=True), self.host("1.0.0", dirty="true"))
        self.assertEqual((meta["channel"], meta["dirty"]), ("dev", True))
        # A tag the Host was not built with never becomes stable.
        meta = build.build_metadata(self.options(), self.state(stable_tags=["1.0.0"]), self.host())
        self.assertEqual(meta["channel"], "dev")

    def test_mismatches_exit(self):
        with self.assertRaises(SystemExit):
            build.build_metadata(self.options(channel="stable", version="1.0.0"), self.state(), self.host("0.2.0-dev"))
        with self.assertRaises(SystemExit):
            build.build_metadata(self.options(), self.state(), self.host(revision=B))
        with self.assertRaises(SystemExit):
            build.build_metadata(self.options(revision=B), self.state(), self.host(revision=B))
        with self.assertRaises(SystemExit):
            build.build_metadata(self.options(channel="stable", version="1.0.0"), self.state(dirty=True), self.host("1.0.0", dirty="true"))
        with self.assertRaises(SystemExit):
            build.build_metadata(self.options(), self.state(dirty=True), self.host(dirty="false"))

    def test_source_state_from_fake_git(self):
        def run(argv, **kwargs):
            command = argv[3:]
            out = {("rev-parse", "HEAD"): A + "\n", ("status", "--porcelain"): "", ("tag", "--points-at", "HEAD"): "v1.0.0\nv1.0.0-rc.1\nfoo\n",
                   ("remote", "get-url", "origin"): "https://u:p@github.com/x/y.git\n"}[tuple(command)]
            return argparse.Namespace(returncode=0, stdout=out)

        state = build.source_state(Path("/nonexistent"), run=run)
        self.assertEqual((state["revision"], state["dirty"], state["stable_tags"]), (A, False, ["1.0.0"]))
        self.assertNotIn("p@", state["repository"])
        state = build.source_state(Path("/nonexistent"), run=lambda *a, **k: argparse.Namespace(returncode=128, stdout=""))
        self.assertEqual((state["revision"], state["dirty"]), ("unknown", True))

    def test_tree_digest_rejects_special_files_and_detects_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "a").write_text("1")
            first = update.tree_digest(root)
            (root / "a").write_text("2")
            self.assertNotEqual(first, update.tree_digest(root))
            (root / "l").symlink_to("a")
            with self.assertRaises(update.UpdateError):
                update.tree_digest(root)


if __name__ == "__main__":
    unittest.main()
