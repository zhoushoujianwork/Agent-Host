#!/usr/bin/env python3
"""Validate file memory on isolated, read-only SQLite history snapshots.

Reports contain counts and hashes, never memory text, query text or CLI output.
Copied databases and authoritative memory documents remain private evidence.
"""

import argparse
import collections
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys


TABLES = ("memories", "memory_revisions", "memory_evidence", "candidates")
HOME_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$")
REPOSITORY = Path(__file__).resolve().parents[2]


def digest(value):
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(raw).hexdigest()


def private_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
    path.chmod(0o600)


def cell(value):
    if isinstance(value, bytes):
        return {"blob_hex": value.hex()}
    return value


def capture_tables(path):
    """Capture exact SQL cells plus normalized semantic payloads for rollback."""
    exact, semantic, columns = {}, {}, {}
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
        for table in TABLES:
            fields = [row[1] for row in db.execute(f"PRAGMA table_info({table})")]
            if not fields:
                raise RuntimeError("history snapshot is missing a required table")
            rows = [list(row) for row in db.execute(f"SELECT * FROM {table}")]
            rows.sort(key=lambda row: json.dumps([cell(value) for value in row], sort_keys=True))
            columns[table] = fields
            exact[table] = [[cell(value) for value in row] for row in rows]
            semantic[table] = [dict(zip(fields, row)) for row in rows]
    owners, projections = {}, {}
    for record in semantic["memories"]:
        owners[record["id"]] = {
            "agent_id": record.get("agent_id") or "default",
            "audience_id": record.get("audience_id") or "owner",
            "sender_id": record.get("sender_id") or "",
        }
    for table in ("memories", "memory_revisions", "candidates"):
        for record in semantic[table]:
            payload = json.loads(record["payload"])
            owner = owners.get(record.get("memory_id", record.get("id")))
            if table == "candidates":
                owner = {"agent_id": record.get("agent_id") or "default", "audience_id": record.get("audience_id") or "owner"}
            if owner:
                payload.update(owner)
            # omitempty serialization can remove optional empty attribution.
            if not payload.get("sender_id"):
                payload.pop("sender_id", None)
            if table == "memories":
                if not isinstance(payload.get("text"), str):
                    raise RuntimeError("historical memory payload lacks authoritative text")
                identity = digest(record["id"])
                projections[identity] = {"column_sha256": digest(record["text"]), "payload_sha256": digest(payload["text"])}
                # Old experience restructuring updated the authoritative payload
                # and FTS tokens without updating this redundant relational
                # text column. Rollback recreates that column from the payload.
                # Compare the full payload and every other tuple field exactly.
                record["text"] = payload["text"]
            record["payload"] = payload
            if "agent_id" in record:
                record["agent_id"] = record["agent_id"] or "default"
            if "audience_id" in record:
                record["audience_id"] = record["audience_id"] or "owner"
    for table in TABLES:
        semantic[table].sort(key=lambda row: json.dumps(row, ensure_ascii=False, sort_keys=True))
    return {
        "exact": {table: {"columns": columns[table], "rows": exact[table]} for table in TABLES},
        "semantic": semantic,
        "counts": {table: len(exact[table]) for table in TABLES},
        "text_projections": projections,
    }


def table_hashes(snapshot, kind):
    return {table: digest(snapshot[kind][table]) for table in TABLES}


def backup_readonly(home, destination):
    """Use SQLite backup so live WAL records participate in one consistent copy."""
    source = home / "state.db"
    if not source.is_file() or source.is_symlink():
        raise RuntimeError("source home requires a regular state.db")
    if (home / "file-memory.json").exists():
        raise RuntimeError("source must use the legacy SQLite memory authority")
    destination.mkdir(mode=0o700)
    copied = destination / "state.db"
    with sqlite3.connect(source.as_uri() + "?mode=ro", uri=True) as incoming:
        with sqlite3.connect(copied) as outgoing:
            incoming.backup(outgoing)
            if outgoing.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                raise RuntimeError("isolated database integrity check failed")
    copied.chmod(0o600)
    return copied


def run_cli(binary, arguments):
    result = subprocess.run([str(binary), *arguments], cwd=REPOSITORY, capture_output=True, text=True, timeout=60)
    if result.returncode:
        # Query text and tool output deliberately stay out of errors/reports.
        raise RuntimeError(f"CLI {' '.join(arguments[:2])} failed with exit {result.returncode}")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError("CLI returned an invalid JSON receipt") from error


def history_cases(snapshot, home):
    shows, queries = {}, {}
    for record in snapshot["semantic"]["memories"]:
        memory = record["payload"]
        agent = record.get("agent_id") or memory.get("agent_id") or "default"
        audience = record.get("audience_id") or memory.get("audience_id") or "owner"
        scope = record["scope"]
        base = ["--home", str(home), "--workspace", str(REPOSITORY), "--memory-scope", scope, "--agent", agent, "--audience", audience]
        key = digest([agent, audience, scope, record["id"]])
        shows[key] = ["memory", "show", *base, record["id"]]
        # Exact historical statements supply reproducible per-scope queries.
        # Ineligible history is inspected but excluded from query construction.
        text = memory.get("text", "")
        if memory.get("status", record.get("status")) == "active" and text.strip():
            query_key = digest([agent, audience, scope, text])
            queries[query_key] = ["memory", "search", *base, text]
    return shows, queries


def run_cases(binary, cases, execute):
    return {kind: {key: execute(binary, args) for key, args in commands.items()} for kind, commands in cases.items()}


def require_equal(before, after, phase):
    if before != after:
        raise RuntimeError(f"historical comparison failed during {phase}")


def case_report(results):
    return {kind: {"count": len(items), "digest": digest(items)} for kind, items in results.items()}


def projection_corrections(before, after):
    corrections = []
    require_equal(set(before["text_projections"]), set(after["text_projections"]), "rollback memory identities")
    for identity, original in before["text_projections"].items():
        restored = after["text_projections"][identity]
        require_equal(original["payload_sha256"], restored["payload_sha256"], "rollback authoritative payload text")
        require_equal(restored["column_sha256"], restored["payload_sha256"], "rollback derived text projection")
        if original["column_sha256"] != original["payload_sha256"]:
            corrections.append({"memory_id_sha256": identity, "field": "memories.text", "before_sha256": original["column_sha256"], "after_sha256": restored["column_sha256"]})
    return corrections


def validate_history(binary, homes, output, execute=run_cli):
    if not homes or any(not HOME_NAME.fullmatch(name) for name in homes):
        raise RuntimeError("history validation requires distinct safe home names")
    output = output.absolute()
    if output.exists() or output.is_symlink():
        raise RuntimeError("output must be a new private directory")
    output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    output.mkdir(mode=0o700)
    (output / "homes").mkdir(mode=0o700)
    report = {"version": 1, "status": "running", "source_access": "read_only_sqlite_backup", "external_execution": False, "homes": {}}
    phase = "snapshot"
    try:
        for name, original_home in homes.items():
            original_home = Path(original_home).resolve(strict=True)
            isolated_home = output / "homes" / name
            database = backup_readonly(original_home, isolated_home)
            initial = capture_tables(database)
            show_commands, search_commands = history_cases(initial, isolated_home)
            cases = {"show": show_commands, "search": search_commands}
            phase = "legacy_queries"
            baseline = run_cases(binary, cases, execute)
            raw_hashes = table_hashes(initial, "exact")
            normalized_hashes = table_hashes(initial, "semantic")
            phase = "migration"
            migration_args = ["memory", "migrate", "--home", str(isolated_home), "--memory-root", str(output / "memory"), "--memory-cache", str(output / "cache" / "memory"), "--memory-source", "history-" + name]
            migrated = execute(binary, migration_args)
            repeated = execute(binary, migration_args)
            require_equal(migrated, repeated, "migration retry")
            phase = "file_queries"
            file_results = run_cases(binary, cases, execute)
            require_equal(baseline, file_results, "file memory show/search")
            require_equal(raw_hashes, table_hashes(capture_tables(database), "exact"), "legacy memory SQL isolation")
            phase = "cache_rebuild"
            shutil.rmtree(output / "cache" / "memory", ignore_errors=True)
            verified = execute(binary, ["memory", "verify", "--home", str(isolated_home)])
            require_equal(migrated, verified, "authoritative file verification")
            rebuilt_results = run_cases(binary, cases, execute)
            require_equal(baseline, rebuilt_results, "rebuilt-cache show/search")
            require_equal(raw_hashes, table_hashes(capture_tables(database), "exact"), "rebuilt-cache SQL isolation")
            phase = "rollback"
            rollback = execute(binary, ["memory", "rollback", "--home", str(isolated_home)])
            require_equal(migrated, rollback, "rollback revision/evidence receipt")
            rolled_snapshot = capture_tables(database)
            require_equal(normalized_hashes, table_hashes(rolled_snapshot, "semantic"), "rollback semantic table tuples")
            corrections = projection_corrections(initial, rolled_snapshot)
            require_equal(baseline, run_cases(binary, cases, execute), "rollback show/search")
            statuses = collections.Counter(record["payload"].get("status", record.get("status", "unknown")) for record in initial["semantic"]["memories"])
            report["homes"][name] = {
                "counts": initial["counts"], "memory_status_counts": dict(statuses),
                "legacy_table_sha256": raw_hashes, "semantic_table_sha256": normalized_hashes,
                "migration": migrated, "queries": case_report(baseline),
                "migration_retry_equal": True, "file_recall_equal": True,
                "legacy_sql_unchanged": True, "cache_rebuild_equal": True,
                "rollback_tuples_equal": True, "rollback_recall_equal": True,
                "derived_projection_corrections": corrections,
            }
        report["status"] = "passed"
    except Exception:
        report.update(status="failed", failure_phase=phase)
        private_json(output / "report.json", report)
        raise
    private_json(output / "report.json", report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--home", action="append", required=True, metavar="NAME=PATH")
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    homes = {}
    for value in arguments.home:
        name, separator, path = value.partition("=")
        if not separator or not path or name in homes or not HOME_NAME.fullmatch(name):
            parser.error("--home requires a distinct safe NAME=PATH")
        homes[name] = Path(path)
    binary = arguments.binary.resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error("--binary must be an executable file")
    try:
        report = validate_history(binary, homes, arguments.output)
    except Exception as error:
        print(f"Historical validation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
