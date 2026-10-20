"""Read-only, destination-aware Agent configuration and bounded run evidence."""

import json
from contextlib import closing
from pathlib import Path
import sqlite3
from settings_store import private_bytes


def execution_engine(store, agent, execution, assigned):
    reference = agent.get("execution_config") or execution.get("execution_config")
    if not reference:
        return {"engine": "Claude Code", "transport": "print / stream-json"} if assigned else {"engine": None, "transport": None}
    path = store.config / reference
    try:
        value = json.loads(private_bytes(path))
        return {"engine": value.get("engine"), "transport": value.get("transport")}
    except (OSError, ValueError):
        return {"engine": None, "transport": None}


def instruction_sources(store, agent):
    entries = []
    for kind in ("instructions_file", "instructions_dir", "user_file"):
        if not agent.get(kind):
            continue
        path = store.config / agent[kind]
        paths = [path / name for name in ("AGENTS.md", "SOUL.md", "IDENTITY.md") if (path / name).exists()] if kind == "instructions_dir" else [path]
        if not paths:
            entries.append({"kind": kind, "path": str(path), "state": "unavailable"})
        for source in paths:
            entry = {"kind": kind, "path": str(source), "state": "configured"}
            # Private user profiles are referenced, never copied into role previews.
            if kind != "user_file":
                try:
                    body = private_bytes(source).decode()
                    entry.update(text=body[:16000], truncated=len(body) > 16000)
                except (OSError, ValueError):
                    entry["state"] = "unavailable"
            entries.append(entry)
    return entries


def configured_destinations(store, rows):
    host = next((r.get("value", {}) for r in rows if Path(r["path"]).name == "host.json"), {})
    destinations = []
    for row in rows:
        if row["category"] != "channels" or row.get("error"):
            continue
        value = row["value"]
        name = Path(row["path"]).name.removeprefix("channels.").removesuffix(".json")
        profile = value.get("execution_profile") or host.get("default_profile")
        execution = host.get("profiles", {}).get(profile, {}).get("execution", {})
        settings = value.get("settings", {})
        bot = settings.get("bot", settings)
        destinations.append({"channel": name, "enabled": value.get("enabled", False),
                             "origin": row["path"],
                             "account": bot.get("client_id") or settings.get("app_id") or "",
                             "tenant": bot.get("corp_id") or settings.get("tenant_id") or "",
                             "execution": execution, "profile": profile,
                             "agents_path": str(store.config / execution["agents_config"]) if execution.get("agents_config") else None})
    for row in rows:
        if row["category"] != "handoff" or row.get("error"):
            continue
        args = row["value"].get("args", [])
        def option(name):
            return args[args.index(name) + 1] if name in args and args.index(name) + 1 < len(args) else None
        path = option("--agents-config")
        if path and Path(path).is_absolute():
            destinations.append({"channel": "cli", "enabled": True, "account": "", "tenant": "",
                                 "state_home": option("--home"),
                                 "agents_path": path, "profile": "CLI handoff",
                                 "execution": {"model": option("--model"), "workspace": option("--workspace"),
                                               "execution_config": option("--execution-config")}})
    return destinations


def observations(catalog, destination, agent_id):
    """Use only explicitly attached state sources; never infer a production path."""
    result = {"activity": [], "groups": [], "skills": [], "evidence_state": "unknown"}
    sources = getattr(catalog, "catalogs", {})
    source = sources.get(destination["channel"])
    if source is None or not source.database.exists():
        return result
    if destination["channel"] == "cli" and (not destination.get("state_home") or
            source.database.parent.resolve() != Path(destination["state_home"]).resolve()):
        return result
    try:
        with closing(source.connection()) as conn:
            tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            if "runs" not in tables:
                return result
            messages = []
            if destination["channel"] != "cli" and "channel_messages" in tables:
                # Account identity is required before associating channel history.
                if not destination["account"]:
                    return result
                messages = [json.loads(r[0]) for r in conn.execute(
                    "SELECT payload FROM channel_messages WHERE channel=? AND account_id=? ORDER BY received_at DESC LIMIT 2000",
                    (destination["channel"], destination["account"]))]
                if destination["tenant"]:
                    messages = [m for m in messages if m.get("tenant_id") == destination["tenant"]]
            run_ids = {m.get("run_id") for m in messages if (m.get("agent_id") or "default") == agent_id}
            query = "SELECT payload FROM runs WHERE COALESCE(NULLIF(json_extract(payload,'$.agent_id'),''),'default')=? ORDER BY created_at DESC LIMIT 200"
            runs = [json.loads(r[0]) for r in conn.execute(query, (agent_id,))]
            if destination["channel"] != "cli":
                runs = [r for r in runs if r["id"] in run_ids]
            replies = {m.get("run_id"): m.get("reply_status", "unknown") for m in reversed(messages)}
            seen = set()
            for m in messages:
                key = (m.get("tenant_id"), m.get("conversation_id"))
                if m.get("conversation_type") != "2" or not key[1] or key in seen:
                    continue
                seen.add(key)
                result["groups"].append({"conversation_id": key[1], "tenant": key[0],
                                         "last_seen": m.get("received_at"),
                                         "last_agent": (m.get("agent_id") or "default") if m.get("run_id") else None})
            for run in runs[:20]:
                key = destination["channel"] + ":db:" + run["id"]
                result["activity"].append({"id": run["id"], "key": key, "created_at": run.get("created_at"),
                                           "execution": run.get("execution_status", "unknown"),
                                           "acceptance": run.get("acceptance_status", "unverified"),
                                           "delivery": replies.get(run["id"], "unknown"),
                                           "model": run.get("model") or run.get("requested_model"),
                                           "workspace": run.get("workspace") or run.get("scope")})
                if "events" not in tables:
                    continue
                for raw, in conn.execute("SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type')='workspace.assembled' ORDER BY seq", (run["id"],)):
                    manifest = json.loads(raw).get("data", {}).get("skill_manifest") or {}
                    if manifest.get("agent_id") != agent_id:
                        continue
                    for skill in manifest.get("sources") or []:
                        entry = {"name": skill.get("name"), "revision": skill.get("revision"), "run_key": key,
                                 "observed_at": run.get("created_at"), "state": "observed_loaded"}
                        if not any(s["name"] == entry["name"] for s in result["skills"]):
                            result["skills"].append(entry)
            result["evidence_state"] = "bounded_history"
    except (OSError, ValueError, sqlite3.Error):
        result.update(evidence_state="read_failed", error="Cannot read this destination's local evidence.")
    return result


def profiles(store, catalog=None):
    if store is None:
        return {"enabled": False, "profiles": []}
    rows = store.index()["files"]
    destinations = configured_destinations(store, rows)
    virtual_files = []
    for destination in destinations:
        if destination["channel"] == "cli" or destination["agents_path"]:
            continue
        # Runtime has an implicit default when no Agent catalog is selected.
        value = {"default_agent": "default", "agents": [{"id": "default", "name": "Default agent"}]}
        row = {"id": "implicit-" + destination["channel"], "category": "agents", "read_only": True,
               "path": destination["origin"], "title": destination["channel"] + " · Default agent",
               "revision": None, "value": value}
        destination["agents_path"] = destination["origin"]
        virtual_files.append(row)
    rows = rows + virtual_files
    connectors = [{"name": r["title"], "origin": r["path"], "state": "unknown" if r.get("error") else
                   "configured" if r.get("value", {}).get("enabled") else "disabled",
                   "scope": "shared_beta"} for r in rows if r["category"] == "sources"]
    result = []
    for row in rows:
        if row["category"] != "agents" or row.get("error"):
            continue
        config = row["value"]
        selected_destinations = [d for d in destinations if d["agents_path"] == row["path"]]
        for destination in selected_destinations or [{"channel": "unassigned", "execution": {}, "enabled": False}]:
            for agent in config.get("agents", []):
                evidence = observations(catalog, destination, agent["id"])
                bindings = {b["conversation_id"]: b["agent_id"] for b in config.get("group_bindings", [])}
                if destination["channel"] == "cli":
                    bindings = {}
                groups = [{"conversation_id": cid, "routing": "explicit", "last_seen": None}
                          for cid, aid in bindings.items() if aid == agent["id"]]
                default = config.get("default_agent") == agent["id"]
                for observed in evidence.pop("groups"):
                    cid = observed["conversation_id"]
                    current = bindings.get(cid, config.get("default_agent"))
                    if current != agent["id"]:
                        continue
                    matching = next((g for g in groups if g["conversation_id"] == cid and not g.get("tenant")), None)
                    entry = {**observed, "routing": "explicit" if cid in bindings else "default"}
                    if matching is not None:
                        matching.update(entry)
                    else:
                        groups.append(entry)
                execution = destination["execution"]
                result.append({"key": row["id"] + ":" + destination["channel"] + ":" + agent["id"],
                               "file_id": row["id"], "id": agent["id"], "name": agent.get("name") or agent["id"],
                               "destination": destination["channel"], "enabled": destination["enabled"],
                               "account": destination.get("account"), "default": default, "origin": row["path"],
                               "revision": row["revision"], "loaded_state": "unknown",
                               "model": agent.get("model") or execution.get("model"),
                               "model_origin": "agent" if agent.get("model") else "service",
                               "execution_config": agent.get("execution_config") or execution.get("execution_config"),
                               "workspace": agent.get("workspace") or execution.get("workspace"),
                               "workspace_origin": "agent" if agent.get("workspace") else "service",
                               "projects": agent.get("projects"),
                               "instructions": instruction_sources(store, agent),
                               "groups": groups, "connectors": connectors,
                               **execution_engine(store, agent, execution, destination["channel"] != "unassigned"), **evidence})
    return {"enabled": True, "profiles": result, "virtual_files": virtual_files}
