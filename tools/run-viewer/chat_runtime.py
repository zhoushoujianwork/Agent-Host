"""Bridge one owner-local Web conversation to the configured Host CLI.

Host owns execution, native sessions and durable evidence. This bridge only
launches the validated command, projects its public events and requests cancel.
"""

import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import uuid

from settings_store import private_bytes

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


def configured_launch(path):
    value = json.loads(private_bytes(path))
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
    managed = json.loads(private_bytes(Path(value["environment_file"])))
    if not managed.keys() <= PROVIDER_KEYS or not all(isinstance(v, str) and v and
            v.strip() == v and not any(c in v for c in "\x00\r\n") for v in managed.values()):
        raise ValueError("Chat 模型环境配置无效。")
    env = {k: v for k, v in os.environ.items() if k not in PROVIDER_KEYS and
           not k.startswith(("ANTHROPIC_", "CLAUDE_CODE_")) and k not in ("CLAUDE_CONFIG_DIR", "CLAUDECODE")}
    env.update(managed)
    command, cwd = result["command"], result["cwd"]
    identity = hashlib.sha256(json.dumps([command, cwd, managed], sort_keys=True).encode()).hexdigest()
    return command, cwd, env, identity


class ChatConflict(ValueError):
    pass


class ChatRuntime:
    def __init__(self, config, launcher=configured_launch):
        self.config = Path(config) if config else None
        self.launcher = launcher
        self.lock = threading.RLock()
        self.turns = []
        self.active = None
        self.child = None
        self.worker = None
        self.session = None
        self.identity = None
        self.needs_new = False
        self.closed = False
        self.state_home = None
        self.native_indexes = {}
        self.native_messages = {}

    def snapshot(self):
        with self.lock:
            return json.loads(json.dumps({"enabled": self.config is not None,
                "notice": "" if self.config else "尚未配置 Chat 执行环境，请配置此 Host 的 CLI 连接。",
                "active_id": self.active, "needs_new": self.needs_new, "turns": self.turns}))

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
            if self.active:
                raise ChatConflict("已有任务正在执行，请等待或中止。")
            if self.needs_new:
                raise ChatConflict("上一轮未正常结束，请先开启新对话；不会自动重试。")
            if len(self.turns) >= 20:
                raise ChatConflict("此对话已达 20 轮，请开启新对话。")
            turn = {"id": uuid.uuid4().hex, "request_id": request_id, "input": message,
                    "text": "", "output": [], "status": "starting", "error": "",
                    "run_id": "", "session_id": "", "exit_code": None}
            self.turns.append(turn)
            self.active = turn["id"]
            self.worker = threading.Thread(target=self._run, args=(turn,), daemon=True)
            self.worker.start()
            return turn["id"]

    def reset(self):
        with self.lock:
            if self.active:
                raise ChatConflict("请先中止当前执行，再开启新对话。")
            self.turns = []
            self.session = self.identity = None
            self.native_indexes = {}
            self.native_messages = {}
            self.needs_new = False

    def stop(self, turn_id):
        with self.lock:
            turn = next((t for t in self.turns if t["id"] == turn_id), None)
            if turn is None:
                raise KeyError("execution not found")
            if self.active != turn_id or turn["status"] == "stopping":
                return
            if self.child and self.child.poll() is not None:
                return
            turn["status"] = "stopping"
            if self.child and self.child.poll() is None:
                # Host's signal context cancels its native process group and
                # persists the interrupted outcome before exiting.
                try:
                    self.child.send_signal(signal.SIGTERM)
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
            if kind == "run.started":
                turn["run_id"] = str(event.get("run_id", ""))
                turn["session_id"] = str(event.get("session_id", ""))
            # Only the business Claude stream belongs in Chat. Host memory and
            # experience processing retain their evidence in the inspector.
            if event.get("agent") == "memory-extractor" or event.get("phase") not in (None, "", "execute", "knowledge_search", "knowledge_read"):
                return event.get("status") if kind == "run.finished" else None
            if kind == "text.delta":
                self._text(turn, event.get("delta", ""))
            if kind in {"native.evidence", "agent.started", "agent.result", "tool.activity"}:
                self._native(turn, event)
            if kind == "tool.activity":
                tool = (event.get("data") or {}).get("tool") or {}
                tool_id, name = tool.get("id"), tool.get("name")
                if tool_id and name and tool.get("stage") == "call":
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

    def _run(self, turn):
        finished = None
        failure = ""
        child = None
        try:
            command, cwd, env, identity = self.launcher(self.config)
            with self.lock:
                if turn["status"] == "stopping":
                    return
                if self.session and self.identity != identity:
                    raise ValueError("执行配置已变化，请开启新对话。")
                if self.session:
                    command = [*command, "--resume", self.session]
                command = [*command, "--format", "json", "-m", turn["input"]]
                child = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
                self.child = child
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
            with self.lock:
                stopping = turn["status"] == "stopping"
                turn["exit_code"] = child.returncode if child else None
                completed = not failure and child and child.returncode == 0 and finished == "completed"
                turn["status"] = "completed" if completed else "canceled" if stopping else "failed"
                for row in turn["output"]:
                    if row.get("status") == "running":
                        row["status"] = "canceled" if stopping else "unknown"
                turn["error"] = failure or ("执行未正常完成，请查看执行记录或检查模型连接。" if turn["status"] == "failed" else "")
                if turn["status"] == "completed":
                    self.session = turn["session_id"] or None
                else:
                    self.needs_new = True
                self.child = None
                self.active = None

    def close(self):
        with self.lock:
            self.closed = True
            if self.active:
                self.stop(self.active)
            worker = self.worker
        if worker:
            worker.join()
