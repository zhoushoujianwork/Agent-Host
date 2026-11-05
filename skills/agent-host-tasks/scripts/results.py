"""Private launch receipts and read-only projections of Host execution evidence."""

import hashlib
import json
import math
import os
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sqlite3
import tempfile


TOKEN_FIELDS = ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")


def utc_now():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


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
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def save_bytes(path, value):
    """Create one owner-private snapshot without newline or encoding changes."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(value)
    except BaseException:
        try:
            os.unlink(path)
        except OSError:
            pass
        raise


def default_handoff_root():
    configured = os.environ.get("XDG_DATA_HOME")
    base = Path(configured) if configured and Path(configured).is_absolute() else Path.home() / ".local/share"
    return base / "agent-host" / "handoffs"


def new_receipt(config, command, cwd, stream_format, output_dir=None):
    if output_dir:
        directory = Path(output_dir).absolute()
        directory.mkdir(mode=0o700)
        directory.chmod(0o700)
    else:
        root = default_handoff_root()
        root.mkdir(mode=0o700, parents=True, exist_ok=True)
        directory = Path(tempfile.mkdtemp(prefix="handoff-", dir=root))
        directory.chmod(0o700)
    inspect_command = [command[0], "run", "show"]
    for flag in ("--home", "--workspace", "--agent", "--memory-scope"):
        if flag in command:
            offset = command.index(flag)
            inspect_command.extend(command[offset:offset + 2])
    receipt = {
        "destination_ref": str(config), "destination_key": destination_key(command, cwd),
        "inspect_command": inspect_command, "cwd": cwd,
        "exit_code": None, "elapsed_ms": None, "stream_format": stream_format,
        "created_at_utc": utc_now(), "finalized_at_utc": None,
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
        "caller": receipt.get("caller"), "stream_format": receipt.get("stream_format", "result"),
        "record_time": {"created_at_utc": receipt.get("created_at_utc"),
                        "finalized_at_utc": receipt.get("finalized_at_utc")},
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
    if receipt.get("stream_format") == "text":
        output = directory / "stdout.txt"
        if output.exists():
            result["report"] = output.read_bytes().decode("utf-8", errors="replace")
        return result
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


def artifact_paths(directory, receipt):
    stdout_name = "stdout.txt" if receipt.get("stream_format") == "text" else "events.jsonl"
    def existing(name):
        path = directory / name
        return str(path) if path.is_file() else None
    return {
        "directory": str(directory), "launch": existing("launch.json"),
        "request": existing("request.txt"), "result": existing("result.json"),
        "stderr": existing("stderr.log"), "stdout": existing(stdout_name),
        "run": existing("run.json"), "execution_record": existing("execution.md"),
    }


def inspect_receipt(directory, private_json, save_run=False, receipt=None):
    directory = Path(directory).absolute()
    receipt = receipt if receipt is not None else private_json(directory / "launch.json")
    result = event_result(directory, receipt)
    result["artifacts"] = artifact_paths(directory, receipt)
    run_id = result["ref"]["run_id"]
    if run_id:
        # Host inspection is read-only and needs no provider credentials.
        # Read the canonical output, including Host-added notices, when available.
        try:
            response = subprocess.run(
                inspection_command(receipt, run_id), cwd=receipt["cwd"],
                capture_output=True, check=False, timeout=15,
            )
            if response.returncode:
                raise ValueError("Host inspection failed")
            run = json.loads(response.stdout.decode("utf-8"))
            if run.get("id") != run_id:
                raise ValueError("Host returned another run")
            if save_run:
                try:
                    save_bytes(directory / "run.json", response.stdout)
                except OSError as error:
                    result["warnings"].append("artifact_export_failed: run.json: " + type(error).__name__)
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
            feedback_directory = receipt.get("feedback_args", {}).get("--feedback-config-dir") or receipt.get("feedback_config_dir")
            if feedback_directory:
                database = Path(feedback_directory).parent / "feedback/state.db"
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
    result["artifacts"] = artifact_paths(directory, receipt)
    return result


def execution_markdown(result, request):
    """Render a deterministic raw execution record without semantic promotion."""
    execution = result.get("execution") or {}
    lines = [
        "# 原始执行记录", "",
        "本记录保留启动器和 Agent 的原始证据，不代表业务验收或知识整理。", "",
        "## 记录时间", "",
        "以下时间是记录文件的创建和定稿时间，不是对执行状态的推断。",
        f"- 创建时间（UTC）: {json.dumps((result.get('record_time') or {}).get('created_at_utc'), ensure_ascii=False)}",
        f"- 定稿时间（UTC）: {json.dumps((result.get('record_time') or {}).get('finalized_at_utc'), ensure_ascii=False)}", "",
        "## 引用", "",
        f"- 证据目录: {json.dumps(result.get('evidence_ref'), ensure_ascii=False)}",
        f"- 目标配置: {json.dumps((result.get('ref') or {}).get('destination_ref'), ensure_ascii=False)}",
        f"- 会话 ID: {json.dumps((result.get('ref') or {}).get('session_id'), ensure_ascii=False)}",
        f"- 运行 ID: {json.dumps((result.get('ref') or {}).get('run_id'), ensure_ascii=False)}",
        f"- 流格式: {json.dumps(result.get('stream_format'), ensure_ascii=False)}", "",
        "## 原始任务", "",
    ]
    request_fence = "```"
    while request_fence in request:
        request_fence += "`"
    lines.extend([request_fence + "text", request, request_fence, "",
                  "## 观察到的状态", "",
                  f"- 进程退出码: {json.dumps(execution.get('exit_code'), ensure_ascii=False)}",
                  f"- 执行状态: {json.dumps(execution.get('execution_status'), ensure_ascii=False)}",
                  f"- 验收状态: {json.dumps(execution.get('acceptance_status'), ensure_ascii=False)}",
                  f"- 记忆状态: {json.dumps(execution.get('memory_status'), ensure_ascii=False)}",
                  f"- 执行错误: {json.dumps(execution.get('error'), ensure_ascii=False)}",
                  f"- 记忆错误: {json.dumps(execution.get('memory_error'), ensure_ascii=False)}"])
    notifications = result.get("notifications") or []
    if notifications:
        for index, notification in enumerate(notifications, 1):
            status = notification.get("status") if isinstance(notification, dict) else None
            lines.append(f"- 通知 {index}: {json.dumps(status, ensure_ascii=False)}")
    else:
        lines.append("- 通知: null")
    lines.extend(["", "## Agent 报告", ""])
    report = result.get("report") or ""
    fence = "```"
    while fence in report:
        fence += "`"
    lines.extend([fence + "text", report, fence, "", "## 原始文件", ""])
    for label, key in (("启动回执", "launch"), ("原始任务", "request"),
                       ("子进程标准错误", "stderr"), ("子进程标准输出", "stdout"),
                       ("规范运行快照", "run")):
        lines.append(f"- {label}: {json.dumps((result.get('artifacts') or {}).get(key), ensure_ascii=False)}")
    if result.get("warnings"):
        lines.extend(["", "## 警告", ""])
        lines.extend(f"- {json.dumps(warning, ensure_ascii=False)}" for warning in result["warnings"])
    return "\n".join(lines) + "\n"
