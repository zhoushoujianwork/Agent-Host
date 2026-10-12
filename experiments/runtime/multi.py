#!/usr/bin/env python3
"""Exercise joint native writes, scoped recall and compatible session isolation.

Uses synthetic projects and private temporary Host/native state. No platform
messages or production project files are touched. Credentials stay in process env.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import uuid

REPO = Path(__file__).resolve().parents[2]


def require(value, message):
    if not value:
        raise AssertionError(message)


def native_script_passed(events, project):
    calls = {e["data"]["tool"]["id"] for e in events if e["type"] == "tool.activity"
             and e.get("data", {}).get("tool", {}).get("name") == "Bash"
             and e["data"]["tool"].get("stage") == "call"
             and project + "/check.py" in e["data"]["tool"].get("input", {}).get("command", "")}
    return any(e["type"] == "tool.activity" and e.get("data", {}).get("tool", {}).get("id") in calls
               and e["data"]["tool"].get("stage") == "result" and not e["data"]["tool"].get("is_error")
               and "PASS_" + project.upper() in str(e["data"]["tool"].get("result", "")) for e in events)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    require(not args.output.exists(), "choose a fresh report path")
    runtime = args.runtime_root.expanduser().resolve()
    config = json.loads((runtime / "config/service.json").read_text())
    providers = json.loads((runtime / "config/environment.json").read_text())
    env = os.environ.copy()
    for key in ("CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
                "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"):
        env.pop(key, None)
    env.update(providers)
    secrets = [v for k, v in env.items() if any(s in k.upper() for s in ("TOKEN", "SECRET", "API_KEY", "AUTH_KEY"))]
    binary = REPO / "bin/agent-host"
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "native_version": subprocess.check_output(["rtk", config["claude"], "--version"], text=True).strip(),
              "requested_claude_model": config["model"], "provider": config["jev_provider"],
              "isolation": "two synthetic checkouts, temporary Host/native state, no platform messages",
              "commands": [], "runs": [], "assertions": [], "passed": False, "retries": 0}
    redactions = []

    def save():
        encoded = json.dumps(report, ensure_ascii=False, indent=2)
        for secret in secrets:
            if secret:
                encoded = encoded.replace(secret, "[REDACTED]")
        for path in sorted(redactions, key=len, reverse=True):
            encoded = encoded.replace(path, "<isolated-root>")
        encoded = re.sub(r'/([^/"\\\s]+/)*agent-host-extraction-[0-9]+', '<isolated-extraction-cwd>', encoded)
        require(not any(s and s in encoded for s in secrets), "secret remained in report")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded + "\n")

    try:
        with tempfile.TemporaryDirectory(prefix="agent-host-multi-") as temporary:
            root = Path(temporary).resolve()
            redactions += [temporary, str(root)]
            home, native = root / "host", root / "native"
            native.mkdir()
            (native / "settings.json").write_text(json.dumps({"disableAllHooks": True}))
            env["CLAUDE_CONFIG_DIR"] = str(native)
            projects, rule_files = {}, {}
            for id, entry in (("alpha", "CLAUDE.md"), ("beta", "AGENTS.md")):
                path = root / id
                path.mkdir()
                marker = "MULTI_RULE_" + id.upper()
                rules = f"# Synthetic project\nFor files within {path}, result.txt must contain exactly {marker} followed by one newline. This convention applies only to this project's files.\n"
                (path / entry).write_text(rules)
                rule_files[path / entry] = rules
                (path / "check.py").write_text(f"from pathlib import Path\nassert Path(__file__).with_name('result.txt').read_text() == {marker!r} + '\\n'\nprint('PASS_{id.upper()}')\n")
                projects[id] = path

            def invoke(label, words, live=False):
                start = time.monotonic()
                proc = subprocess.run(["rtk", str(binary), *words], cwd=root, env=env, text=True,
                                      capture_output=True, timeout=420)
                events = [json.loads(line) for line in proc.stdout.splitlines() if line.strip()] if live else []
                report["commands"].append({"label": label, "arguments": words, "events": events,
                    "output": "" if live else proc.stdout, "stderr": proc.stderr, "exit_code": proc.returncode,
                    "elapsed_seconds": round(time.monotonic() - start, 2)})
                save()
                require(proc.returncode == 0, label + " failed; inspect retained evidence")
                return events if live else json.loads(proc.stdout)

            for id, path in projects.items():
                invoke("register-" + id, ["project", "add", "--home", str(home), "--id", id, "--workspace", str(path)])

            def turn(label, ids, message, resume=None, primary=None):
                words = ["chat", "--home", str(home), "--format", "json", "--claude", config["claude"],
                    "--model", config["model"], "--jev-provider", config["jev_provider"], "--jev-model", config["jev_model"],
                    "--allowed-tools", "Read,Write,Bash(python3 *)", "--timeout", "6m", "-m", message]
                for id in ids:
                    words += ["--project", id]
                if resume:
                    words += ["--resume", resume]
                if primary:
                    words += ["--primary-project", primary]
                events = invoke(label, words, True)
                started = next(e["data"] for e in events if e["type"] == "run.started")
                receipt = next(e for e in reversed(events) if e["type"] == "run.finished")
                init = next(e["data"] for e in events if e["type"] == "agent.started")
                result = next(e["data"]["raw"].get("result", "") for e in events if e["type"] == "agent.result")
                recalled = next(e["data"].get("memories") or [] for e in events if e["type"] == "memory.recalled")
                row = {"label": label, "run_id": receipt["run_id"], "session_id": receipt["session_id"],
                    "native_session_id": init["native_session_id"], "output": result, "receipt": receipt["data"],
                    "recalled": recalled, **started}
                report["runs"].append(row)
                save()
                require(receipt["data"]["execution_status"] == "completed", label + " incomplete")
                require(init["raw"]["cwd"] == started["workspace"], "actual native cwd mismatch")
                require({p["id"] for p in started["projects"]} == set(ids), "project membership mismatch")
                require(all(m["scope"] in {"project:"+id for id in ids} for m in recalled), "unselected memory recalled")
                if len(ids) > 1:
                    require(receipt["data"]["memory_status"] == "skipped" and receipt["data"]["experience_status"] == "skipped", "joint run published new memory")
                print(label + ": completed", flush=True)
                return row

            secret_a, secret_joint = "ONLY_A_"+uuid.uuid4().hex, "ONLY_AB_"+uuid.uuid4().hex
            a_seed = turn("a-baseline", ["alpha"], "This project's release codename is LILY-ALPHA-MULTI.")
            b = turn("b-baseline", ["beta"], "This project's release codename is LILY-BETA-MULTI.")
            require(a_seed["receipt"]["memory_status"] == "saved" and b["receipt"]["memory_status"] == "saved", "fixture declarations were not admitted")
            a = turn("a-native-only-marker", ["alpha"], "For this native conversation only, remember " + secret_a + ". Do not retain this as durable memory. Reply briefly.", a_seed["session_id"])
            joint = turn("joint-write-and-tests", ["alpha", "beta"],
                f"Work jointly on alpha and beta, equally. Create {projects['alpha']}/result.txt and {projects['beta']}/result.txt using each project's own rules, then run python3 {projects['alpha']}/check.py and python3 {projects['beta']}/check.py separately. Report their observed output. For this native conversation only, remember {secret_joint}. Do not edit any other files.")
            require(joint["project_id"] == "" and len(joint["additional_directories"]) == 2, "symmetric task did not use neutral cwd")
            for id, path in projects.items():
                require((path / "result.txt").read_text() == "MULTI_RULE_" + id.upper() + "\n", "wrong per-project write convention")
                subprocess.run(["rtk", "python3", str(path / "check.py")], check=True, capture_output=True)
            events = report["commands"][-1]["events"]
            require(all(native_script_passed(events, id) for id in projects), "independent successful native Bash test output not observed")
            continued = turn("joint-resume", ["beta", "alpha"], "Report the conversation-only ONLY_AB marker from earlier. Do not read files.", joint["session_id"])
            require(continued["native_session_id"] == joint["native_session_id"] and secret_joint in continued["output"], "joint native continuation failed")
            returned = turn("return-to-a", ["alpha"], "Report the conversation-only ONLY_A marker. Also report an ONLY_AB marker only if it is actually present in this conversation. Do not read files.", a["session_id"])
            require(returned["native_session_id"] == a["native_session_id"] and secret_a in returned["output"], "A did not resume")
            require(secret_joint not in returned["output"], "joint-only native context leaked into A")
            fresh = turn("fresh-joint-recall", ["alpha", "beta"], "What are alpha and beta's release codenames? Use current Host memories and cite each memory ID. Do not read files.")
            require(fresh["native_session_id"] != joint["native_session_id"], "fresh joint resumed an old session")
            require({m["scope"] for m in fresh["recalled"]} == {"project:alpha", "project:beta"}, "joint scope recall incomplete")
            require(all(any(m["id"] in fresh["output"] for m in fresh["recalled"] if m["scope"] == "project:"+id) for id in projects), "joint answer lost source attribution")
            for path, rules in rule_files.items():
                require(path.read_text() == rules, "project rules were edited")
            require(not list(native.glob("projects/**/memory/*")), "native automatic memory wrote files")
            report["assertions"] = ["native cwd and all selected directories agree with saved assembly", "different CLAUDE/AGENTS rules coexist in joint controlled writes", "each project's test runs natively with observed output", "AB resumes independently of argument order", "A to AB to A retains A context without joint-only context", "fresh AB recalls and cites both existing project scopes", "joint memory and extraction are skipped; raw evidence remains", "rules unchanged and native automatic memory disabled"]
            report["passed"] = True
    except Exception as error:
        report["failure"] = str(error)
    finally:
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save()
    print("multi-project native acceptance: " + ("passed" if report["passed"] else "failed"), flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
