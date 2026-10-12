#!/usr/bin/env python3
"""Validate the already selected live DWS source in private acceptance state.

This runner never selects another conversation, sends a message, or touches
production state. Raw CLI output and source evidence stay under --private-root.
Only the bounded structural report is written to --output and standard output.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import stat
import subprocess
import sys
import tempfile
from urllib.parse import quote


class AcceptanceFailure(Exception):
    pass


def require(condition, code):
    if not condition:
        raise AcceptanceFailure(code)


def private_json(path, code):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink(), code)
    require(info.st_uid == os.getuid() and info.st_mode & 0o077 == 0, code)
    require(info.st_size <= 8 * 1024 * 1024, code)
    with path.open("rb") as stream:
        opened = os.fstat(stream.fileno())
        require((opened.st_dev, opened.st_ino) == (info.st_dev, info.st_ino), code)
        return json.load(stream)


def write_private(path, data):
    if not isinstance(data, bytes):
        data = (json.dumps(data, indent=2, ensure_ascii=False) + "\n").encode()
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)


def source_report(report):
    require(isinstance(report, dict) and len(report.get("sources", [])) == 1,
            "expected_one_selected_source_report")
    return report["sources"][0]


def inspect_state(home):
    database = home / "state.db"
    require(database.is_file(), "acceptance_database_missing")
    uri = "file:" + quote(str(database), safe="/") + "?mode=ro"
    with sqlite3.connect(uri, uri=True) as connection:
        counts = {table: connection.execute("SELECT COUNT(*) FROM " + table).fetchone()[0]
                  for table in ("sessions", "runs", "memories", "candidates", "channel_messages")}
        sources = [json.loads(row[0]) for row in connection.execute(
            "SELECT payload FROM discovery_sources ORDER BY id")]
        evidence = [json.loads(row[0]) for row in connection.execute(
            "SELECT payload FROM discovery_evidence ORDER BY id")]
        collections = [json.loads(row[0]) for row in connection.execute(
            "SELECT payload FROM discovery_collections ORDER BY id")]
        return {"side_effect_counts": counts, "sources": sources,
                "evidence": evidence, "collections": collections,
                "collection_count": len(collections)}


def no_business_effects(snapshot):
    require(all(count == 0 for count in snapshot["side_effect_counts"].values()),
            "discovery_created_business_or_memory_state")


def exact_evidence(evidence, selection, profile, project_id, source_id=None):
    tenant, principal = profile.split(":", 1)
    require(bool(evidence), "cited_evidence_missing")
    for item in evidence:
        require(item.get("profile") == profile and item.get("tenant_id") == tenant
                and item.get("principal_id") == principal
                and item.get("conversation_id") == selection["conversation_id"]
                and item.get("project_id") == project_id
                and bool(item.get("author_id")) and bool(item.get("message_id"))
                and item.get("availability") == "available",
                "evidence_attribution_mismatch")
        if source_id:
            require(item.get("source_id") == source_id, "evidence_source_mismatch")


class Runner:
    def __init__(self, binary, environment, private_directory, timeout):
        self.binary = binary
        self.environment = environment
        self.directory = private_directory
        self.timeout = timeout

    def cli(self, label, arguments, allow_failure=False):
        # Unknown Host commands pass through RTK unchanged. Never invoke a shell or
        # print argv, provider stderr, model output, identities, or source text.
        completed = subprocess.run(["rtk", str(self.binary), *arguments],
                                   env=self.environment, capture_output=True,
                                   timeout=self.timeout, check=False)
        write_private(self.directory / (label + ".json"), completed.stdout)
        write_private(self.directory / (label + ".stderr"), completed.stderr)
        require(len(completed.stdout) <= 8 * 1024 * 1024
                and len(completed.stderr) <= 1024 * 1024, "cli_output_limit_exceeded")
        require(allow_failure or completed.returncode == 0, label + "_command_failed")
        try:
            result = json.loads(completed.stdout)
        except (ValueError, UnicodeError):
            raise AcceptanceFailure(label + "_invalid_json") from None
        return result, completed.returncode


def validate_export(export, snapshot, selection, profile, project_id, disposition):
    proposal = export.get("proposal", {})
    require(export.get("disposition") == disposition
            and proposal.get("project_id") == project_id
            and proposal.get("audience") == "owner-private", "topic_export_boundary_mismatch")
    evidence = export.get("evidence", [])
    exact_evidence(evidence, selection, profile, project_id, proposal.get("source_id"))
    by_id = {item["id"]: item for item in snapshot["evidence"]}
    references = proposal.get("draft", {}).get("evidence_ids", [])
    require(references and set(references) == {item["id"] for item in evidence},
            "topic_citation_set_mismatch")
    for item in evidence:
        require(item["id"] in by_id and item == by_id[item["id"]],
                "topic_citation_not_original_snapshot")
    draft = proposal.get("draft", {})
    require(bool(draft.get("title")) and bool(draft.get("summary")),
            "native_topic_missing_substantive_fields")
    return len(evidence)


def collect(runner, label, home, config, claude, allow_failure=False):
    report, code = runner.cli(label, ["discovery", "collect", "--home", str(home),
                                   "--config", str(config), "--claude", claude], allow_failure)
    return source_report(report), code


def run(args, structural):
    private_root = Path(args.private_root)
    production = Path(args.production)
    binary = Path(args.binary)
    output = Path(args.output)
    require(all(path.is_absolute() for path in (private_root, production, binary, output)),
            "absolute_paths_required")
    require(not output.exists() and not output.is_symlink(), "structural_report_already_exists")
    root_info = private_root.lstat()
    require(stat.S_ISDIR(root_info.st_mode) and not private_root.is_symlink()
            and root_info.st_uid == os.getuid() and root_info.st_mode & 0o077 == 0,
            "private_root_permissions_invalid")
    require(binary.is_file() and os.access(binary, os.X_OK), "host_binary_unavailable")
    output = Path(args.output)
    require(output.is_absolute() and production != output
            and production not in output.parents, "report_must_be_outside_production")
    structural["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
    context = private_json(private_root / "context.json", "private_context_invalid")
    require(Path(context["root"]) == private_root, "private_context_root_mismatch")
    config_path = Path(context["discovery_config"])
    require(config_path.parent == private_root, "selection_config_outside_private_root")
    config = private_json(config_path, "selection_config_invalid")
    project_id = context["project_id"]
    require(len(config["sources"]) == 1, "selection_must_be_single_conversation")
    selection = config["sources"][0]
    profile = context["target"]["profile"]
    require(config["profile"] == profile and selection["conversation_id"] == context["conversation_id"]
            and selection["peer_open_dingtalk_id"] == context["target"]["open_dingtalk_id"]
            and selection["type"] == "dm" and selection["project_id"] == project_id,
            "approved_source_selection_changed")
    service = private_json(production / "config" / "service.json", "production_service_invalid")
    environment = private_json(production / "config" / "environment.json", "production_environment_invalid")
    require(isinstance(environment, dict) and all(isinstance(key, str) and isinstance(value, str)
            for key, value in environment.items()), "production_environment_shape_invalid")
    require(config["model"] == service["model"], "discovery_model_differs_from_production")
    claude = service["claude"]
    require(Path(claude).is_absolute() and os.access(claude, os.X_OK), "native_executable_unavailable")
    child_environment = os.environ.copy()
    child_environment.update(environment)
    child_environment["CLAUDE_CONFIG_DIR"] = str(private_root / "native")
    child_environment["PYTHONDONTWRITEBYTECODE"] = "1"
    raw_directory = Path(tempfile.mkdtemp(prefix="discovery-validation-", dir=private_root))
    runner = Runner(binary, child_environment, raw_directory, 3 * config["timeout_seconds"] + 60)
    main_home = private_root / "discovery-state"
    first = source_report(private_json(private_root / "collection-1.json", "first_collection_proof_invalid"))
    require(first.get("complete") and first.get("messages", 0) >= 4
            and first.get("added_topics", 0) >= 1
            and first.get("target_verification") == "exact_conversation_verified_in_messages",
            "initial_live_collection_proof_failed")
    # Prefer the already saved true overlap proof, before later authorized
    # platform test messages can change source revisions. No clock is altered.
    repeat_path = next((private_root / name for name in ("collection-3.json", "collection-2.json")
                        if (private_root / name).is_file()), None)
    if repeat_path:
        repeated = source_report(private_json(repeat_path, "overlap_proof_invalid"))
    else:
        repeated, _ = collect(runner, "main_repeat", main_home, config_path, claude)
    require(repeated.get("complete") and repeated.get("added_evidence") == 0
            and repeated.get("added_topics") == 0, "reopened_overlap_not_deduplicated")
    before = inspect_state(main_home)
    write_private(raw_directory / "main-before.json", before)
    no_business_effects(before)
    require(len(before["sources"]) == 1, "main_source_count_changed")
    exact_evidence(before["evidence"], selection, profile, project_id)
    project, _ = runner.cli("main_project", ["project", "show", "--home", str(main_home), "--id", project_id])
    topics, _ = runner.cli("main_topics", ["discovery", "topics", "--home", str(main_home), "--status", "all", "--limit", "100"])
    require(bool(topics), "initial_native_topic_missing")
    topic = next((item for item in topics if item.get("status") == "pending"), topics[0])
    review, _ = runner.cli("main_review", ["discovery", "show", "--home", str(main_home), topic["id"]])
    cited_count = validate_export(review, before, selection, profile, project_id, "review_only")
    if topic["status"] == "pending":
        accepted, _ = runner.cli("main_accept", ["discovery", "accept", "--home", str(main_home),
                                               "--project", project_id, topic["id"]])
        validate_export(accepted, before, selection, profile, project_id, "accepted_for_owner_private_handoff_only")
        require(accepted["proposal"]["status"] == "accepted", "topic_acceptance_not_persisted")
    else:
        require(topic["status"] == "accepted", "no_acceptable_native_topic")
    after = inspect_state(main_home)
    write_private(raw_directory / "main-after.json", after)
    no_business_effects(after)
    require(before["side_effect_counts"] == after["side_effect_counts"], "owner_acceptance_side_effects")
    structural.update({"initial_messages": first["messages"], "native_topics": len(topics),
                       "reopened_overlap_messages": repeated["messages"],
                       "reopened_overlap_added_evidence": 0, "reopened_overlap_added_topics": 0,
                       "review_before_acceptance": True, "exact_original_citations": True,
                       "accepted_evidence_count": cited_count, "owner_private_handoff_only": True,
                       "acceptance_reused": topic["status"] == "accepted"})

    partial_home = raw_directory / "partial-state"
    partial_home.mkdir(mode=0o700)
    partial_config = dict(config, page_limit=1, max_messages=3, model=service["model"])
    partial_path = raw_directory / "partial-config.json"
    write_private(partial_path, partial_config)
    runner.cli("partial_project", ["project", "add", "--home", str(partial_home), "--id", project_id,
                                   "--workspace", project["workspace"], "--name", "Discovery acceptance"])
    capped, capped_code = collect(runner, "partial_first", partial_home, partial_path, claude, True)
    partial_before = inspect_state(partial_home)
    write_private(raw_directory / "partial-before.json", partial_before)
    no_business_effects(partial_before)
    require(not capped.get("complete") and 0 < len(partial_before["evidence"]) <= 3
            and capped.get("added_topics") == 0, "bounded_partial_source_not_observed")
    exact_evidence(partial_before["evidence"], selection, profile, project_id)
    require(len(partial_before["sources"]) == 1, "partial_source_count_changed")
    checkpoint = partial_before["sources"][0]["checkpoint"]
    require(checkpoint.get("complete_through", "").startswith("0001-")
            and checkpoint.get("pending_from") == capped["from"]
            and checkpoint.get("pending_to") == capped["to"], "partial_checkpoint_was_promoted")
    failed_ledger = bool(partial_before["collections"][-1].get("error"))
    restarted, restart_code = collect(runner, "partial_restart", partial_home, partial_path, claude, True)
    partial_repeated = inspect_state(partial_home)
    write_private(raw_directory / "partial-repeated.json", partial_repeated)
    no_business_effects(partial_repeated)
    require(not restarted.get("complete") and restarted["from"] == capped["from"]
            and restarted["to"] == capped["to"] and restart_code != 0, "partial_restart_not_explicit")
    if failed_ledger:
        require(not restarted.get("blocked") and restarted.get("added_evidence") == 0
                and partial_repeated["collection_count"] == partial_before["collection_count"] + 1
                and bool(partial_repeated["collections"][-1].get("error")), "failure_ledger_retry_not_preserved")
        restart_behavior = "explicit_same_range_failure_retry_deduplicated"
    else:
        require(restarted.get("blocked") and partial_repeated["collection_count"] == partial_before["collection_count"],
                "same_caps_truncation_was_replayed")
        restart_behavior = "unchanged_caps_blocked_without_provider_replay"
    require({item["id"] for item in partial_before["evidence"]}
            == {item["id"] for item in partial_repeated["evidence"]}, "partial_retry_changed_evidence")
    raised_config = dict(partial_config, page_limit=10, max_messages=200)
    raised_path = raw_directory / "raised-config.json"
    write_private(raised_path, raised_config)
    recovered, _ = collect(runner, "partial_recovered", partial_home, raised_path, claude)
    recovered_state = inspect_state(partial_home)
    write_private(raw_directory / "partial-recovered-state.json", recovered_state)
    no_business_effects(recovered_state)
    exact_evidence(recovered_state["evidence"], selection, profile, project_id)
    retained_ids = {item["id"] for item in partial_before["evidence"]}
    recovered_ids = {item["id"] for item in recovered_state["evidence"]}
    require(recovered.get("complete") and recovered.get("messages", 0) >= 4
            and recovered.get("added_topics", 0) > 0 and retained_ids <= recovered_ids
            and recovered.get("added_evidence") == len(recovered_ids - retained_ids),
            "raised_caps_did_not_complete_deduplicated_source")
    require(recovered_state["sources"][0]["checkpoint"]["complete_through"] == capped["to"],
            "recovery_did_not_complete_original_pending_range")
    recovered_topics, _ = runner.cli("partial_topics", ["discovery", "topics", "--home", str(partial_home),
                                                       "--status", "pending", "--limit", "100"])
    require(bool(recovered_topics), "real_recovery_native_topic_missing")
    for index, item in enumerate(recovered_topics):
        exported, _ = runner.cli("partial_review_" + str(index), ["discovery", "show", "--home", str(partial_home), item["id"]])
        validate_export(exported, recovered_state, selection, profile, project_id, "review_only")
    structural.update({"partial_messages_retained": len(retained_ids),
                       "partial_pages": partial_before["collections"][-1]["pages_fetched"],
                       "partial_provider_exit_nonzero": capped_code != 0, "partial_restart_behavior": restart_behavior,
                       "partial_pending_range_preserved": True, "recovered_messages": recovered["messages"],
                       "recovery_added_evidence": recovered["added_evidence"], "recovery_duplicate_revisions": 0,
                       "recovery_native_topics": len(recovered_topics), "original_pending_range_completed": True,
                       "business_state_counts": after["side_effect_counts"], "memory_published": False,
                       "messages_sent": 0, "production_state_modified": False,
                       "unverified": ["group_noise_quality", "sustained_polling", "global_account_coverage"]})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("private-root", "production", "binary", "output"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    structural = {"report_version": 1, "date": datetime.datetime.now(datetime.timezone.utc).date().isoformat(),
                  "method": "real_dws_selected_private_source_native_discovery_acceptance", "passed": False}
    exit_code = 0
    try:
        run(args, structural)
        structural["passed"] = True
    except AcceptanceFailure as error:
        structural["failure"] = str(error)
        exit_code = 1
    except subprocess.TimeoutExpired:
        structural["failure"] = "bounded_cli_timeout"
        exit_code = 1
    except Exception:
        # Unanticipated setup/schema errors must not expose private paths,
        # configuration values, account metadata, or retained source content.
        structural["failure"] = "private_acceptance_setup_or_inspection_failed"
        exit_code = 1
    output = Path(args.output)
    data = (json.dumps(structural, indent=2) + "\n").encode()
    try:
        require(output.is_absolute(), "absolute_report_path_required")
        write_private(output, data)
    except (OSError, AcceptanceFailure):
        print(json.dumps({"passed": False, "failure": "structural_report_write_failed"}))
        return 1
    print(data.decode(), end="")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
