#!/usr/bin/env python3
"""Validate two registered projects with isolated real Claude sessions.

Provider configuration is explicitly selected from an installed runtime. No
messages are sent to DingTalk and no production state or project files are used.
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

ROOT = Path(__file__).resolve().parents[2]


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def redact_extraction_directories(encoded):
    return re.sub(r'/([^/"\\\s]+/)*agent-host-extraction-[0-9]+',
                  '<isolated-extraction-cwd>', encoded)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    require(not args.output.exists(), "choose a fresh report path")
    runtime = args.runtime_root.expanduser().resolve()
    config = json.loads((runtime / "config/service.json").read_text())
    providers = json.loads((runtime / "config/environment.json").read_text())
    require(config["jev_provider"] == "patsnap", "this fixture requires the explicitly selected Patsnap runtime")
    require(providers.get("PATSNAP_API_KEY"), "selected runtime has no Patsnap credential")
    env = os.environ.copy()
    for name in ("CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
                 "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"):
        env.pop(name, None)
    env.update(providers)
    secrets = [str(value) for name, value in env.items()
               if any(part in name.upper() for part in ("TOKEN", "SECRET", "API_KEY", "AUTH_KEY"))]
    binary = ROOT / "bin/agent-host"
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "native_version": subprocess.check_output([config["claude"], "--version"], text=True).strip(),
              "requested_claude_model": config["model"], "provider": "patsnap",
              "isolation": "temporary Host state, two synthetic project directories and one isolated native profile; no platform messages",
              "commands": [], "runs": [], "assertions": [], "passed": False, "retries": 0}
    temporary_paths = []

    def clean(value):
        encoded = json.dumps(value, ensure_ascii=False)
        for secret in secrets:
            if secret:
                encoded = encoded.replace(secret, "[REDACTED]")
        for path in sorted(temporary_paths, key=len, reverse=True):
            encoded = encoded.replace(path, "<isolated-root>")
        return json.loads(redact_extraction_directories(encoded))

    def save():
        encoded = json.dumps(clean(report), ensure_ascii=False, indent=2) + "\n"
        require(not any(secret and secret in encoded for secret in secrets), "credential remained in report")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded)

    try:
        with tempfile.TemporaryDirectory(prefix="agent-host-runtime-") as temporary:
            root = Path(temporary).resolve()
            temporary_paths.extend((temporary, str(root)))
            home, native = root / "host", root / "native"
            native.mkdir()
            (native / "settings.json").write_text(json.dumps({"disableAllHooks": True, "autoMemoryEnabled": False}))
            env["CLAUDE_CONFIG_DIR"] = str(native)
            projects = {}
            for project, entry in (("alpha", "AGENTS.md"), ("beta", "CLAUDE.md")):
                workspace = root / project
                workspace.mkdir()
                marker = "PROJECT_RULE_" + project.upper()
                rules = f"# Synthetic project\nProject instruction marker: {marker}\nWhenever asked to create result.txt, write exactly {marker} followed by one newline.\n"
                (workspace / entry).write_text(rules)
                projects[project] = {"workspace": workspace, "marker": marker, "rules": rules, "entry": entry}

            def invoke(label, words, live=False):
                start = time.monotonic()
                proc = subprocess.run([str(binary), *words], env=env, cwd=root, text=True,
                                      capture_output=True, timeout=420)
                events = [json.loads(line) for line in proc.stdout.splitlines() if line.strip()] if live else []
                report["commands"].append({"label": label, "arguments": words,
                                           "events": events, "output": "" if live else proc.stdout,
                                           "exit_code": proc.returncode, "elapsed_seconds": round(time.monotonic() - start, 2),
                                           "stderr": proc.stderr})
                save()
                require(proc.returncode == 0, f"{label} failed; inspect retained report")
                return events if live else json.loads(proc.stdout)

            for project, values in projects.items():
                registered = invoke("register-" + project, ["project", "add", "--home", str(home),
                                    "--id", project, "--workspace", str(values["workspace"])])
                require(registered["scope"] == "project:" + project, "new project lacks stable scope")

            def turn(label, project, text, resume=None):
                words = ["chat", "--home", str(home), "--project", project, "--format", "json",
                         "--claude", config["claude"], "--model", config["model"],
                         "--jev-provider", "patsnap", "--jev-model", config["jev_model"], "--timeout", "6m",
                         "--allowed-tools", "Read,Write", "-m", text]
                if resume:
                    words += ["--resume", resume]
                events = invoke(label, words, live=True)
                started = next(e for e in events if e["type"] == "run.started")
                recalled = next(e for e in events if e["type"] == "memory.recalled")
                result = next(e for e in events if e["type"] == "agent.result")
                receipt = next(e for e in reversed(events) if e["type"] == "run.finished")
                row = {"label": label, "run_id": receipt["run_id"], "session_id": receipt["session_id"],
                       "project_id": project, "output": result["data"]["raw"].get("result", ""),
                       "recalled": recalled["data"]["memories"] or [], "receipt": receipt["data"]}
                report["runs"].append(row)
                require(started["data"]["workspace"] == str(projects[project]["workspace"]), "wrong native directory")
                require(started["data"]["scope"] == "project:" + project, "scope confused with cwd")
                native_init = next(e for e in events if e["type"] == "agent.started")
                require(native_init["data"]["raw"]["cwd"] == str(projects[project]["workspace"]), "wrong native process cwd")
                row["native_session_id"] = native_init["data"]["native_session_id"]
                require(receipt["data"]["execution_status"] == "completed", "native execution incomplete")
                require(all(m["scope"] == "project:" + project for m in row["recalled"]), "cross-project recall")
                save()
                print(label + ": completed", flush=True)
                return row, events

            sessions, native_sessions, codenames, memory_ids = {}, {}, {}, {}
            for project in projects:
                codenames[project] = "ORCHID-" + project.upper() + "-RUNTIME-20261010"
                row, _ = turn("statement-" + project, project, "This project's release codename is " + codenames[project] + ".")
                require(row["receipt"]["memory_status"] == "saved", "fixture declaration was not admitted")
                sessions[project] = row["session_id"]
                native_sessions[project] = row["native_session_id"]
            for project, values in projects.items():
                row, events = turn("write-" + project, project,
                                   "Create result.txt using this project's instruction convention. Report its content.", sessions[project])
                require(row["session_id"] == sessions[project], "native continuation changed Host session")
                require(row["native_session_id"] == native_sessions[project], "native conversation was not resumed")
                require((values["workspace"] / "result.txt").read_text() == values["marker"] + "\n", "project instructions or file destination incorrect")
                require((values["workspace"] / values["entry"]).read_text() == values["rules"], "project instruction file changed")
                if project == "alpha":
                    require(any(e.get("data", {}).get("native_type") == "project_instructions" for e in events), "legacy AGENTS compatibility was not observed")
            for project in projects:
                row, _ = turn("fresh-recall-" + project, project,
                              "What is this project's release codename? Use current Host memory, cite its memory ID, and also give the project instruction marker.")
                require(row["session_id"] != sessions[project], "fresh session reused native conversation")
                require(row["native_session_id"] != native_sessions[project], "fresh run reused native session")
                require(row["recalled"], "fresh session lacked Host memory")
                require(codenames[project] in row["output"], "fresh answer did not use scoped knowledge")
                require(projects[project]["marker"] in row["output"], "fresh session omitted project rules")
                require(any(m["id"] in row["output"] for m in row["recalled"]), "fresh answer omitted source memory ID")
                memory_ids[project] = [m["id"] for m in row["recalled"]]
            require(set(memory_ids["alpha"]).isdisjoint(memory_ids["beta"]), "project memory identities mixed")
            require(not list(native.glob("projects/**/memory/*")), "native auto memory wrote files")
            report["assertions"] = ["two projects use stable independent scopes and actual cwd",
                                     "legacy AGENTS and native CLAUDE project rules produce correct files",
                                     "A and B resume their own native sessions",
                                     "fresh sessions recall only their project memory and cite its ID",
                                     "project rules remain unchanged and native auto memory files are absent"]
            report["passed"] = True
    except Exception as error:
        report["failure"] = str(error)
    finally:
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save()
    print("runtime acceptance: " + ("passed" if report["passed"] else "failed"), flush=True)
    raise SystemExit(0 if report["passed"] else 1)


if __name__ == "__main__":
    main()
