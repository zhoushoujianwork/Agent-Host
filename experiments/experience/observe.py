#!/usr/bin/env python3
"""Summarize learning and actual native Skill use without exporting source text."""

import argparse
from contextlib import closing
from datetime import datetime, timezone
import json
from pathlib import Path
import sqlite3


def observe(home, since="", limit=30):
    if not 1 <= limit <= 100:
        raise ValueError("limit must be 1-100")
    if since:
        stamp = datetime.fromisoformat(since.replace("Z", "+00:00"))
        if stamp.tzinfo is None:
            raise ValueError("since must include a timezone")
        stamp = stamp.astimezone(timezone.utc)
        since = stamp.strftime("%Y-%m-%dT%H:%M:%S.") + f"{stamp.microsecond:06d}000Z"
    database = Path(home).resolve() / "state.db"
    with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as db:
        rows = db.execute(
            "SELECT id,payload FROM runs WHERE created_at>=? ORDER BY created_at DESC LIMIT ?",
            (since, limit),
        ).fetchall()
        runs = []
        for run_id, payload in rows:
            run = json.loads(payload)
            item = {
                "run_id": run_id,
                "created_at": run.get("created_at"),
                "execution_status": run.get("execution_status"),
                "experience_status": run.get("experience_status"),
                "learning": None,
                "hint_matches": None,
                "recalled_procedures": 0,
                "published_active": 0,
                "published_conflicted": 0,
                "skill_uses": [],
                "observed_tool_failures": 0,
                "tccli_unknown_profile_flag": 0,
            }
            sources, calls = {}, {}
            events = db.execute(
                """SELECT payload FROM events WHERE run_id=? AND
                (json_extract(payload,'$.type') IN
                ('experience.learning.summary','memory.procedure_hints.applied',
                 'memory.recalled','candidate.accepted','workspace.assembled')
                OR (json_extract(payload,'$.phase')='execute'
                    AND json_extract(payload,'$.data.tool.stage') IN ('call','result')))
                ORDER BY seq""",
                (run_id,),
            )
            for (raw,) in events:
                event = json.loads(raw)
                data = event.get("data") or {}
                kind = event.get("type")
                if kind == "experience.learning.summary":
                    item["learning"] = {
                        key: data.get(key)
                        for key in ("drafts", "eligible", "refinement_attempted", "refined_eligible")
                    }
                elif kind == "memory.procedure_hints.applied":
                    item["hint_matches"] = data.get("matches", 0)
                elif kind == "memory.recalled":
                    item["recalled_procedures"] = sum(
                        memory.get("kind") == "procedure" for memory in (data.get("memories") or [])
                    )
                elif kind == "candidate.accepted":
                    status = (data.get("memory") or {}).get("status")
                    if status in ("active", "conflicted"):
                        item["published_" + status] += 1
                elif kind == "workspace.assembled":
                    sources = {
                        source["name"]: source
                        for source in ((data.get("skill_manifest") or {}).get("sources") or [])
                    }
                tool = data.get("tool")
                if not tool or event.get("phase") != "execute":
                    continue
                if tool.get("stage") == "call":
                    calls[tool["id"]] = tool
                    continue
                call = calls.pop(tool.get("id"), None)
                if call is None:
                    continue
                item["observed_tool_failures"] += bool(tool.get("is_error"))
                arguments = call.get("input") or {}
                if not isinstance(arguments, dict):
                    continue
                command = arguments.get("command", "")
                result = json.dumps(tool.get("result"), ensure_ascii=False)
                if "tccli" in command and "--cli-profile" in command and "Unknown options" in result:
                    item["tccli_unknown_profile_flag"] += 1
                if call.get("name") != "Skill" or tool.get("is_error") or tool.get("result") is None:
                    continue
                name = arguments.get("skill", "").split(":")[-1]
                source = sources.get(name)
                if source:
                    item["skill_uses"].append({
                        "memory_id": source["memory_id"],
                        "revision": source["revision"],
                        "tool_id": tool["id"],
                    })
            runs.append(item)
    return {
        "scope": "bounded local operational sample; publication counts are historical event outcomes",
        "limits": "Skill use proves a paired invocation, not causal benefit or business acceptance; wrapper errors may undercount API failures",
        "runs": runs,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", required=True, help="Channel or CLI state directory")
    parser.add_argument("--since", default="", help="Inclusive UTC timestamp, e.g. 2026-10-15T04:00:00Z")
    parser.add_argument("--limit", type=int, default=30)
    args = parser.parse_args()
    print(json.dumps(observe(args.home, args.since, args.limit), indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
