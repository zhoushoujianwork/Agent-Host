"""Bridge concurrent owner-local room messages to the configured Host CLI.

Host owns execution, native sessions and durable evidence. This bridge only
launches the validated command, projects its public events and requests cancel.
"""

from collections import deque
from datetime import datetime, timezone
import hashlib
import json
import os
import re
from pathlib import Path
import signal
import subprocess
import tempfile
import sys
import threading
import time
import uuid

from settings_store import private_bytes, atomic_write

ROOT = Path(__file__).resolve().parents[2]
PROVIDER_KEYS = {"TYPESAFE_API_KEY", "JEV_API_KEY", "ANTHROPIC_BASE_URL",
                 "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
                 "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK", "CLAUDE_CODE_MAX_OUTPUT_TOKENS"}
OUTPUT_LIMIT = 64 * 1024


def native_text(value):
    if isinstance(value, str):
        text = value
    elif isinstance(value, list):
        text = "\n".join(native_text(item) for item in value)
    elif isinstance(value, dict) and value.get("type") == "text":
        text = str(value.get("text", ""))
    elif isinstance(value, dict) and value.get("type") == "content":
        text = native_text(value.get("content", ""))
    elif isinstance(value, dict) and value.get("type") == "image":
        text = "[image output]"
    else:
        text = json.dumps(value, ensure_ascii=False, indent=2)
    return text if len(text) <= OUTPUT_LIMIT else text[:OUTPUT_LIMIT] + "\n[output truncated; full evidence remains in the execution record]"


def default_config(root):
    """Never attach a development viewer to another root's CLI destination."""
    if root is None:
        return None
    root = Path(root).resolve()
    local = root / "config/web-chat.json"
    if local.exists():
        return local
    path = Path.home() / ".config/agent-host/task-handoff.json"
    try:
        value = json.loads(private_bytes(path))
        args = value["args"]
        for flag in ("--home", "--agents-config", "--workspace", "--native-profile"):
            Path(args[args.index(flag) + 1]).resolve().relative_to(root)
        return path
    except (OSError, ValueError, KeyError, TypeError):
        return None


def agent_destination(path):
    """Read only the explicitly configured CLI catalog; never scan other homes."""
    value = json.loads(private_bytes(Path(path)))
    args = value.get("args")
    if not isinstance(args, list) or not all(isinstance(arg, str) for arg in args):
        raise ValueError("Chat 连接缺少明确的智能体配置。")
    def option(flag):
        if args.count(flag) != 1 or args.index(flag) + 1 >= len(args):
            raise ValueError("Chat 连接缺少明确的智能体配置。")
        return args[args.index(flag) + 1]
    catalog_path = Path(option("--agents-config"))
    if not catalog_path.is_absolute():
        raise ValueError("Chat 智能体目录必须使用绝对路径。")
    raw = private_bytes(catalog_path)
    catalog = json.loads(raw)
    agents = catalog.get("agents")
    if not isinstance(agents, list) or not 0 < len(agents) <= 128:
        raise ValueError("Chat 智能体目录无效。")
    choices = []
    for agent in agents:
        if not isinstance(agent, dict):
            raise ValueError("Chat 智能体目录无效。")
        identifier, name = agent.get("id"), agent.get("name")
        if not isinstance(identifier, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", identifier) or not isinstance(name, str) or not name.strip() or len(name) > 200 or any(c in name for c in "\x00\r\n") or any(a["id"] == identifier for a in choices):
            raise ValueError("Chat 智能体目录无效。")
        choices.append({"id": identifier, "name": name})
    selected = option("--agent")
    if selected not in {a["id"] for a in choices}:
        raise ValueError("Chat 默认智能体已不在配置中。")
    optional = lambda flag: args[args.index(flag) + 1] if flag in args and args.index(flag) + 1 < len(args) else None
    return {"agents": choices, "selected_agent": selected, "agents_path": str(catalog_path),
            "catalog_digest": hashlib.sha256(raw).hexdigest(), "state_home": option("--home"),
            "execution": {"model": optional("--model"), "workspace": option("--workspace"),
                          "execution_config": optional("--execution-config")}}


def configured_launch(path, agent_id=None):
    value = json.loads(private_bytes(path))
    destination = agent_destination(path)
    if agent_id is not None and agent_id not in {a["id"] for a in destination["agents"]}:
        raise ValueError("所选智能体不在 Web 连接配置中。")
    script = next((directory / "scripts/chat.py" for directory in
                   (ROOT / "skills/agent-host-tasks",
                    Path.home() / ".agents/skills/agent-host-tasks",
                    Path.home() / ".codex/skills/agent-host-tasks")
                   if (directory / "scripts/chat.py").is_file()), None)
    if script is None:
        raise ValueError("Chat 配置校验器不可用，请检查安装。")
    checked = subprocess.run([sys.executable, "-B", str(script), "--config", str(path), "--check"],
                             capture_output=True, timeout=45, check=False)
    if checked.returncode:
        raise ValueError("Chat 配置校验失败，请检查 Agent、工作目录和模型连接。")
    result = json.loads(checked.stdout)
    # Read the same validated configuration again; a changed destination cannot
    # race the checker and redirect this launch.
    if json.loads(private_bytes(path)) != value:
        raise ValueError("Chat 配置已变化，请检查后重新发送。")
    if agent_destination(path) != destination:
        raise ValueError("智能体目录已变化，请重新选择后发送。")
    managed = json.loads(private_bytes(Path(value["environment_file"])))
    if not managed.keys() <= PROVIDER_KEYS or not all(isinstance(v, str) and v and
            v.strip() == v and not any(c in v for c in "\x00\r\n") for v in managed.values()):
        raise ValueError("Chat 模型环境配置无效。")
    env = {k: v for k, v in os.environ.items() if k not in PROVIDER_KEYS and
           not k.startswith(("ANTHROPIC_", "CLAUDE_CODE_")) and k not in ("CLAUDE_CONFIG_DIR", "CLAUDECODE")}
    env.update(managed)
    command, cwd = [*result["command"], "--renew-runtime", "--feedback-other-users-only=true"], result["cwd"]
    if agent_id is not None:
        command[command.index("--agent") + 1] = agent_id
    identity = hashlib.sha256(json.dumps([command, cwd, managed], sort_keys=True).encode()).hexdigest()
    return command, cwd, env, identity


class ChatConflict(ValueError):
    pass


class AgentChatRuntime:
    """One independent in-memory conversation per configured local Agent.

    Host persists run evidence/native sessions. This owner-local viewer keeps
    projections while running; switching is never a replay or a channel handoff.
    """
    def __init__(self, config, launcher=configured_launch):
        self.config = Path(config) if config else None
        self.launcher = launcher
        self.lock = threading.RLock()
        self.contexts = {}
        self.current = None
        self.selected_agent = None
        self.closed = False

    @property
    def state_home(self):
        return self.current.state_home if self.current else None

    def _select(self, agent_id, destination):
        if agent_id not in {a["id"] for a in destination["agents"]}:
            raise ValueError("所选智能体不在 Web 连接配置中。")
        key = (destination["agents_path"], destination["state_home"], agent_id)
        if key not in self.contexts:
            if len(self.contexts) >= 128:
                raise ChatConflict("此页面已保留 128 个智能体会话，请重新启动页面服务后继续。")
            room_file = Path(destination["state_home"]) / "web-chat/rooms" / (hashlib.sha256(json.dumps(key).encode()).hexdigest() + ".json")
            self.contexts[key] = ChatRuntime(self.config, lambda path: self.launcher(path, agent_id), room_file)
        self.selected_agent, self.current = agent_id, self.contexts[key]
        self.current.state_home = Path(destination["state_home"]).resolve()

    def snapshot(self):
        with self.lock:
            try:
                destination = agent_destination(self.config) if self.config else None
                if destination and self.current is None:
                    self._select(destination["selected_agent"], destination)
                available = destination and self.selected_agent in {a["id"] for a in destination["agents"]}
                result = self.current.snapshot() if self.current else {"turns": [], "active_id": None}
                result.update(enabled=bool(available) and not self.closed, selected_agent=self.selected_agent,
                              agents=destination["agents"] if destination else [],
                              notice="" if available else "请检查此 Web 连接的智能体配置。")
                return result
            except (OSError, ValueError, KeyError, TypeError, AttributeError):
                result = self.current.snapshot() if self.current else {"turns": [], "active_id": None}
                result.update(enabled=False, agents=[], selected_agent=self.selected_agent,
                              notice="无法读取 Web 智能体配置，请检查连接设置。")
                return result

    def select(self, agent_id):
        with self.lock:
            if self.closed or any(c.active for c in self.contexts.values()):
                raise ChatConflict("请等待当前执行结束或中止后，再切换智能体。")
            self._select(agent_id, agent_destination(self.config))

    def start(self, request_id, message, agent_id=None):
        with self.lock:
            snapshot = self.snapshot()
            if not snapshot["enabled"] or (agent_id is not None and agent_id != self.selected_agent):
                raise ChatConflict("当前智能体已变化，请刷新后重新发送。")
            return self.current.start(request_id, message)

    def reset(self):
        with self.lock:
            if self.current:
                self.current.reset()

    def stop(self, turn_id):
        with self.lock:
            if self.current:
                self.current.stop(turn_id)

    def close(self):
        with self.lock:
            self.closed = True
            contexts = list(self.contexts.values())
        for runtime in contexts:
            runtime.close()


class ChatRuntime:
    def __init__(self, config, launcher=configured_launch, room_file=None):
        self.config = Path(config) if config else None
        self.launcher = launcher
        self.lock = threading.RLock()
        self.turns = []
        self.children = {}
        self.workers = {}
        self.room_file = room_file
        self.room_id = uuid.uuid4().hex
        saved = {}
        if room_file:
            room_file.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            fd, temporary = tempfile.mkstemp(prefix=".room-", dir=room_file.parent)
            try:
                with os.fdopen(fd, "w") as stream:
                    json.dump({"room_id": self.room_id}, stream)
                    stream.flush()
                    os.fsync(stream.fileno())
                try:
                    os.link(temporary, room_file)
                except FileExistsError:
                    saved = json.loads(private_bytes(room_file, max_bytes=4 << 20))
                    self.room_id = saved["room_id"]
                    if not isinstance(self.room_id, str) or not re.fullmatch(r"[0-9a-f]{32}", self.room_id):
                        raise ValueError("Web Chat 房间记录无效。")
            finally:
                os.unlink(temporary)
        self.session = None
        self.identity = saved.get("identity")
        self.needs_new = False
        self.closed = False
        self.state_home = None
        self.native_indexes = {}
        self.native_messages = {}
        receipts = saved.get("turns", [])
        if not isinstance(receipts, list) or len(receipts) > 20:
            raise ValueError("Web Chat 消息收据无效。")
        for receipt in receipts:
            if not isinstance(receipt, dict) or not isinstance(receipt.get("input"), str) or len(receipt["input"].encode()) > 16 * 1024 or receipt.get("room_id") != self.room_id or receipt.get("sender_id") != "local-owner" or not isinstance(receipt.get("request_id"), str) or not 0 < len(receipt["request_id"]) <= 64:
                raise ValueError("Web Chat 消息收据无效。")
            if receipt.get("status") in ("starting", "running", "stopping"):
                receipt.update(status="unknown", error="页面服务重启，此条执行结果尚未确认。请查看执行记录；不会自动重试。")
            receipt["output"] = []
            self.turns.append(receipt)

    def _persist(self):
        if self.room_file:
            fields = ("id", "request_id", "input", "room_id", "sender_id", "status", "run_id", "session_id", "exit_code", "started_at", "elapsed_ms", "error", "error_code", "model", "transport", "engine")
            receipts = []
            for turn in self.turns:
                receipt = {key: turn[key] for key in fields if key in turn}
                receipt["text"] = turn["text"][:8000]
                receipt["output_truncated"] = bool(turn["output"] or len(turn["text"]) > 8000)
                receipts.append(receipt)
            atomic_write(self.room_file, json.dumps({"room_id": self.room_id, "identity": self.identity, "turns": receipts}, ensure_ascii=False).encode())

    @property
    def active(self):
        return next((t["id"] for t in self.turns if t["status"] in ("starting", "running", "stopping")), None)

    def snapshot(self):
        with self.lock:
            snapshot = json.loads(json.dumps({"enabled": self.config is not None,
                "notice": "" if self.config else "尚未配置 Chat 执行环境，请配置此 Host 的 CLI 连接。",
                "room_id": self.room_id, "active_id": self.active,
                "active_ids": [t["id"] for t in self.turns if t["status"] in ("starting", "running", "stopping")],
                "max_concurrent": 4, "needs_new": False, "turns": self.turns}))
            for turn in snapshot["turns"]:
                started = turn.pop("started_at", None)
                if turn["status"] in ("starting", "running", "stopping") and started:
                    turn["elapsed_ms"] = max(0, int((time.time() - started) * 1000))
            return snapshot

    def start(self, request_id, message):
        if not isinstance(request_id, str) or len(request_id) > 64 or not request_id:
            raise ValueError("发送请求缺少有效标识。")
        if not isinstance(message, str) or not message.strip() or len(message.encode()) > 16 * 1024:
            raise ValueError("请输入内容，单次消息最多 16 KiB。")
        with self.lock:
            previous = next((t for t in self.turns if t["request_id"] == request_id), None)
            if previous:
                if previous["input"] != message:
                    raise ChatConflict("请求标识已用于另一条消息。")
                return previous["id"]
            if self.closed or not self.config:
                raise ChatConflict("Chat 执行环境尚不可用。")
            if sum(t["status"] in ("starting", "running", "stopping") for t in self.turns) >= 4:
                raise ChatConflict("此房间已有 4 条消息正在执行，请稍后发送。")
            if len(self.turns) >= 20:
                raise ChatConflict("此对话已达 20 轮，请开启新对话。")
            turn = {"id": uuid.uuid4().hex, "request_id": request_id, "input": message,
                    "text": "", "output": [], "status": "starting", "error": "",
                    "run_id": "", "session_id": "", "exit_code": None,
                    "started_at": time.time()}
            self.turns.append(turn)
            turn["room_id"] = self.room_id
            turn["sender_id"] = "local-owner"
            try:
                self._persist()
            except (OSError, ValueError):
                self.turns.pop()
                raise
            worker = threading.Thread(target=self._run, args=(turn,), daemon=True)
            self.workers[turn["id"]] = worker
            worker.start()
            return turn["id"]

    def reset(self):
        with self.lock:
            if self.active:
                raise ChatConflict("请先中止当前执行，再开启新对话。")
            self.turns = []
            self.room_id = uuid.uuid4().hex
            self.session = self.identity = None
            self._persist()
            self.native_indexes = {}
            self.native_messages = {}
            self.needs_new = False

    def stop(self, turn_id):
        with self.lock:
            turn = next((t for t in self.turns if t["id"] == turn_id), None)
            if turn is None:
                raise KeyError("execution not found")
            if turn["status"] not in ("starting", "running"):
                return
            child = self.children.get(turn_id)
            if turn["status"] == "stopping":
                return
            if child and child.poll() is not None:
                return
            turn["status"] = "stopping"
            if child and child.poll() is None:
                # Host's signal context cancels its native process group and
                # persists the interrupted outcome before exiting.
                try:
                    child.send_signal(signal.SIGTERM)
                except ProcessLookupError:
                    pass

    def _output(self, turn, kind, **fields):
        rows = turn["output"]
        row = {"id": str(int(rows[-1]["id"]) + 1 if rows else 1), "kind": kind, **fields}
        rows.append(row)
        if len(rows) > 200:
            del rows[0]
            turn["output_truncated"] = True
        return row

    def _tool_call(self, turn, tool_id, name):
        row = next((row for row in turn["output"] if row["kind"] == "tool" and
                    row["stage"] == "call" and row["tool_id"] == tool_id), None)
        return row if row is not None else self._output(turn, "tool", stage="call",
            tool_id=tool_id, name=name, content="", parameters="", status="running")

    def _text(self, turn, delta):
        if not isinstance(delta, str) or len(turn["text"]) + len(delta) > 512 * 1024:
            raise ValueError("回答超过页面显示上限，请查看执行记录。")
        turn["text"] += delta
        if turn["output"] and turn["output"][-1]["kind"] == "text":
            turn["output"][-1]["content"] += delta
        else:
            self._output(turn, "text", content=delta)

    def _native(self, turn, event):
        data = event.get("data") or {}
        raw = data.get("raw") or {}
        if not isinstance(raw, dict):
            return
        parent = raw.get("parent_tool_use_id", "")
        indexes = self.native_indexes.setdefault(turn["id"], {})
        partial = raw.get("event") or {}
        if raw.get("type") == "stream_event" and isinstance(partial, dict):
            key = (parent, partial.get("index", 0))
            if partial.get("type") == "message_start":
                for index in list(indexes):
                    if index[0] == parent:
                        del indexes[index]
            block = partial.get("content_block") or {}
            if partial.get("type") == "content_block_start" and block.get("type") == "tool_use":
                tool_id, name = block.get("id"), block.get("name")
                if tool_id and name:
                    indexes[key] = (tool_id, name)
                    self._tool_call(turn, tool_id, name)
            delta = partial.get("delta") or {}
            if delta.get("type") == "input_json_delta" and key in indexes:
                tool_id, name = indexes[key]
                row = self._tool_call(turn, tool_id, name)
                row["content"] = native_text(row["content"] + delta.get("partial_json", ""))
        native_type = raw.get("type")
        if native_type == "system":
            subtype = raw.get("subtype", "")
            if subtype == "init":
                self._output(turn, "status", label="init", content=str(raw.get("model", "")))
            elif subtype in {"status", "compact_boundary", "task_started", "task_progress", "task_notification", "hook_started", "hook_progress", "hook_response"}:
                content = {key: raw[key] for key in ("status", "task_id", "description", "summary", "message", "last_tool_name", "elapsed_time_seconds", "hook_name", "hook_event", "stdout", "stderr", "output", "exit_code", "outcome") if key in raw}
                self._output(turn, "status", label=subtype, content=native_text(content))
        elif native_type == "tool_progress" and raw.get("tool_use_id") and raw.get("tool_name"):
            row = self._tool_call(turn, raw["tool_use_id"], raw["tool_name"])
            row["progress"] = native_text({key:raw[key] for key in ("elapsed_time_seconds", "status", "task_id") if key in raw})
        elif native_type == "rate_limit_event" and raw.get("rate_limit_info"):
            self._output(turn, "status", label=native_type, content=native_text(raw["rate_limit_info"]))
        elif event.get("type") == "agent.result":
            self._output(turn, "status", label=str(raw.get("subtype") or "result"),
                         content=native_text(raw["errors"]) if raw.get("errors") else "")
        elif native_type == "assistant":
            message = raw.get("message") or {}
            identity = raw.get("uuid") or message.get("id") or hashlib.sha256(json.dumps(message, sort_keys=True).encode()).hexdigest()
            seen = self.native_messages.setdefault(turn["id"], set())
            if identity not in seen:
                seen.add(identity)
                text = "".join(block.get("text", "") for block in message.get("content", []) if block.get("type") == "text")
                if raw.get("is_api_error_message"):
                    self._output(turn, "status", label="api_error", content=native_text(text))
                elif text and not turn["text"].endswith(text):
                    # Native full messages backfill adapters that supplied no
                    # text deltas. Streamed/full envelopes must never repeat it.
                    self._text(turn, text)

    def _event(self, turn, event):
        with self.lock:
            kind = event.get("type")
            if kind == "run.finished" and isinstance(event.get("elapsed_ms"), (int, float)):
                turn["elapsed_ms"] = max(0, event["elapsed_ms"])
            if kind == "run.started":
                turn["run_id"] = str(event.get("run_id", ""))
                turn["session_id"] = str(event.get("session_id", ""))
                self._persist()
            if kind == "agent.dispatched":
                execution = (event.get("data") or {}).get("execution") or {}
                if isinstance(execution, dict):
                    turn["transport"] = str(execution.get("transport", ""))
                    turn["engine"] = str(execution.get("engine", ""))
            if kind == "conversation.runtime_renewed":
                self._output(turn, "status", label="执行上下文已更新", content="本轮技能或配置发生变化，已开启新执行会话；之前的聊天记录仍保留，原生历史未续接。")
            # Only the business Claude stream belongs in Chat. Host memory and
            # experience processing retain their evidence in the inspector.
            if event.get("agent") == "memory-extractor" or event.get("phase") not in (None, "", "execute", "knowledge_search", "knowledge_read"):
                return event.get("status") if kind == "run.finished" else None
            if event.get("component") == "harness.acp" and event.get("model") and (kind in {"text.delta", "agent.result"} or (event.get("data") or {}).get("native_type") == "native_configuration"):
                turn["model"] = str(event["model"])
            if kind == "text.delta":
                self._text(turn, event.get("delta", ""))
            if kind in {"native.evidence", "agent.started", "agent.result", "tool.activity"}:
                if event.get("component") == "harness.acp":
                    self._acp(turn, event)
                else:
                    self._native(turn, event)
            if kind == "tool.activity":
                tool = (event.get("data") or {}).get("tool") or {}
                tool_id, name = tool.get("id"), tool.get("name")
                if tool_id and name and tool.get("stage") in {"call", "update"}:
                    row = self._tool_call(turn, tool_id, name)
                    payload = tool.get("input", {})
                    row["parameters"] = native_text(payload)
                    row["content"] = native_text(payload["command"]) if name == "Bash" and isinstance(payload, dict) and isinstance(payload.get("command"), str) else row["parameters"]
                elif tool_id and tool.get("stage") == "result":
                    call = next((row for row in turn["output"] if row.get("tool_id") == tool_id and row.get("stage") == "call"), None)
                    status = "failed" if tool.get("is_error") else "completed"
                    if call:
                        call["status"] = status
                    self._output(turn, "tool", stage="result", tool_id=tool_id,
                        name=name or (call["name"] if call else tool_id), content=native_text(tool.get("result", "")),
                        parameters="", status=status)
            if kind == "run.finished":
                return event.get("status")
        return None

    def _acp(self, turn, event):
        raw = (event.get("data") or {}).get("raw") or {}
        if not isinstance(raw, dict):
            return
        if event.get("type") == "agent.started":
            self._output(turn, "status", label="init", content=str(event.get("model") or "Claude ACP"))
        elif raw.get("native_initialization"):
            skills = raw.get("skills") or []
            servers = raw.get("mcp_servers") or []
            details = f"{len(skills)} 个技能 · {len(servers)} 个知识与工具服务"
            if skills:
                details += "\nSkills: " + ", ".join(str(name) for name in skills)
            if servers:
                details += "\nMCP: " + ", ".join(str(server.get("name", "")) for server in servers if isinstance(server, dict))
            self._output(turn, "status", label="原生能力", content=details)
        elif raw.get("sessionUpdate") == "plan":
            content = "\n".join(("✓ " if entry.get("status") == "completed" else "→ " if entry.get("status") == "in_progress" else "· ") + str(entry.get("content", "")) for entry in raw.get("entries", []) if isinstance(entry, dict))
            row = next((row for row in turn["output"] if row.get("label") == "原生计划"), None)
            if row is None:
                row = self._output(turn, "status", label="原生计划", content="")
            row["content"] = native_text(content)
        elif raw.get("sessionUpdate") == "notice":
            self._output(turn, "status", label=str(raw.get("title") or "Agent 通知"), content=native_text(raw.get("description", "")))
        elif raw.get("sessionUpdate") == "permission_request":
            allowed = (raw.get("outcome") or {}).get("outcome") == "selected"
            self._output(turn, "status", label="原生权限请求", content="按执行配置允许本次调用。" if allowed else "已取消：当前入口尚不支持交互审批。")

    def _run(self, turn):
        finished = None
        failure = ""
        child = None
        diagnostics = deque(maxlen=8)
        reader = None
        def drain_errors(stream):
            while chunk := stream.read(1024):
                diagnostics.append(chunk)
        try:
            command, cwd, env, identity = self.launcher(self.config)
            with self.lock:
                if turn["status"] == "stopping":
                    return
                if self.identity and self.identity != identity:
                    raise ValueError("执行配置已变化，请开启新对话。")
                command = [*command, "--room", turn["room_id"], "--request-id", turn["request_id"], "--received-at", datetime.fromtimestamp(turn["started_at"], timezone.utc).isoformat(),
                           "--format", "json", "-m", turn["input"]]
                child = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
                reader = threading.Thread(target=drain_errors, args=(child.stderr,), daemon=True)
                reader.start()
                self.children[turn["id"]] = child
                turn["status"] = "running"
                self.identity = identity
                self.state_home = Path(command[command.index("--home") + 1]).resolve() if "--home" in command else None
            while True:
                line = child.stdout.readline(4 * 1024 * 1024 + 1)
                if not line:
                    break
                if len(line) > 4 * 1024 * 1024:
                    raise ValueError("执行事件超过页面处理上限，请查看执行记录。")
                event = json.loads(line)
                if not isinstance(event, dict):
                    raise ValueError("Host 返回了无效事件。")
                result = self._event(turn, event)
                if result:
                    finished = result
            child.wait()
        except ValueError as error:
            failure = str(error) if not isinstance(error, json.JSONDecodeError) else "Host 返回了无效事件。"
        except (OSError, subprocess.SubprocessError, KeyError, TypeError, AttributeError):
            failure = "无法启动或读取 Host，请检查 Chat 配置和模型连接。"
        finally:
            if child:
                if child.poll() is None:
                    try:
                        child.send_signal(signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                    child.wait()
                child.stdout.close()
                if reader:
                    reader.join()
                child.stderr.close()
            with self.lock:
                stopping = turn["status"] == "stopping"
                turn["exit_code"] = child.returncode if child else None
                completed = not failure and child and child.returncode == 0 and finished == "completed"
                turn["status"] = "completed" if completed else "canceled" if stopping else "failed"
                if "elapsed_ms" not in turn:
                    turn["elapsed_ms"] = max(0, int((time.time() - turn["started_at"]) * 1000))
                for row in turn["output"]:
                    if row.get("status") == "running":
                        row["status"] = "canceled" if stopping else "unknown"
                # Classify known launch failures; never expose arbitrary stderr,
                # which can contain provider credentials or private configuration.
                stderr_tail = b"".join(diagnostics)
                session_busy = not turn["run_id"] and b"another CLI owns this conversation;" in stderr_tail
                busy = session_busy or (not turn["run_id"] and b"another host owns this state home:" in stderr_tail)
                if busy and not stopping:
                    failure = ("此对话正在由另一项任务处理，本次尚未开始。其他独立对话可并发执行；请在本轮结束后手动继续。" if session_busy else
                               "状态目录正被维护操作或旧版 CLI 独占，本次尚未开始。新版 CLI 的独立对话支持并发；请在独占操作结束后手动发送。")
                    turn["error_code"] = "session_busy" if session_busy else "state_busy"
                elif not failure and child and child.returncode and not turn["run_id"]:
                    failure = f"Host 启动失败（退出码 {child.returncode}），尚未生成执行记录。请检查 CLI 配置或服务日志。"
                turn["error"] = failure or ("执行未正常完成，请查看执行记录或检查模型连接。" if turn["status"] == "failed" else "")
                if turn["status"] == "completed":
                    self.session = turn["session_id"] or None
                self.children.pop(turn["id"], None)
                self.workers.pop(turn["id"], None)
                try:
                    self._persist()
                except (OSError, ValueError):
                    turn["error"] += " 页面收据保存失败，请保留执行记录编号。"

    def close(self):
        with self.lock:
            self.closed = True
            for turn in self.turns:
                if turn["status"] in ("starting", "running"):
                    self.stop(turn["id"])
            workers = list(self.workers.values())
        for worker in workers:
            worker.join()
