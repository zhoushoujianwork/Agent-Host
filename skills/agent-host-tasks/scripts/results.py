"""Private launch receipts and read-only projections of Host execution evidence."""

import hashlib
import json
import math
import os
from pathlib import Path
import subprocess
import sqlite3
import tempfile


TOKEN_FIELDS = ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")


def native_usage(events):
    """Project terminal Claude results; streaming snapshots are not additive."""
    calls, seen = [], set()
    for event in events:
        data = event.get("data")
        raw = data.get("raw") if isinstance(data, dict) else None
        if (event.get("component") not in ("harness.claude", "harness.claude.extractor") or
                not isinstance(raw, dict) or raw.get("type") != "result"):
            continue
        # A terminal result can be mirrored in the journal. Scope identity to the
        # Host run/phase and native invocation, preserving distinct extraction calls.
        identity = (event.get("run_id"), event.get("phase"), event.get("component"),
                    raw.get("uuid") or json.dumps(raw, sort_keys=True))
        if identity in seen:
            continue
        seen.add(identity)
        reported = raw.get("usage")
        reported = reported if isinstance(reported, dict) else {}
        tokens = {key: value if type(value := reported.get(key)) is int and value >= 0 else None
                  for key in TOKEN_FIELDS}
        cost = raw.get("total_cost_usd")
        if type(cost) not in (int, float) or not math.isfinite(cost) or cost < 0:
            cost = None
        calls.append({
            "phase": event.get("phase"), "component": event.get("component"),
            "native_session_id": raw.get("session_id") or data.get("native_session_id"),
            "event_seq": event.get("seq"), "model": event.get("model") or None,
            "tokens": tokens, "cost_usd": cost,
            # Keep native per-model accounting separately: it can differ from the
            # result-level usage and must never be added to it a second time.
            "model_usage": raw.get("modelUsage") if isinstance(raw.get("modelUsage"), dict) else None,
        })
    totals = {}
    missing = {}
    for key in TOKEN_FIELDS:
        values = [call["tokens"][key] for call in calls if call["tokens"][key] is not None]
        totals[key] = sum(values) if values else None
        missing[key] = len(calls) - len(values)
    costs = [call["cost_usd"] for call in calls if call["cost_usd"] is not None]
    return {
        "source": "claude_terminal_results", "scope": "observed_native_calls_only",
        "excludes": ["jev", "other_unreported_provider_calls"],
        "tokens": totals, "calls": calls, "missing_token_calls": missing,
        "cost_basis": "native_reported_estimate" if costs else None,
        "missing_cost_calls": len(calls) - len(costs),
    }, math.fsum(costs) if costs else None


def destination_key(command, cwd):
    return hashlib.sha256(json.dumps([command, cwd]).encode()).hexdigest()


def save_json(path, value):
    # Atomic replacement lets inspection run while the launcher finishes.
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix=".receipt-")
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output, ensure_ascii=False)
            output.write("\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def new_receipt(config, command, cwd, output_dir=None):
    if output_dir:
        directory = Path(output_dir).absolute()
        directory.mkdir(mode=0o700)
    else:
        root = config.parent / "handoffs"
        root.mkdir(mode=0o700, exist_ok=True)
        directory = Path(tempfile.mkdtemp(prefix="handoff-", dir=root))
    inspect_command = [command[0], "run", "show"]
    for flag in ("--home", "--workspace", "--agent", "--memory-scope"):
        if flag in command:
            offset = command.index(flag)
            inspect_command.extend(command[offset:offset + 2])
    receipt = {
        "destination_ref": str(config), "destination_key": destination_key(command, cwd),
        "inspect_command": inspect_command, "cwd": cwd,
        "exit_code": None, "elapsed_ms": None,
    }
    if "--agents-config" in command:
        receipt["feedback_config_dir"] = str(Path(command[command.index("--agents-config") + 1]).parent)
    save_json(directory / "launch.json", receipt)
    return directory, receipt


def event_result(directory, receipt):
    result = {
        "ref": {"destination_ref": receipt["destination_ref"], "session_id": None, "run_id": None},
        "report": "", "execution": {"exit_code": receipt["exit_code"]},
        "evidence_ref": str(directory), "model": None,
        "elapsed_ms": receipt.get("elapsed_ms"), "cost_usd": None,
        "warnings": [], "usage": native_usage([])[0],
        "feedback": None, "notifications": [],
        "caller": receipt.get("caller"),
    }
    if receipt.get("requested_session_id"):
        result["requested_session_id"] = receipt["requested_session_id"]
    if receipt.get("launch_error"):
        result["warnings"].append("launch_failed: " + receipt["launch_error"])
    if receipt["exit_code"] not in (None, 0):
        errors = directory / "stderr.log"
        if errors.exists():
            with errors.open("rb") as source:
                source.seek(max(0, errors.stat().st_size - 4096))
                result["stderr_tail"] = source.read().decode("utf-8", errors="replace")
    events = directory / "events.jsonl"
    if not events.exists():
        return result
    observed = []
    with events.open(encoding="utf-8", errors="replace") as source:
        for line in source:
            try:
                event = json.loads(line)
                if not isinstance(event, dict):
                    raise ValueError("event is not an object")
            except ValueError:
                if "incomplete_or_invalid_events" not in result["warnings"]:
                    result["warnings"].append("incomplete_or_invalid_events")
                continue
            for key in ("session_id", "run_id"):
                if event.get(key):
                    result["ref"][key] = event[key]
            data = event.get("data") or {}
            if not isinstance(data, dict):
                continue
            raw = data.get("raw")
            if isinstance(raw, dict) and raw.get("type") == "result":
                observed.append(event)
            if event.get("type") == "run.finished":
                for key in ("execution_status", "acceptance_status", "memory_status", "error", "memory_error"):
                    if key in data:
                        result["execution"][key] = data[key]
            if event.get("type") == "feedback.bound" and isinstance(data.get("feedback"), dict):
                result["feedback"] = data["feedback"]
            if event.get("type") == "agent.result" and event.get("component") == "harness.claude" and event.get("phase") == "execute":
                raw = data.get("raw")
                if isinstance(raw, dict) and isinstance(raw.get("result"), str):
                    result["report"] = raw["result"]
                result["model"] = event.get("model") or None
    result["usage"], result["cost_usd"] = native_usage(observed)
    return result


def inspection_command(receipt, run_id):
    command = receipt["inspect_command"]
    allowed = {"--home", "--workspace", "--agent", "--memory-scope"}
    if (not isinstance(command, list) or len(command) < 3 or
            not all(isinstance(arg, str) and arg for arg in command) or
            not Path(command[0]).is_absolute() or command[1:3] != ["run", "show"] or
            len(command[3:]) % 2 or len(set(command[3::2])) != len(command[3::2]) or
            not set(command[3::2]) <= allowed or "--home" not in command[3::2] or
            not isinstance(run_id, str) or not run_id or run_id.startswith("-")):
        raise ValueError("invalid read-only inspection command")
    return [*command, run_id]


def inspect_receipt(directory, private_json):
    directory = Path(directory).absolute()
    receipt = private_json(directory / "launch.json")
    result = event_result(directory, receipt)
    run_id = result["ref"]["run_id"]
    if run_id:
        # Host inspection is read-only and needs no provider credentials.
        # Read the canonical output, including Host-added notices, when available.
        try:
            response = subprocess.run(
                inspection_command(receipt, run_id), cwd=receipt["cwd"],
                capture_output=True, text=True, check=False, timeout=15,
            )
            if response.returncode:
                raise ValueError("Host inspection failed")
            run = json.loads(response.stdout)
            if run.get("id") != run_id:
                raise ValueError("Host returned another run")
            result["ref"]["session_id"] = run["session_id"]
            result["report"] = run.get("output") or ""
            result["model"] = run.get("model") or None
            result["feedback"] = run.get("feedback") or result["feedback"]
            for key in ("execution_status", "acceptance_status", "memory_status", "error", "memory_error"):
                if key in run:
                    result["execution"][key] = run[key]
        except (OSError, ValueError, KeyError, TypeError, AttributeError, subprocess.TimeoutExpired):
            result["warnings"].append("host_inspection_unavailable; retained launch evidence only")
    if result["feedback"]:
        try:
            directory = receipt.get("feedback_args", {}).get("--feedback-config-dir") or receipt.get("feedback_config_dir")
            if directory:
                database = Path(directory).parent / "feedback/state.db"
                if database.exists():
                    with sqlite3.connect(database.absolute().as_uri() + "?mode=ro", uri=True) as connection:
                        result["notifications"] = [json.loads(row[0]) for row in connection.execute(
                            "SELECT payload FROM feedback WHERE json_extract(CAST(payload AS TEXT),'$.run_id')=?", (run_id,))]
        except (OSError, ValueError, KeyError, TypeError, IndexError, sqlite3.Error):
            result["warnings"].append("notification_inspection_unavailable")
    if receipt["exit_code"] is None:
        result["warnings"].append("process_exit_unobserved; inspect before continuation")
    if not result["report"]:
        result["warnings"].append("final_report_unavailable; see evidence")
    return result
