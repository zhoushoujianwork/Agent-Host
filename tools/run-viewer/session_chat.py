"""Owner-local projection of an explicitly selected, existing Host session."""

import hashlib
import http.client
from datetime import datetime
from pathlib import Path
import sys

from chat_runtime import ChatRuntime, ChatConflict, agent_destination, configured_launch
import run_control


class SessionChatRuntime(ChatRuntime):
    attached = True

    def __init__(self, source, key, config, launcher=configured_launch, selected=None):
        selected = selected or source.record(key)["run"]
        self.source, self.key = source, key
        self.agent_id = selected.get("agent_id") or "default"
        self.source_home = source.database.parent.resolve()
        try:
            self.local = bool(config and Path(agent_destination(config)["state_home"]).resolve() == self.source_home)
        except (OSError, ValueError, KeyError, TypeError):
            self.local = False
        self.original_run = selected["id"]
        self.context_id = hashlib.sha256((str(self.source_home) + selected["session_id"]).encode()).hexdigest()
        def launch(path):
            if self.local:
                command, cwd, env, identity = launcher(config, self.agent_id)
                if Path(command[command.index("--home") + 1]).resolve() != self.source_home:
                    raise ChatConflict("原会话的执行目标已变化。")
                return command, cwd, env, identity
            previous = next((t["run_id"] for t in reversed(self.turns) if t["status"] == "completed" and t["run_id"]), self.original_run)
            return [sys.executable, "-B", str(Path(__file__).with_name("session_client.py")), "--home", str(self.source_home), "--run", previous], str(Path(__file__).parent), {}, self.context_id
        super().__init__(config or source.database, launch)
        self.state_home = self.source_home
        self.takeover_session = selected["session_id"]
        self.stop_requests = set()

    def snapshot(self):
        with self.lock:
            records = self.source.session_history(self.key)
            history = []
            projection = ChatRuntime(None)
            for record in records:
                run = record["run"]
                if any(t["run_id"] == run["id"] for t in self.turns):
                    continue
                turn = {"id": "history-" + run["id"], "request_id": "history-" + run["id"],
                        "input": run["input"], "text": "", "output": [], "run_id": run["id"],
                        "session_id": run["session_id"], "status": run["execution_status"],
                        "execution_status": run["execution_status"], "memory_status": run["memory_status"],
                        "model": run.get("model"), "execution_error": run.get("error", ""),
                        "memory_error": run.get("memory_error", ""), "experience_error": run.get("experience_error", ""),
                        "read_only": True}
                for event in record["events"]:
                    projection._event(turn, event)
                    if event.get("type") == "run.finished":
                        turn["status"] = event.get("status", turn["status"])
                finished = any(e.get("type") == "run.finished" for e in record["events"])
                controllable = not finished and run_control.available(self.source_home, run["id"])
                if controllable and turn["status"] not in {"starting", "running", "stopping"}:
                    turn["status"] = "running"  # Execution may be done while memory is still finalizing.
                active = turn["status"] in {"starting", "running", "stopping"}
                turn["can_stop"] = active and controllable
                if active and run["id"] in self.stop_requests:
                    turn["status"] = "stopping"
                if active and run.get("created_at"):
                    started = datetime.fromisoformat(run["created_at"].replace("Z", "+00:00"))
                    turn["elapsed_ms"] = max(0, int((datetime.now(started.tzinfo) - started).total_seconds() * 1000))
                if not turn["text"]:
                    turn["text"] = run.get("output", "")
                history.append(turn)
            result = super().snapshot()
            busy = any(t["session_id"] == self.takeover_session and t["status"] in {"starting", "running", "stopping"} for t in history)
            unavailable = not self.local and not (self.source_home / "memory-control.sock").exists()
            turns = history + result["turns"]
            active_ids = [t["id"] for t in turns if t["status"] in {"starting", "running", "stopping"}]
            result.update(turns=turns, context_id=self.context_id,
                          active_ids=active_ids, active_id=next(iter(active_ids), None),
                          selected_session=self.takeover_session, max_concurrent=1,
                          needs_new=busy or unavailable, enabled=not self.closed,
                          notice=("正在实时同步原会话，可中止当前执行。" if any(t.get("can_stop") for t in history) else
                                  "正在实时同步原会话；此执行未提供终止接口，请在原入口停止。") if busy else
                          "原会话服务未连接，请启动对应服务后继续。" if unavailable else
                          "已接入原会话上下文；新回复显示在 Web，保留原 Agent、项目和会话范围。")
            return result

    def stop(self, turn_id):
        with self.lock:
            if any(t["id"] == turn_id for t in self.turns):
                return super().stop(turn_id)
            turn = next((t for t in self.snapshot()["turns"] if t["id"] == turn_id), None)
            if turn is None:
                raise KeyError("execution not found in selected session")
            if turn["status"] not in {"starting", "running", "stopping"}:
                return
            try:
                run_control.stop(self.source_home, turn["run_id"])
            except (OSError, ValueError, http.client.HTTPException) as error:
                raise ChatConflict("无法确认中止请求，请刷新执行记录；原运行可能已结束或未提供控制接口。") from error
            self.stop_requests.add(turn["run_id"])

    def start(self, request_id, message):
        with self.lock:
            if any(t["request_id"] == request_id for t in self.turns):
                return super().start(request_id, message)
            snapshot = self.snapshot()
            if snapshot["active_id"] or snapshot["needs_new"]:
                raise ChatConflict("原会话正在执行或服务不可用，请等待后继续。")
            return super().start(request_id, message)
