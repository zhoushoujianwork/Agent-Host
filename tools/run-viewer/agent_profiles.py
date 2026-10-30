"""Read-only, destination-aware Agent configuration and bounded run evidence."""

import json
from contextlib import closing
from pathlib import Path
import sqlite3
from settings_store import private_bytes, redact
from chat_runtime import agent_destination
from platform_skills import preview
import hashlib


def execution_engine(store, agent, execution, assigned, origin=None):
    if execution.get("offload_config"):
        return {"engine":"Offload","transport":"exec"}
    reference = agent.get("execution_config") or execution.get("execution_config")
    if not reference:
        return {"engine": "Claude Code", "transport": "print / stream-json"} if assigned else {"engine": None, "transport": None}
    base = Path(origin).parent if agent.get("execution_config") and origin else store.config
    path = base / reference
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
    result = {"activity": [], "groups": [], "skills": [], "native_inventory": None, "platform_catalog": None, "evidence_state": "unknown"}
    sources = getattr(catalog, "catalogs", {})
    source_name = destination["channel"]
    if source_name == "web":
        source_name = next((name for name, source in sources.items() if destination.get("state_home") and source.database.parent.resolve() == Path(destination["state_home"]).resolve()), None)
    source = sources.get(source_name)
    if source is None and destination["channel"] == "web" and hasattr(catalog, "database") and destination.get("state_home") and catalog.database.parent.resolve() == Path(destination["state_home"]).resolve():
        source, source_name = catalog, ""
    if source is None or not source.database.exists():
        return result
    if destination["channel"] in {"cli", "web"} and (not destination.get("state_home") or
            source.database.parent.resolve() != Path(destination["state_home"]).resolve()):
        return result
    try:
        with closing(source.connection()) as conn:
            tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            if "runs" not in tables:
                return result
            messages = []
            if destination["channel"] not in {"cli", "web"} and "channel_messages" in tables:
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
            if destination["channel"] not in {"cli", "web"}:
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
                key = (source_name + ":" if source_name else "") + "db:" + run["id"]
                result["activity"].append({"id": run["id"], "key": key, "created_at": run.get("created_at"),
                                           "execution": run.get("execution_status", "unknown"),
                                           "acceptance": run.get("acceptance_status", "unverified"),
                                           "delivery": replies.get(run["id"], "unknown"),
                                           "model": run.get("model") or run.get("requested_model"),
                                           "workspace": run.get("workspace") or run.get("scope")})
                if "events" not in tables:
                    continue
                if result["platform_catalog"] is None:
                    for payload, in conn.execute("SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type')='skills.catalog.provided' ORDER BY seq DESC", (run["id"],)):
                        event = json.loads(payload)
                        data = event.get("data") or {}
                        manifest = data.get("manifest") or {}
                        empty = not manifest.get("version") and not manifest.get("sources")
                        if not empty and (manifest.get("agent_id") != agent_id or manifest.get("audience_id") != (run.get("audience_id") or "owner")):
                            continue
                        fields = ("skill_id", "name", "kind", "title", "applicability", "revision", "content_digest", "tool_contract_digest")
                        snapshot = {"digest": manifest.get("digest"), "run_key": key,
                                    "observed_at": event.get("at") or run.get("created_at"),
                                    "audience_id": manifest.get("audience_id") or run.get("audience_id") or "owner", "scopes": manifest.get("scopes") or [],
                                    "sources": [{k: s[k] for k in fields if k in s} for s in manifest.get("sources") or []],
                                    "unready": data.get("unready") or {}, "loads": [], "coverage": "latest_snapshot_in_20_runs"}
                        activity = list(conn.execute("SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type') IN ('skills.load.observed','skills.reference.observed') ORDER BY seq LIMIT 201", (run["id"],)))
                        snapshot["loads_truncated"] = len(activity) > 200
                        for raw, in activity[:200]:
                            observed = json.loads(raw)
                            detail = observed.get("data") or {}
                            if detail.get("snapshot_digest") != snapshot["digest"]:
                                continue
                            snapshot["loads"].append({"type": observed.get("type"), "status": observed.get("status"),
                                "at": observed.get("at"), "skill_id": (detail.get("source") or {}).get("skill_id"),
                                "path": (detail.get("file") or {}).get("path")})
                        result["platform_catalog"] = preview(snapshot)
                        break
                if result["native_inventory"] is None:
                    # Fetch initialization evidence only; tool output can dwarf
                    # the inventory and is available in the execution detail.
                    events = conn.execute(
                        "SELECT payload FROM events WHERE run_id=? "
                        "AND json_extract(payload,'$.phase')='execute' "
                        "AND json_extract(payload,'$.component') IN ('harness.acp','harness.claude') "
                        "AND (json_extract(payload,'$.data.raw.native_initialization') "
                        "OR (json_extract(payload,'$.data.raw.type')='system' "
                        "AND json_extract(payload,'$.data.raw.subtype')='init')) ORDER BY seq DESC",
                        (run["id"],))
                    for (payload,) in events:
                        event = json.loads(payload)
                        raw = (event.get("data") or {}).get("raw") or {}
                        if event.get("phase") != "execute" or event.get("component") not in {"harness.acp", "harness.claude"} or not isinstance(raw, dict):
                            continue
                        if not (raw.get("native_initialization") or raw.get("type") == "system" and raw.get("subtype") == "init"):
                            continue
                        if raw.get("admitted") is False:
                            continue
                        result["native_inventory"] = {"skills": [name[:200] for name in raw.get("skills") or [] if isinstance(name, str)],
                            "services": [{"name": str(s.get("name", ""))[:200], "status": str(s.get("status", "unknown"))[:40]} for s in raw.get("mcp_servers") or [] if isinstance(s, dict)],
                            "skills_observed": isinstance(raw.get("skills"), list), "model": event.get("model"),
                            "transport": "acp" if event["component"] == "harness.acp" else "print / stream-json",
                            "observed_at": event.get("at") or run.get("created_at"), "run_key": key}
                        break
                for raw, in conn.execute("SELECT payload FROM events WHERE run_id=? AND json_extract(payload,'$.type')='workspace.assembled' ORDER BY seq", (run["id"],)):
                    manifest = json.loads(raw).get("data", {}).get("skill_manifest") or {}
                    if manifest.get("agent_id") != agent_id:
                        continue
                    for skill in manifest.get("sources") or []:
                        entry = {"name": skill.get("name"), "revision": skill.get("revision"), "run_key": key,
                                 "observed_at": run.get("created_at"), "state": "observed_assembled"}
                        if not any(s["name"] == entry["name"] for s in result["skills"]):
                            result["skills"].append(entry)
            result["evidence_state"] = "bounded_history"
    except (OSError, ValueError, sqlite3.Error):
        result.update(evidence_state="read_failed", error="Cannot read this destination's local evidence.")
    return result


def profiles(store, catalog=None, web_config=None, web_agent=None):
    if store is None:
        return {"enabled": False, "profiles": []}
    rows = store.index()["files"]
    destinations = configured_destinations(store, rows)
    virtual_files = []
    web_options = None
    web_error = None
    if web_config:
        try:
            web = agent_destination(web_config)
            web_options = store.agent_options() if hasattr(store, "agent_options") else None
            destinations.insert(0, {"channel": "web", "enabled": True, "account": "", "tenant": "", **web})
            if web["agents_path"] is None:
                path=str(web_config)
                destinations[0]["agents_path"]=path
                virtual_files.append({"id":"web-offload","category":"agents","read_only":True,"path":path,
                    "title":"Web Chat · Offload","revision":None,"value":{"default_agent":web["selected_agent"],"agents":web["agents"]}})
            elif not any(row["category"] == "agents" and row["path"] == web["agents_path"] for row in rows):
                path = Path(web["agents_path"])
                virtual_files.append({"id": "web-agents-" + hashlib.sha256(str(path).encode()).hexdigest()[:12],
                    "category": "agents", "read_only": True, "path": str(path), "title": "Web Chat 智能体",
                    "revision": None, "value": redact(json.loads(private_bytes(path)), token=lambda _: "[redacted]")})
        except (OSError, ValueError, KeyError, TypeError, AttributeError):
            web_error = "无法读取当前 Web Chat 连接配置。"
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
                if destination["channel"] in {"cli", "web"}:
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
                               "chat_selected": destination["channel"] == "web" and agent["id"] == (web_agent or destination.get("selected_agent")),
                               "account": destination.get("account"), "default": default, "origin": row["path"],
                               "revision": row["revision"], "loaded_state": "unknown",
                               "model": agent.get("model") or execution.get("model"),
                               "model_origin": "agent" if agent.get("model") else "service",
                               "execution_config": agent.get("execution_config") or execution.get("execution_config"),
                               "workspace": agent.get("workspace") or execution.get("workspace"),
                               "workspace_origin": "agent" if agent.get("workspace") else "service",
                               "projects": agent.get("projects"),
                               "instructions": instruction_sources(store, agent),
                               "configured_skills": agent.get("native_skills"),
                               "platform_policy": "offload_native" if execution.get("offload_config") else "automatic_default" if agent["id"] == "default" and destination["channel"] != "unassigned" else "existing_selection",
                               "configured_knowledge": agent.get("knowledge_sources"),
                               "groups": groups, "connectors": [] if execution.get("offload_config") else ([{"name": source["name"], "origin": "当前 Web 连接", "state": "configured", "scope": "agent_selection"}
                                   for source in web_options["knowledge_sources"] if agent.get("knowledge_sources") is None or source["id"] in agent["knowledge_sources"]]
                                   if destination["channel"] == "web" and web_options else connectors),
                               **execution_engine(store, agent, execution, destination["channel"] != "unassigned", row["path"]), **evidence})
    result.sort(key=lambda profile: profile["destination"] != "web")
    return {"enabled": True, "profiles": result, "virtual_files": virtual_files, "web_error": web_error}
