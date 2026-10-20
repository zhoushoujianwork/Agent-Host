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
PHASES = {"recall": "检索记忆", "execute": "AI 执行", "remember": "判断长期记忆",
          "persist": "保存记忆", "evidence": "整理执行证据", "extract": "提取经验",
          "assess": "评估经验", "candidates": "保存经验候选",
          "knowledge_search": "检索知识", "knowledge_read": "阅读资料"}


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
                    "text": "", "steps": [], "status": "starting", "error": "",
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

    def _event(self, turn, event):
        with self.lock:
            kind = event.get("type")
            if kind == "run.started":
                turn["run_id"] = str(event.get("run_id", ""))
                turn["session_id"] = str(event.get("session_id", ""))
            if kind == "text.delta" and event.get("agent") != "memory-extractor":
                delta = event.get("delta", "")
                if not isinstance(delta, str) or len(turn["text"]) + len(delta) > 512 * 1024:
                    raise ValueError("回答超过页面显示上限，请查看执行记录。")
                turn["text"] += delta
            phase = event.get("phase", "")
            if kind in ("phase.started", "phase.resumed", "phase.finished") and phase in PHASES:
                previous = next((s for s in reversed(turn["steps"]) if s.get("phase") == phase and s["status"] == "running"), None)
                if kind == "phase.finished" and previous:
                    previous["status"] = event.get("status", "unknown")
                else:
                    turn["steps"].append({"label": PHASES[phase], "phase": phase, "status": event.get("status", "running")})
            if kind == "tool.activity":
                tool = (event.get("data") or {}).get("tool") or {}
                if not tool.get("name"):
                    return None
                status = "failed" if tool.get("is_error") else "completed" if tool.get("stage") == "result" else "running"
                previous = next((s for s in reversed(turn["steps"]) if tool.get("id") and s.get("tool_id") == tool["id"]), None)
                if previous:
                    previous["status"] = status
                else:
                    turn["steps"].append({"label": str(tool.get("name") or "工具调用")[:120],
                                          "tool_id": tool.get("id"), "status": status})
            if len(turn["steps"]) > 200:
                turn["steps"] = turn["steps"][-200:]
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
                for step in turn["steps"]:
                    if step["status"] == "running":
                        step["status"] = "canceled" if stopping else "unknown"
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
