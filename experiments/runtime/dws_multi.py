#!/usr/bin/env python3
"""Bounded owner-DM acceptance through dws and the existing application bot.

Requires explicit sending authorization. Stops the managed receiver, exercises
an isolated Host, then restores production with make install. Uses no IM history
polling; a future-event listener observes actual bot replies.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import sqlite3
import subprocess
import time

REPO = Path(__file__).resolve().parents[2]


def require(ok, reason):
    if not ok:
        raise AssertionError(reason)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--production", type=Path, required=True)
    parser.add_argument("--isolated-root", type=Path, required=True)
    parser.add_argument("--profile", required=True)
    parser.add_argument("--bot-id", required=True)
    parser.add_argument("--bot-name", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    production = args.production.expanduser().resolve()
    root = args.isolated_root.expanduser()
    require(not root.exists() and not args.output.exists(), "use fresh acceptance paths")
    root.mkdir(mode=0o700, parents=True)
    root = root.resolve()
    for part in ("config", "state", "workspace", "native", "logs", "alpha", "beta"):
        (root / part).mkdir(mode=0o700)
    for name in ("service.json", "environment.json", "dingtalk.json"):
        shutil.copyfile(production / "config" / name, root / "config" / name)
        (root / "config" / name).chmod(0o600)
    config = json.loads((root / "config/service.json").read_text())
    config.pop("knowledge_config", None)
    config["allowed_tools"] = ["Read"]
    (root / "config/service.json").write_text(json.dumps(config))
    (root / "native/settings.json").write_text(json.dumps({"disableAllHooks": True}))
    for id, entry in (("alpha", "CLAUDE.md"), ("beta", "AGENTS.md")):
        (root / id / entry).write_text("# Synthetic acceptance rules\nThis project's instruction marker is DT_MULTI_" + id.upper() + ". Always include it when asked about this project. Do not edit files.\n")
        (root / id / "README.md").write_text("Synthetic " + id + " login project.\n")
    binary = REPO / "bin/agent-host"
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "target": {"name": args.bot_name, "open_dingtalk_id": args.bot_id, "profile": args.profile},
              "isolation": "synthetic checkouts and private Host/native state; same bot, one active receiver",
              "messages": [], "commands": [], "runs": [], "assertions": [], "passed": False}
    service, listener, stopped = None, None, False
    logs = []

    def save():
        (root / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2)+"\n")
        encoded = json.dumps(report, ensure_ascii=False, indent=2).replace(str(root), "<isolated-runtime>")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded+"\n")

    def command(words, timeout=60):
        result = subprocess.run(["rtk", *map(str, words)], cwd=REPO, text=True, capture_output=True, timeout=timeout)
        require(result.returncode == 0, "command failed: " + str(words[:3]) + " " + result.stderr[:500])
        return json.loads(result.stdout)

    def connect():
        nonlocal service
        log = open(root / "logs/service.ndjson", "a")
        logs.append(log)
        offset = (root / "logs/service.ndjson").stat().st_size
        service = subprocess.Popen(["rtk", str(binary), "service", "run", "--root", str(root)],
                                   cwd=root / "workspace", stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        deadline = time.monotonic()+40
        while time.monotonic() < deadline:
            require(service.poll() is None, "isolated service exited before connection")
            fresh_log = (root / "logs/service.ndjson").read_bytes()[offset:].decode("utf-8", errors="replace")
            if "DingTalk Stream connection established" in fresh_log:
                return
            time.sleep(.2)
        raise TimeoutError("isolated stream connection missing")

    def stop(proc):
        if proc and proc.poll() is None:
            os.killpg(proc.pid, signal.SIGTERM)
            proc.wait(timeout=40)

    def database(runtime=root):
        connection = sqlite3.connect(f"file:{runtime}/state/state.db?mode=ro", uri=True)
        connection.row_factory = sqlite3.Row
        return connection

    def send(label, text):
        result = command(["dws", "chat", "+messages-send", "--as", "user", "--open-dingtalk-id", args.bot_id,
                          "--text", text, "--idempotency-key", "AH-MULTI-"+root.name+"-"+label,
                          "--profile", args.profile, "--format", "json", "--yes"])
        require(result.get("ok") and result["result"].get("success"), "DWS send not accepted")
        delivery = command(["dws", "chat", "+messages-query-send-status", "--open-task-id", result["sendReceipt"]["openTaskId"],
                            "--profile", args.profile, "--format", "json"])
        require(delivery.get("readyForMessageActions") and delivery["result"]["sendStatus"] == "SUCCESS", "source send status unknown")
        row = {"label": label, "text": text, "send": result, "delivery": delivery}
        report["messages"].append(row)
        save()
        print("sent " + label, flush=True)
        return row

    def wait(row, runtime=root):
        deadline = time.monotonic()+420
        while time.monotonic() < deadline:
            with database(runtime) as c:
                found = c.execute("SELECT payload FROM channel_messages WHERE message_id=?", (row["delivery"]["messageRef"]["openMessageId"],)).fetchone()
                if found:
                    message = json.loads(found[0])
                    if message.get("reply_status") == "accepted" and message["status"] in ("awaiting", "completed", "failed", "unknown", "interrupted"):
                        row["host"] = message
                        row["reactions"] = [dict(r) for r in c.execute("SELECT * FROM channel_reactions WHERE channel_message_id=?", (message["id"],))]
                        if message.get("run_id"):
                            row["run"] = json.loads(c.execute("SELECT payload FROM runs WHERE id=?", (message["run_id"],)).fetchone()[0])
                            export_run(c, row["label"], row["run"])
                        save()
                        print(row["label"] + ": " + message["status"], flush=True)
                        return row
            time.sleep(.25)
        raise TimeoutError("result unknown for " + row["label"] + "; task not replayed")

    def export_run(connection, label, run):
        if any(r["run_id"] == run["id"] for r in report["runs"]):
            return
        events = [json.loads(r[0]) for r in connection.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (run["id"],))]
        report["runs"].append({**run, "run_id": run["id"], "label": label, "recalled": run.get("context", [])})
        report["commands"].append({"label": label, "arguments": [], "events": events})

    def task(label, text):
        row = wait(send(label, text))
        require(row["host"]["status"] == "completed" and row.get("run", {}).get("execution_status") == "completed", label + " did not execute")
        return row

    spec = importlib.util.spec_from_file_location("installer", REPO / "tools/service/install.py")
    installer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(installer)
    try:
        for id in ("alpha", "beta"):
            command([binary, "project", "add", "--home", root / "state", "--id", "dt-"+id,
                     "--workspace", root / id, "--description", "Login engineering tests", "--alias", "验证"+id])
        command([binary, "service", "check", "--root", root])
        with database(production) as c:
            require(c.execute("SELECT count(*) FROM channel_messages WHERE status IN ('queued','claimed')").fetchone()[0] == 0, "production has active work")
        stopped = True
        installer.stop_existing(f"gui/{os.getuid()}/{installer.LABEL}")
        with sqlite3.connect(production / "state/state.db") as before, sqlite3.connect(root / "production-before.db") as backup:
            before.backup(backup)
        connect()
        stream, diagnostic = open(root / "logs/dws.ndjson", "w"), open(root / "logs/dws.stderr", "w")
        logs += [stream, diagnostic]
        listener = subprocess.Popen(["rtk", "dws", "event", "+listen-im", "--kind", "sender", "--open-dingtalk-id", args.bot_id,
                       "--events", "message,reaction", "--duration", "20m", "--profile", args.profile, "--format", "ndjson"],
                       stdout=stream, stderr=diagnostic, start_new_session=True)
        deadline = time.monotonic()+40
        while time.monotonic() < deadline:
            require(listener.poll() is None, "future listener exited before ready")
            if "[event] ready" in (root / "logs/dws.stderr").read_text():
                break
            time.sleep(.2)
        else:
            raise TimeoutError("future listener did not become ready")
        wait(send("auto", "/routing auto"))
        a = task("a", "dt-alpha：使用 Read 读取 README.md，报告本项目规则标记。这个原生会话的临时标记是 DT_ONLY_A_20261010，不要保存为记忆。")
        b = task("b", "dt-beta：使用 Read 读取 README.md，报告本项目规则标记。")
        automatic_joint = task("auto-joint", "比较 dt-alpha 和 dt-beta 的 README.md，报告各自项目规则标记，不要修改文件。")
        require({p["id"] for p in automatic_joint["run"]["projects"]} == {"dt-alpha", "dt-beta"}
                and len(automatic_joint["run"]["additional_directories"]) == 2, "automatic routing omitted a project")
        require(all("DT_MULTI_"+id.upper() in automatic_joint["run"]["output"] for id in ("alpha", "beta")), "automatic joint rules missing")
        pending = wait(send("ambiguous", "修复登录测试。"))
        require(pending["host"]["status"] == "awaiting" and not pending["host"].get("run_id"), "ambiguous request executed")
        with database() as c:
            choice = json.loads(c.execute("SELECT payload FROM channel_choices WHERE channel_message_id=?", (pending["host"]["id"],)).fetchone()[0])
        stop(service)
        connect()
        chosen = wait(send("choose", "/choose "+choice["id"]+" dt-alpha dt-beta -- 只读检查两个 README.md 并报告各自规则，不要修改文件。"))
        with database() as c:
            original = json.loads(c.execute("SELECT payload FROM channel_messages WHERE id=?", (pending["host"]["id"],)).fetchone()[0])
            joint = json.loads(c.execute("SELECT payload FROM runs WHERE id=?", (original["run_id"],)).fetchone()[0])
            export_run(c, "clarified-joint", joint)
        require(chosen["host"]["status"] == "completed" and joint["execution_status"] == "completed", "clarified joint execution failed")
        require(joint["input"] == pending["text"] and joint["supplements"][0]["message_id"] == chosen["host"]["message_id"], "clarification source changed")
        require(joint["supplements"][0]["text"] == "只读检查两个 README.md 并报告各自规则，不要修改文件。", "clarification restriction changed")
        require({p["id"] for p in joint["projects"]} == {"dt-alpha", "dt-beta"} and len(joint["additional_directories"]) == 2, "confirmation did not form joint scope")
        require(joint["memory_status"] == "skipped", "joint memory not skipped")
        require(all("DT_MULTI_"+id.upper() in joint["output"] for id in ("alpha", "beta")), "joint rules not loaded")
        with database() as c:
            count_before_duplicate = c.execute("SELECT count(*) FROM runs").fetchone()[0]
        duplicate = wait(send("duplicate-choose", "/choose "+choice["id"]+" dt-alpha dt-beta"))
        require(not duplicate["host"].get("run_id"), "duplicate confirmation executed")
        with database() as c:
            require(c.execute("SELECT count(*) FROM runs").fetchone()[0] == count_before_duplicate, "duplicate confirmation added a run")
            require(json.loads(c.execute("SELECT payload FROM channel_messages WHERE id=?", (pending["host"]["id"],)).fetchone()[0])["run_id"] == joint["id"], "duplicate confirmation replaced original run")
        returned = task("return-a", "dt-alpha：报告本原生会话先前收到的 DT_ONLY_A 临时标记，不要读取文件。")
        require(returned["run"]["native_session_id"] == a["run"]["native_session_id"] and "DT_ONLY_A_20261010" in returned["run"]["output"], "A to AB to A continuation failed")
        switch = send("pin-joint", "/project dt-alpha dt-beta")
        fast = send("fast-task", "报告两个项目的规则标记，不要修改文件。")
        wait(switch)
        wait(fast)
        require(fast["run"]["native_session_id"] == joint["native_session_id"], "ordered pin/task did not resume joint scope")
        wait(send("new-joint", "/new"))
        fresh = task("fresh-joint", "报告两个项目的规则标记，不要修改文件。")
        require(fresh["run"]["native_session_id"] != joint["native_session_id"], "/new did not reset selected set")
        os.rename(root / "beta", root / "beta-missing")
        try:
            missing = wait(send("missing-extra", "读取两个项目 README.md。"))
            require(missing["host"]["status"] == "failed" and not missing["host"].get("run_id"), "missing add-dir executed or fell back")
        finally:
            os.rename(root / "beta-missing", root / "beta")
        wait(send("auto-again", "/routing auto"))
        general = task("general", "请写三条不关联任何工程或已有任务的通用会议检查清单。")
        require(general["run"]["workspace"] == str(root / "workspace") and not general["run"].get("projects"), "general request inherited a project")
        pending_cancel = wait(send("pending-cancel", "修复登录测试。"))
        require(pending_cancel["host"]["status"] == "awaiting", "cancellation fixture not ambiguous")
        with database() as c:
            cancel_id = c.execute("SELECT id FROM channel_choices WHERE channel_message_id=?", (pending_cancel["host"]["id"],)).fetchone()[0]
            count_before_cancel = c.execute("SELECT count(*) FROM runs").fetchone()[0]
        canceled = wait(send("cancel", "/cancel "+cancel_id))
        require(canceled["host"]["status"] == "completed", "cancel command failed")
        with database() as c:
            require(c.execute("SELECT status FROM channel_choices WHERE id=?", (cancel_id,)).fetchone()[0] == "canceled", "choice not canceled")
            original = json.loads(c.execute("SELECT payload FROM channel_messages WHERE id=?", (pending_cancel["host"]["id"],)).fetchone()[0])
            require(not original.get("run_id") and c.execute("SELECT count(*) FROM runs").fetchone()[0] == count_before_cancel, "canceled task executed")
        deadline = time.monotonic()+30
        while time.monotonic() < deadline:
            with database() as c:
                if not c.execute("SELECT count(*) FROM channel_reactions WHERE status IN ('ready','sending') OR removal_status='sending'").fetchone()[0]:
                    report["final_reactions"] = [dict(r) for r in c.execute("SELECT * FROM channel_reactions ORDER BY rowid")]
                    break
            time.sleep(.2)
        else:
            raise TimeoutError("reaction deliveries did not settle independently")
        report["joint_run"] = joint
        report["assertions"] = ["automatic single/multiple routing", "durable clarification survives restart and retains source/supplement", "duplicate confirmation does not rerun", "A to AB to A restores A native session", "manual switch/task FIFO and joint /new", "missing extra directory fails before native execution", "general topic returns to configured default", "explicit cancellation without execution"]
        report["passed"] = True
    except Exception as error:
        report["failure"] = str(error)
        print("acceptance failure: "+str(error), flush=True)
    finally:
        for label, proc in (("service", service), ("listener", listener)):
            try:
                stop(proc)
            except Exception as error:
                report.setdefault("cleanup_errors", []).append(label+": "+str(error))
                report["passed"] = False
        for log in logs:
            try:
                log.close()
            except Exception as error:
                report.setdefault("cleanup_errors", []).append(str(error))
        if listener:
            try:
                observations = [json.loads(line) for line in (root / "logs/dws.ndjson").read_text().splitlines() if line.strip()]
                replies = [e for e in observations if e.get("type") == "user_im_message_receive_user"
                           and e.get("sender_open_dingtalk_id") == args.bot_id and e.get("content")]
                report["platform_observations"] = observations
                report["listener_exit_code"] = listener.returncode
                verified = {}
                for label in ("auto-joint", "ambiguous", "choose", "return-a", "fast-task", "fresh-joint", "missing-extra"):
                    row = next((r for r in report["messages"] if r["label"] == label), {})
                    expected = row.get("host", {}).get("reply_text")
                    verified[label] = bool(expected and any(e["content"] == expected for e in replies))
                report["observed_bot_replies"] = verified
                if report["passed"] and not all(verified.values()):
                    report["passed"], report["failure"] = False, "key bot replies not observed by future listener"
            except Exception as error:
                report["passed"], report["observation_error"] = False, str(error)
        if stopped:
            try:
                require(service is None or service.poll() is not None, "isolated receiver is still alive; cannot reconnect production concurrently")
                restored = subprocess.run(["rtk", "make", "install"], cwd=REPO, text=True, capture_output=True, timeout=120)
                report["production_restore"] = {"exit_code": restored.returncode, "stdout": restored.stdout, "stderr": restored.stderr}
                require(restored.returncode == 0, "production restore failed")
                wait(send("production-restored", "/project"), production)
                preserved = {}
                if (root / "production-before.db").exists():
                    with sqlite3.connect(root / "production-before.db") as before, database(production) as after:
                        for table in ("projects", "runs", "events", "sessions", "memories", "memory_revisions", "memory_sources", "candidates"):
                            present = before.execute("SELECT name FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone()
                            if present:
                                columns = [r[1] for r in before.execute("PRAGMA table_info("+table+")")]
                                query = "SELECT "+",".join(columns)+" FROM "+table+" ORDER BY rowid"
                                old, new = before.execute(query).fetchall(), [tuple(r) for r in after.execute(query)]
                                preserved[table] = {"rows": len(old), "original_columns_unchanged": old == new}
                    report["production_preserved"] = preserved
                    require(all(r["original_columns_unchanged"] for r in preserved.values()), "production evidence changed")
            except Exception as error:
                report["passed"], report["restore_error"] = False, str(error)
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save()
        restore_status = "production restored" if report.get("production_restore", {}).get("exit_code") == 0 and not report.get("restore_error") else "restoration not verified"
        print("DWS multi-project acceptance: "+("passed" if report["passed"] else "failed")+"; "+restore_status, flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
