#!/usr/bin/env python3
"""Four approved owner-to-existing-bot history acceptance turns.

This runner performs real sends and briefly stops the managed production bot.
Invoke --send-authorized only after direct user approval of all four messages
and the temporary receiver switch. The flag is required before any runtime
files are read. Preparing or importing this module performs no subprocesses,
account reads, sends, or production changes. Unknown sends/tasks are never
replayed. Raw commands, outputs, chat material and credentials remain in the
existing private root; the public output contains only booleans and counts.

Required private files: context.json with target {name, open_dingtalk_id,
profile}, conversation_id; and the previously verified conversation-info.json.
Example after approval: rtk python3 experiments/history/dws_platform.py
--production ABS --private-root EXISTING_ABS --binary ABS --output ABS
--send-authorized
"""

import argparse
from collections import Counter
import datetime as dt
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import sqlite3
import stat
import subprocess
import time
import traceback


REPO = Path(__file__).resolve().parents[2]
MARKER = "AH-HISTORY-LIVE-20261011-R9K4"
FIRST_REPLY = "验收口令为 " + MARKER + "。"
TOOLS = (
    "mcp__conversation_history__search_conversation_history",
    "mcp__conversation_history__read_conversation_history",
)
FIRST_PROMPT = (
    "这是已授权的私聊历史能力验收。请只回复这一句：" + FIRST_REPLY +
    " 不要执行命令、读取或修改文件、部署、发消息，或把口令保存为长期记忆。"
)
RECALL_PROMPT = (
    "这是已授权的私聊历史能力验收。请实际调用内置 conversation_history 的 "
    "search_conversation_history 和 read_conversation_history 两个工具，"
    "查找并读取本次验收第一条消息及其机器人回复。报告当时回复的完整验收口令和"
    "该历史回复实际记录的 delivery_status；同时说明当前请求已排除。"
    "必须由本轮两个工具的真实结果恢复，不能只凭当前原生会话记忆回答。"
    "只使用这两个历史工具，不要执行命令、读取或修改文件、部署、发消息，"
    "或把口令保存为长期记忆。"
)


def require(ok, reason):
    if not ok:
        raise AssertionError(reason)


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def iso(value):
    return value.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def write_private(path, value):
    data = value if isinstance(value, bytes) else (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(data)


def private_path(path, directory=False):
    require(path.is_absolute() and path.resolve(strict=True) == path, "path must be existing, absolute and canonical")
    info = path.lstat()
    require(info.st_uid == os.getuid() and not stat.S_ISLNK(info.st_mode), "private path must belong to this user without symlinks")
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode), "private path has wrong file type")
    require(info.st_mode & 0o077 == 0, "private runtime files must not be group/world accessible")


def database(runtime):
    connection = sqlite3.connect((runtime / "state/state.db").as_uri() + "?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA query_only=ON")
    return connection


def sql_name(name):
    return '"' + name.replace('"', '""') + '"'


def known_values(value, field):
    if isinstance(value, dict):
        return ([value[field]] if field in value else []) + [v for child in value.values() for v in known_values(child, field)]
    if isinstance(value, list):
        return [v for child in value for v in known_values(child, field)]
    return []


def preserved_original_rows(before, after):
    """Compare original columns by declared identity, including WITHOUT ROWID."""
    tables, original_rows = 0, 0
    for (table,) in before.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"):
        info = before.execute("PRAGMA table_info(" + sql_name(table) + ")").fetchall()
        columns = [row[1] for row in info]
        primary = [row[1] for row in sorted(info, key=lambda row: row[5]) if row[5] > 0]
        identity = primary or ["rowid"]
        projection = ",".join(map(sql_name, columns))
        # A declared primary key exists on every WITHOUT ROWID table. Rowid is
        # used only for legacy tables without a declared identity.
        if not primary:
            projection = "rowid," + projection
        query = "SELECT " + projection + " FROM " + sql_name(table)
        old = [tuple(row) for row in before.execute(query)]
        new = [tuple(row) for row in after.execute(query)]
        indexes = [columns.index(name) for name in identity] if primary else [0]
        grouped_old, grouped_new = {}, {}
        for rows, grouped in ((old, grouped_old), (new, grouped_new)):
            for row in rows:
                key = tuple(row[index] for index in indexes)
                grouped.setdefault(key, Counter())[row] += 1
        # Counter preserves multiplicity even in legacy nullable primary keys.
        # New rows/columns may be added; every old row and original value stays.
        require(all(not (values - grouped_new.get(key, Counter())) for key, values in grouped_old.items()),
                "production original table rows changed")
        tables += 1
        original_rows += len(old)
    return {"tables": tables, "rows": original_rows}


def deferred_send_status_error(stderr):
    """Recognize only the observed same-task visibility delay, after warnings."""
    decoder = json.JSONDecoder()
    for index, character in enumerate(stderr):
        if character != "{":
            continue
        try:
            value, end = decoder.raw_decode(stderr[index:])
        except ValueError:
            continue
        if not isinstance(value, dict) or not isinstance(value.get("error"), dict) or stderr[index + end:].strip():
            continue
        error = value["error"]
        return (error.get("category") == "api" and error.get("origin") == "dingtalk_api"
                and error.get("operation") == "tools/call" and error.get("reason") == "invalid_request"
                and error.get("server_error_code") == "PARAM_ERROR" and error.get("server_key") == "im"
                and error.get("stage") == "tool_validation" and error.get("message") == "消息不存在")
    return False


def history_data(raw):
    if isinstance(raw, list):
        require(len(raw) == 1 and raw[0].get("type") == "text", "history result must be one actual MCP text block")
        raw = raw[0]["text"]
    require(isinstance(raw, str), "history result must contain MCP text")
    value = json.loads(raw)
    require(isinstance(value.get("turns"), list) and value.get("coverage", {}).get("source"), "history result omitted structured evidence")
    return value


def tool_evidence(events):
    """Pair actual execute-phase journal observations by native tool-use ID."""
    calls = {}
    last_seq = 0
    for event in events:
        tool = event.get("data", {}).get("tool")
        if event.get("phase") != "execute" or not tool:
            continue
        seq = event["seq"]
        require(seq > last_seq, "tool journal sequence must increase")
        last_seq = seq
        identity = tool.get("id")
        require(identity, "tool observation has no identity")
        if tool.get("stage") == "call":
            require(identity not in calls and tool.get("name") in TOOLS, "unexpected or duplicate execution tool")
            calls[identity] = {"id": identity, "name": tool["name"], "input": tool.get("input"), "call_seq": seq}
        elif tool.get("stage") == "result":
            require(identity in calls and "result_seq" not in calls[identity], "tool result has no unique preceding call")
            require(isinstance(tool.get("is_error"), bool), "tool result omitted actual error status")
            require(not tool.get("name") or tool["name"] == calls[identity]["name"], "tool result name changed")
            calls[identity].update(result=tool.get("result"), is_error=tool["is_error"], result_seq=seq)
        else:
            raise AssertionError("tool observation has unknown stage")
    require(all("result_seq" in value for value in calls.values()), "execution has unpaired tool calls")
    return list(calls.values())


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.production, self.private, self.binary = args.production, args.private_root, args.binary
        self.artifacts = self.private / "platform-acceptance"
        require(not self.artifacts.exists() and not args.output.exists(), "use fresh acceptance artifacts; do not rerun unknown sends")
        self.artifacts.mkdir(mode=0o700)
        self.logs = self.artifacts / "commands"
        self.logs.mkdir(mode=0o700)
        self.runtime = self.artifacts / "runtime"
        self.command_count = 0
        self.service = None
        self.service_log = None
        self.stopped_production = False
        self.context = json.loads((self.private / "context.json").read_text())
        self.target = self.context["target"]
        require(all(isinstance(self.target.get(k), str) and self.target[k] for k in ("name", "open_dingtalk_id", "profile")), "verified target metadata missing")
        require(isinstance(self.context.get("conversation_id"), str) and self.context["conversation_id"], "verified private conversation ID missing")
        info = json.loads((self.private / "conversation-info.json").read_text())
        require(self.context["conversation_id"] in known_values(info, "openConversationId"), "stored conversation metadata does not verify selected conversation")
        self.start = utcnow() - dt.timedelta(seconds=2)
        self.private_report = {"context": self.context, "started_at": iso(utcnow()), "steps": [], "commands": []}
        self.public = {"report_version": 1, "passed": False, "send_authorized": True,
                       "method": "four_approved_live_owner_bot_dm_turns",
                       "started_at": self.private_report["started_at"],
                       "binary_sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
                       "send_attempts": 0, "messages_sent": 0, "terminal_source_send_successes": 0,
                       "host_final_replies_accepted": 0, "host_completed_runs": 0,
                       "history_search_read_phases": 0, "live_dws_history_reads": 0,
                       "production_stopped": False, "production_restored": False,
                       "fresh_production_stream_connection": False, "production_original_rows_preserved": False,
                       "only_history_tools_called": False, "isolated_workspace_and_state": True,
                       "discovery_disabled_for_isolated_service": True, "cleanup_failures": 0}
        spec = importlib.util.spec_from_file_location("history_acceptance_installer", REPO / "tools/service/install.py")
        self.installer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.installer)
        self.launch_target = f"gui/{os.getuid()}/{self.installer.LABEL}"
        self.save()

    def save(self):
        write_private(self.artifacts / "evidence.json", self.private_report)
        write_private(self.args.output, self.public)

    def command(self, words, timeout=60):
        self.command_count += 1
        stem = self.logs / f"{self.command_count:03d}"
        # RTK's ps filter truncates the process inventory. Receiver exclusivity
        # needs every row, so preserve raw ps output through proxy.
        prefix = ["rtk", "proxy"] if Path(words[0]).name == "ps" else ["rtk"]
        argv = [*prefix, *map(str, words)]
        write_private(stem.with_suffix(".argv.json"), argv)
        try:
            result = subprocess.run(argv, cwd=REPO, capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            write_private(stem.with_suffix(".stdout"), error.stdout or b"")
            write_private(stem.with_suffix(".stderr"), error.stderr or b"")
            self.private_report["commands"].append({"sequence": self.command_count, "timeout": True})
            self.save()
            raise
        write_private(stem.with_suffix(".stdout"), result.stdout.encode())
        write_private(stem.with_suffix(".stderr"), result.stderr.encode())
        self.private_report["commands"].append({"sequence": self.command_count, "exit_code": result.returncode})
        self.save()
        return result

    def json_command(self, words, timeout=60):
        result = self.command(words, timeout)
        require(result.returncode == 0, "recorded subprocess failed; inspect private command evidence")
        return json.loads(result.stdout)

    def receivers(self):
        result = self.command(["/bin/ps", "-axo", "pid=,ppid=,command="])
        require(result.returncode == 0, "local receiver inventory failed")
        found = []
        for line in result.stdout.splitlines():
            fields = line.strip().split(None, 2)
            if len(fields) != 3:
                continue
            try:
                words = shlex.split(fields[2])
            except ValueError:
                continue
            if words and Path(words[0]).name == "agent-host" and words[1:3] in (["service", "run"], ["dingtalk", "serve"]):
                found.append(int(fields[0]))
        return found

    def idle(self):
        with database(self.production) as connection:
            require(connection.execute("SELECT count(*) FROM channel_messages WHERE status IN ('queued','claimed')").fetchone()[0] == 0, "production has active channel work; no receiver switch")
            require(connection.execute("SELECT count(*) FROM runs WHERE execution_status IN ('running','pending')").fetchone()[0] == 0, "production has active native work; no receiver switch")

    def fixture(self):
        self.runtime.mkdir(mode=0o700)
        for name in ("config", "state", "workspace", "native", "logs"):
            (self.runtime / name).mkdir(mode=0o700, parents=True)
        for name in ("service.json", "environment.json", "dingtalk.json"):
            source = self.production / "config" / name
            private_path(source)
            shutil.copyfile(source, self.runtime / "config" / name)
            (self.runtime / "config" / name).chmod(0o600)
        service = json.loads((self.runtime / "config/service.json").read_text())
        for name in ("knowledge_config", "agents_config", "discovery_config", "no_tools"):
            service.pop(name, None)
        service["allowed_tools"] = list(TOOLS)
        service["max_concurrent_conversations"] = 1
        write_private(self.runtime / "config/service.json", service)
        bot = json.loads((self.runtime / "config/dingtalk.json").read_text())
        require(self.target["profile"] == bot["corp_id"] + ":" + bot["owner_user_id"], "DWS identity must exactly match configured owner and tenant")
        secret = Path(bot["client_secret_file"])
        private_path(secret)
        cloned = self.runtime / "config/client-secret"
        shutil.copyfile(secret, cloned)
        cloned.chmod(0o600)
        bot["client_secret_file"] = str(cloned)
        write_private(self.runtime / "config/dingtalk.json", bot)
        write_private(self.runtime / "native/settings.json", {"disableAllHooks": True, "autoMemoryEnabled": False})
        registry = self.json_command(["dws", "profile", "list", "--format", "json"])
        require(registry.get("success") is True, "DWS local profile registry unavailable")
        profiles = [p for p in registry["profiles"] if p.get("profile") == self.target["profile"]]
        require(len(profiles) == 1 and profiles[0].get("corpId", "") + ":" + profiles[0].get("userId", "") == self.target["profile"], "DWS stable identity drift or ambiguity")
        checked = self.json_command([self.binary, "service", "check", "--root", self.runtime])
        require(checked.get("status") == "local_configuration_valid", "isolated service configuration invalid")

    def snapshot(self):
        destination = self.artifacts / "production-before.db"
        with database(self.production) as source, sqlite3.connect(destination) as backup:
            source.backup(backup)
        destination.chmod(0o600)

    def preserve(self):
        with sqlite3.connect(self.artifacts / "production-before.db") as before, database(self.production) as after:
            preserved = preserved_original_rows(before, after)
        self.public.update(production_original_rows_preserved=True, production_tables_preserved=preserved["tables"],
                           production_original_rows=preserved["rows"])

    def connect(self):
        require(not self.receivers(), "another local bot receiver is active")
        log_path = self.runtime / "logs/service.ndjson"
        self.service_log = open(log_path, "ab", buffering=0)
        log_path.chmod(0o600)
        offset = log_path.stat().st_size
        argv = ["rtk", str(self.binary), "service", "run", "--root", str(self.runtime)]
        write_private(self.artifacts / f"service-start-{offset}.json", argv)
        self.service = subprocess.Popen(argv, cwd=self.runtime / "workspace", stdout=self.service_log,
                                        stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            require(self.service.poll() is None, "isolated receiver exited before fresh connection")
            if "DingTalk Stream connection established" in log_path.read_bytes()[offset:].decode(errors="replace"):
                require(len(self.receivers()) == 1, "expected exactly one local bot receiver")
                self.public["one_local_bot_receiver"] = True
                return
            time.sleep(.2)
        raise TimeoutError("isolated receiver fresh Stream connection not observed")

    def stop_isolated(self):
        if self.service and self.service.poll() is None:
            os.killpg(self.service.pid, signal.SIGTERM)
            try:
                self.service.wait(timeout=45)
            except subprocess.TimeoutExpired:
                os.killpg(self.service.pid, signal.SIGKILL)
                self.service.wait(timeout=10)
        if self.service_log:
            self.service_log.close()
            self.service_log = None
        require(not self.receivers(), "isolated receiver did not stop; production must not connect concurrently")

    def send(self, label, text):
        require(self.public["send_attempts"] < 4, "four-message authorization bound exceeded")
        step = {"label": label, "text": text, "send_started_at": iso(utcnow()), "send_state": "attempting"}
        self.private_report["steps"].append(step)
        self.public["send_attempts"] += 1
        self.save()  # Durable intent precedes the send; reruns are forbidden.
        key = "AH-HISTORY-" + hashlib.sha256(str(self.private).encode()).hexdigest()[:24] + "-" + label
        result = self.json_command(["dws", "chat", "+messages-send", "--as", "user",
                                   "--open-dingtalk-id", self.target["open_dingtalk_id"], "--text", text,
                                   "--idempotency-key", key, "--profile", self.target["profile"], "--format", "json", "--yes"])
        step["send"] = result
        require(result.get("ok") is True and result.get("result", {}).get("success") is True, "source send was not accepted; do not replay")
        task = result.get("sendReceipt", {}).get("openTaskId")
        require(isinstance(task, str) and task, "source send omitted genuine task ID; do not replay")
        self.public["messages_sent"] += 1
        step["send_state"] = "api_accepted"
        self.save()
        return self.await_send_status(step)

    def await_send_status(self, step):
        # Query only the task ID returned by the already accepted source send.
        # This method never issues a source send or requeues a Host task.
        accepted = step.get("send", {})
        require(accepted.get("ok") is True and accepted.get("result", {}).get("success") is True,
                "status read needs an existing accepted source send")
        task = accepted.get("sendReceipt", {}).get("openTaskId")
        require(isinstance(task, str) and task, "status read needs a genuine returned send task ID")
        if step.get("send_state") == "terminal_success":
            return step
        deadline = time.monotonic() + 45
        while True:
            remaining = deadline - time.monotonic()
            require(remaining > 0, "source send status unknown after bounded reads; do not replay")
            observed = self.command(["dws", "chat", "+messages-query-send-status", "--open-task-id", task,
                                     "--profile", self.target["profile"], "--format", "json"], timeout=min(30, remaining))
            deferred = observed.returncode != 0 and deferred_send_status_error(observed.stderr)
            step.setdefault("status_read_attempts", []).append({"command_sequence": self.command_count,
                                                                "exit_code": observed.returncode,
                                                                "observed_at": iso(utcnow()),
                                                                "deferred_message_visibility": deferred})
            self.save()
            if deferred:
                require(time.monotonic() < deadline, "source send visibility unknown after bounded same-task reads; do not replay")
                time.sleep(min(1, max(0, deadline - time.monotonic())))
                continue
            require(observed.returncode == 0, "source status read failed; inspect captured stderr and do not replay")
            delivery = json.loads(observed.stdout)
            step["delivery"] = delivery
            status = delivery.get("result", {}).get("sendStatus")
            if status == "SUCCESS":
                require(delivery.get("readyForMessageActions") is True, "successful send has no actionable genuine reference")
                reference = delivery.get("messageRef", {})
                require(reference.get("openMessageId"), "successful source send omitted genuine message ID")
                require(not reference.get("openConversationId") or reference["openConversationId"] == self.context["conversation_id"], "source send returned another conversation")
                self.public["terminal_source_send_successes"] += 1
                step["send_state"] = "terminal_success"
                self.save()
                print("Source send " + step["label"] + " succeeded", flush=True)
                return step
            require(status not in ("FAILED", "FAILURE", "CANCELED"), "source send terminal failure; do not replay")
            require(time.monotonic() < deadline, "source send status unknown; do not replay")
            time.sleep(1)

    def wait(self, step, expect_run=True):
        message_id = step["delivery"]["messageRef"]["openMessageId"]
        deadline = time.monotonic() + 420
        while time.monotonic() < deadline:
            require(self.service.poll() is None, "isolated receiver exited while task state unknown; do not replay")
            with database(self.runtime) as connection:
                row = connection.execute("SELECT payload FROM channel_messages WHERE message_id=?", (message_id,)).fetchone()
                if row:
                    message = json.loads(row[0])
                    if message.get("reply_status") == "accepted":
                        require(message.get("status") == "completed", "Host terminal result was not completed")
                        require(message.get("conversation_id") == self.context["conversation_id"], "Host received another conversation")
                        step["host"] = message
                        self.public["host_final_replies_accepted"] += 1
                        if expect_run:
                            runrow = connection.execute("SELECT payload FROM runs WHERE id=?", (message.get("run_id"),)).fetchone()
                            require(runrow, "completed receipt has no source-linked run")
                            run = json.loads(runrow[0])
                            require(run.get("execution_status") == "completed", "native execution was not completed")
                            require(run.get("source", {}).get("channel_message_id") == message["id"], "run receipt attribution mismatch")
                            require(run.get("workspace") == str(self.runtime / "workspace") and not run.get("projects"), "acceptance escaped empty default workspace/catalog")
                            events = [json.loads(r[0]) for r in connection.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (run["id"],))]
                            step.update(run=run, events=events, tool_evidence=tool_evidence(events))
                            self.public["host_completed_runs"] += 1
                        else:
                            require(not message.get("run_id"), "/new unexpectedly executed a native task")
                        step["host_finished_at"] = iso(utcnow())
                        self.save()
                        print("Host " + step["label"] + " completed", flush=True)
                        return step
            time.sleep(.25)
        raise TimeoutError("Host task state unknown; original task is not replayed")

    def recall(self, step, original, former_current=None):
        required = set(TOOLS)
        reference = "host-history:" + original["host"]["id"]
        refreshed = former_current is None
        for evidence in step["tool_evidence"]:
            if evidence["is_error"]:
                continue
            data = history_data(evidence["result"])
            require(data["coverage"].get("current_request_excluded") is True, "history omitted current-request exclusion evidence")
            require(all(turn["receipt_id"] != step["host"]["id"] for turn in data["turns"]), "history returned current receipt")
            if former_current is not None and evidence["name"] == TOOLS[0]:
                refreshed = refreshed or any(turn.get("receipt_id") == former_current["host"]["id"] for turn in data["turns"])
            for turn in data["turns"]:
                if turn.get("receipt_id") != original["host"]["id"]:
                    continue
                require(turn.get("reference") == reference, "original history reference changed")
                require(turn.get("reply", {}).get("text") == original["host"]["reply_text"], "tool result did not recover exact stored reply")
                require(turn.get("delivery_status") == "accepted" and turn.get("execution_status") == "completed", "tool result did not recover actual original statuses")
                if evidence["name"] == TOOLS[1]:
                    require(evidence["input"].get("reference") == reference, "read did not use original stored reference")
                required.discard(evidence["name"])
        require(not required, "fresh paired search and read did not retrieve original receipt")
        require(refreshed, "resumed history search did not expose former current receipt; MCP argv may be stale")
        require(MARKER in step["host"]["reply_text"] and "accepted" in step["host"]["reply_text"].lower(), "actual delivered recall reply omitted marker or delivery state")
        self.public["history_search_read_phases"] += 1

    def platform_replies(self):
        end = utcnow() + dt.timedelta(seconds=1)
        envelope = self.json_command(["dws", "chat", "+chat-messages", "--open-dingtalk-id", self.target["open_dingtalk_id"],
                                      "--profile", self.target["profile"], "--format", "json", "--start", iso(self.start), "--end", iso(end),
                                      "--order", "asc", "--page-all", "--page-limit", "10", "--max-items", "200", "--no-reactions", "--timeout", "30"], timeout=45)
        self.public["live_dws_history_reads"] += 1
        self.private_report["platform_history"] = envelope
        require(envelope.get("contractVersion") == "im.message-list.v1" and isinstance(envelope.get("messages"), list), "DWS bounded history contract changed")
        require(envelope.get("complete") is True and not envelope.get("partial") and not envelope.get("hasMore") and envelope.get("paginationKnown") is True, "DWS bounded history coverage incomplete")
        require(envelope.get("count") == len(envelope["messages"]), "DWS history count inconsistent")
        require(envelope.get("queryRange", {}).get("semantics") == "[start,end)", "DWS bounded history omitted range semantics")
        require(all(m.get("conversationId") == self.context["conversation_id"] for m in envelope["messages"]), "DWS history returned outside selected conversation")
        matches, used = [], set()
        for step in self.private_report["steps"]:
            expected = step["host"]["reply_text"]
            candidates = [m for m in envelope["messages"] if m.get("text") == expected and m.get("messageId") not in used
                          and m.get("senderId") == self.target["open_dingtalk_id"]]
            require(candidates, "bounded DWS history did not expose comparable exact bot reply text")
            selected = candidates[0]
            used.add(selected["messageId"])
            matches.append(selected)
        self.private_report["matched_platform_replies"] = matches
        genuine_ids = [m["messageId"] for m in matches]
        details = self.json_command(["dws", "chat", "+messages-mget", "--msg-ids", ",".join(genuine_ids), "--no-reactions",
                                     "--profile", self.target["profile"], "--format", "json", "--timeout", "30"], timeout=45)
        self.private_report["platform_reply_mget"] = details
        # The installed normalized mget leaf has no generic ok/success field.
        # Verify its actual complete ledger and returned message snapshots.
        require(details.get("contractVersion") == "im.message-list.v1" and details.get("complete") is True
                and details.get("hasMore") is False and details.get("paginationKnown") is True, "mget coverage incomplete or contract changed")
        require(details.get("failedCount") == 0 and details.get("failures") == []
                and details.get("notFoundCount") == 0 and details.get("notFoundMessageIds") == [], "mget has failed or unavailable replies")
        require(details.get("requestedCount") == details.get("foundCount") == 4
                and isinstance(details.get("messages"), list) and len(details["messages"]) == 4, "mget did not return all four requested replies")
        returned = {m.get("messageId"): m for m in details["messages"]}
        require(set(returned) == set(genuine_ids), "mget returned another or repeated message ID")
        require(all(returned[m["messageId"]].get("text") == m["text"]
                    and returned[m["messageId"]].get("senderId") == self.target["open_dingtalk_id"]
                    and returned[m["messageId"]].get("conversationId") == self.context["conversation_id"] for m in matches),
                "mget exact reply text, bot sender or conversation differs")
        self.public.update(exact_platform_bot_replies_verified=4, genuine_reply_ids_mget_verified=4,
                           host_reply_id_available=False, platform_reply_text_representation_comparable=True,
                           exact_selected_private_conversation_verified=True)
        self.save()

    def execute(self):
        self.fixture()
        self.idle()
        state = self.installer.launch_state(self.launch_target, run=self.command)
        require(state.get("running") and state.get("pid") and self.receivers() == [state["pid"]], "managed production must be the sole local receiver")
        self.stopped_production = True  # Restore even if stop partially succeeds.
        self.installer.stop_existing(self.launch_target, run=self.command)
        self.public["production_stopped"] = True
        require(not self.receivers(), "production receiver remained after stop")
        self.idle()
        self.snapshot()
        self.connect()
        first = self.wait(self.send("first", FIRST_PROMPT))
        require(first["host"]["reply_text"].strip() == FIRST_REPLY, "first saved reply did not follow exact marker instruction")
        self.wait(self.send("new", "/new"), expect_run=False)
        third = self.wait(self.send("recall-after-new", RECALL_PROMPT))
        self.recall(third, first)
        require(first["run"]["native_session_id"] != third["run"]["native_session_id"], "/new did not rotate native session")
        dispatch = [e for e in third["events"] if e.get("type") == "agent.dispatched" and e.get("phase") == "execute"]
        require(len(dispatch) == 1 and dispatch[0].get("data", {}).get("resume") is False, "post-reset task did not dispatch a fresh native session")
        self.public["native_session_rotated_after_new"] = True
        self.stop_isolated()
        self.connect()
        fourth_prompt = RECALL_PROMPT + " 本轮必须先以空 query、limit=20 搜索，确认上轮查询消息已成为历史，再读取并报告第一条验收消息的回复。"
        fourth = self.wait(self.send("recall-after-restart", fourth_prompt))
        self.recall(fourth, first, former_current=third)
        require(third["run"]["native_session_id"] == fourth["run"]["native_session_id"], "service restart did not resume compatible native session")
        require(third["host"]["id"] != fourth["host"]["id"], "receipt did not refresh across restart")
        dispatch = [e for e in fourth["events"] if e.get("type") == "agent.dispatched" and e.get("phase") == "execute"]
        require(len(dispatch) == 1 and dispatch[0].get("data", {}).get("resume") is True, "post-restart task did not actually dispatch native resume")
        self.public.update(native_session_resumed_after_restart=True, refreshed_receipt_on_native_resume=True,
                           current_requests_excluded=True, exact_saved_reply_and_delivery_recovered=True,
                           only_history_tools_called=True)
        self.platform_replies()
        self.public["checks_completed"] = True

    def finish(self):
        try:
            if self.service:
                self.stop_isolated()
        except Exception:
            self.private_report["isolated_cleanup_error"] = traceback.format_exc()
            self.public["cleanup_failures"] += 1
        if self.stopped_production:
            try:
                require(not self.receivers(), "another receiver active; production restore would duplicate bot reception")
                restored = self.installer.install(self.binary, self.production, Path.home() / "Library/LaunchAgents", run=self.command)
                self.private_report["production_restore"] = restored
                require(restored.get("status") == "connected", "installer did not verify fresh production connection")
                require(self.receivers() == [restored["pid"]], "restored production is not sole local receiver")
                self.public.update(production_restored=True, fresh_production_stream_connection=True)
                if (self.artifacts / "production-before.db").exists():
                    self.preserve()
                print("Production restored with a fresh Stream connection", flush=True)
            except Exception:
                self.private_report["production_restore_error"] = traceback.format_exc()
                self.public["cleanup_failures"] += 1
        self.public["passed"] = bool(self.public.get("checks_completed") and self.public["production_restored"]
                                     and self.public["production_original_rows_preserved"] and not self.public["cleanup_failures"])
        self.private_report["finished_at"] = iso(utcnow())
        self.public["finished_at"] = self.private_report["finished_at"]
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--production", type=Path, required=True)
    parser.add_argument("--private-root", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--send-authorized", action="store_true", help="assert direct user approval of exactly four proposed sends and temporary receiver switch")
    args = parser.parse_args()
    if not args.send_authorized:
        parser.error("--send-authorized requires prior direct user approval; no runtime was read or changed")
    os.umask(0o077)
    for path in (args.production, args.private_root):
        private_path(path, directory=True)
    private_path(args.private_root / "context.json")
    private_path(args.private_root / "conversation-info.json")
    require(args.binary.is_absolute() and args.binary.resolve(strict=True) == args.binary and os.access(args.binary, os.X_OK), "binary must be an absolute canonical executable")
    require(args.output.is_absolute() and args.output.parent.resolve(strict=True) == args.output.parent, "output needs an existing canonical absolute parent")
    require(args.production not in args.private_root.parents and args.private_root not in args.production.parents and args.private_root != args.production, "acceptance must be outside production")
    require(args.production not in args.output.parents, "public output must be outside production")
    acceptance = Acceptance(args)

    def stop_signal(signum, frame):
        raise KeyboardInterrupt("acceptance interrupted; restoring receiver")

    signal.signal(signal.SIGTERM, stop_signal)
    signal.signal(signal.SIGINT, stop_signal)
    try:
        acceptance.execute()
    except (Exception, KeyboardInterrupt):
        acceptance.private_report["failure"] = traceback.format_exc()
        acceptance.public["execution_failed"] = True
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        acceptance.finish()
    print("Private-bot history acceptance " + ("passed" if acceptance.public["passed"] else "failed") +
          "; production restoration " + ("verified" if acceptance.public["production_restored"] else "unverified"), flush=True)
    return 0 if acceptance.public["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
