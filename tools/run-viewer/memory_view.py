"""Read-only memory projections from stored rows and explicit report observations."""

import json

MEMORY_LIMIT = 200


def normalize_report(report):
    """Adapt native chat inspection reports without changing their saved evidence."""
    if "runs" in report:
        return report
    commands = report.get("commands")
    if not isinstance(commands, list) or not commands:
        raise ValueError("report requires runs or inspected native commands")
    rows, normalized = [], []
    for command in commands:
        if not isinstance(command, dict) or not isinstance(command.get("run"), dict):
            raise ValueError("native command report requires an inspected run")
        run = command["run"]
        if not run.get("id"):
            raise ValueError("inspected run requires an ID")
        receipt = {k: run[k] for k in ("execution_status", "memory_status", "acceptance_status", "experience_status") if k in run}
        rows.append({**run, "run_id": run["id"], "label": command.get("label", ""),
                     "receipt": receipt, "recalled": run.get("context") or []})
        normalized.append({**command, "arguments": command.get("command", []),
                           "events": command.get("events", run.get("events", []))})
    return {**report, "runs": rows, "commands": normalized}


def object_payload(raw):
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError("memory projection requires object payloads")
    return value


def database_rows(catalog):
    if not catalog.database.exists():
        if catalog.file_memory_marker() is not None:
            raise ValueError("file memory requires its preserved operational state.db")
        return [], [], 0, 0, catalog.filter_context()
    conn = catalog.memory_connection()
    try:
        version = catalog.schema_version(conn)
        context = catalog.filter_context(conn)
        where, args = catalog.memory_filter(context, version)
        memories = catalog.memory_table(conn)
        annotations = catalog.memory_table(conn, "memory_annotations")
        total = conn.execute("SELECT count(*) FROM " + memories + " m" + where, args).fetchone()[0]
        annotation = "a.payload" if version >= 3 else "NULL"
        join = " LEFT JOIN " + annotations + " a ON a.memory_id=m.id" if version >= 3 else ""
        rows = []
        for raw, derived in conn.execute("SELECT m.payload," + annotation + " FROM " + memories + " m" + join + where + " ORDER BY m.rowid DESC LIMIT ?", args + (MEMORY_LIMIT,)):
            memory = object_payload(raw)
            if version >= 3 and derived is None:
                raise ValueError(f"missing stored annotation for memory {memory.get('id')}")
            rows.append((memory, object_payload(derived) if derived else None))
        run_where, run_args = catalog.run_filter(context)
        run_total = conn.execute("SELECT count(*) FROM runs" + run_where, run_args).fetchone()[0]
        records = []
        for (raw,) in conn.execute("SELECT payload FROM runs" + run_where + " ORDER BY created_at DESC,rowid DESC LIMIT 200", run_args):
            run = object_payload(raw)
            events = [object_payload(e[0]) for e in conn.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (run["id"],))]
            records.append({"key": "db:" + run["id"], "run": run, "events": events})
        return rows, records, total, run_total, context
    finally:
        conn.close()


def run_brief(record):
    run = record["run"]
    return {"key": record["key"], "workspace": run.get("workspace") or run.get("scope", ""),
            **{k: run.get(k) for k in
               ("id", "input", "created_at", "model", "requested_model", "execution_status", "label",
                "project_id", "project_name", "projects", "additional_directories", "scope", "session_id", "native_session_id")},
            "agent_id": run.get("agent_id") or "default", "audience_id": run.get("audience_id") or "owner"}


def retrieval_decisions(record, scope=None):
    return [decision for event in record["events"] if event["type"] == "memory.retrieval.evaluated"
            and (scope is None or event.get("data", {}).get("scope") in (None, scope))
            for decision in event.get("data", {}).get("decisions", [])]


def decision_summary(record):
    event = next((e for e in reversed(record["events"]) if e["type"] == "decision.completed"), None)
    if not event:
        return None
    decision = event.get("data", {}).get("decision", {})
    audit = decision.get("details", {})
    if not isinstance(audit, dict):
        raise ValueError("decision audit must be an object")
    result = {"kind": decision.get("kind"), "store": decision.get("store"),
              "model": decision.get("model"), "reason": audit.get("reason"),
              "threshold": audit.get("eligibility_probability_threshold"), "relations": []}
    for evaluation in audit.get("evaluations", []):
        answers = evaluation.get("response", {}).get("answers", {})
        if evaluation.get("stage") == "classify":
            result.update({k: answers.get(k) for k in ("eligibility", "memory_kind")})
        elif evaluation.get("stage") == "compare":
            existing = evaluation.get("state", {}).get("existing", {})
            for key, answer in answers.items():
                result["relations"].append({"id": existing.get(key, {}).get("id"), "statement": existing.get(key, {}).get("statement"), **answer})
    return result


def memory_catalog(catalog):
    database, db_records, db_total, run_total, filter_info = database_rows(catalog)
    external = {}
    imported = [m for m, _ in database if m.get("external_receipt_id")]
    if imported:
        conn = catalog.connection()
        try:
            for memory in imported:
                row = conn.execute("SELECT payload FROM external_memory_receipts WHERE id=?",
                                   (memory["external_receipt_id"],)).fetchone()
                if row is None:
                    raise ValueError("external memory receipt is missing")
                receipt = object_payload(row[0])
                binding = receipt["connection"]
                if any(binding.get(k) != memory.get(k, "default" if k == "agent_id" else "owner")
                       for k in ("scope", "agent_id", "audience_id")):
                    raise ValueError("external receipt does not match memory attribution")
                ids = set(memory.get("evidence_ids") or [])
                evidence = [e for e in receipt["submission"]["evidence"] if e["id"] in ids]
                if len(evidence) != len(ids):
                    raise ValueError("external memory evidence is missing")
                external[memory["id"]] = {
                    "role": "external_agent_supplied_evidence", "receipt_id": receipt["id"],
                    "source": receipt["submission"]["source"], "received_at": receipt["received_at"],
                    "evidence": evidence, "digest": receipt["digest"],
                    "coverage": "Selected excerpts supplied by an external agent; not Host-observed execution."}
        finally:
            conn.close()
    db_source = catalog.memory_source()
    groups = {}
    if database or db_records:
        groups["db"] = {"source": db_source, "records": db_records, "rows": database}
    for record in catalog.archives().values():
        group = groups.setdefault(record["source"]["id"], {"source": record["source"], "records": [], "rows": []})
        group["records"].append(record)

    items, recalls, sources = [], [], []
    for group in groups.values():
        source, records = group["source"], group["records"]
        records.sort(key=lambda r: r["run"].get("created_at", ""))
        by_run = {r["run"]["id"]: r for r in records}
        observed, priorities, derived = {}, {}, {}

        def put(memory, priority):
            if not isinstance(memory, dict) or not memory.get("id") or not isinstance(memory.get("text"), str):
                raise ValueError("memory observation requires an ID and source text")
            key = memory["id"]
            if priority >= priorities.get(key, -1):
                observed[key] = {**observed.get(key, {}), **memory}
                priorities[key] = priority
            else:
                # A lower-priority injection can fill absent provenance fields;
                # it cannot overwrite a later inspected lifecycle observation.
                observed[key] = {**memory, **observed[key]}

        for memory, annotation in group["rows"]:
            put(memory, 3)
            derived[memory["id"]] = annotation
        if source["kind"] == "report":
            for record in records:
                for memory in record["run"].get("context") or []:
                    put(memory, 0)
                for event in record["events"]:
                    if event["type"] != "decision.completed":
                        continue
                    audit = event.get("data", {}).get("decision", {}).get("details", {})
                    for evaluation in audit.get("evaluations", []):
                        if evaluation.get("stage") != "compare":
                            continue
                        for value in evaluation.get("state", {}).get("existing", {}).values():
                            put({**value, "text": value["statement"], "scope": record["run"].get("scope", ""),
                                 "agent_id": record["run"].get("agent_id") or "default",
                                 "audience_id": record["run"].get("audience_id") or "owner"}, 1)
                for memory in record.get("memories", []):
                    put(memory, 2)

        for memory in observed.values():
            origin = by_run.get(memory.get("source_run_id"))
            annotation = derived.get(memory["id"])
            annotation_origin = "stored" if annotation is not None else "unavailable"
            annotation_record = None
            if annotation is None and source["kind"] == "report":
                # Only an exact recorded input can supply historical annotations.
                matches = [r for r in records if r["run"].get("input") == memory["text"]]
                if len(matches) == 1:
                    event = next((e for e in matches[0]["events"] if e["type"] == "memory.query.analyzed"), None)
                    if event:
                        annotation = event.get("data", {}).get("query", {}).get("annotation")
                        if not isinstance(annotation, dict):
                            raise ValueError("recorded query annotation must be an object")
                        annotation_origin, annotation_record = "recorded_input", matches[0]["key"]
            uses = []
            for record in records:
                injected = next((m for m in record["run"].get("context") or [] if m["id"] == memory["id"]), None)
                decision = next((d for d in retrieval_decisions(record, memory.get("scope")) if d["memory_id"] == memory["id"]), None)
                if injected or decision:
                    uses.append({**run_brief(record), "status": "injected" if injected else "candidate" if decision["accepted"] else "excluded",
                                 "revision": injected.get("revision") if injected else None,
                                 "reason": decision.get("reason") if decision else None,
                                 "routes": decision.get("routes", []) if decision else []})
            items.append({"key": source["id"] + ":" + memory["id"], "memory": memory,
                          "source": source, "snapshot": source["kind"] == "report",
                          "annotation": annotation, "annotation_origin": annotation_origin,
                          "annotation_record_key": annotation_record,
                          "origin_run": run_brief(origin) if origin else None,
                          "external_evidence": external.get(memory["id"]) if source["kind"] == "database" else None,
                          "decision": decision_summary(origin) if origin and memory.get("source_type") == "user_statement" else None,
                          "uses": uses})
        for record in records:
            context = record["run"].get("context") or []
            if not context:
                continue
            recalls.append({**run_brief(record), "source": source,
                            "context": context, "decisions": retrieval_decisions(record)})
        sources.append({**source, "memory_count": len(observed),
                        "latest_at": records[-1]["run"].get("created_at", "") if records else ""})
    items.sort(key=lambda item: item["memory"].get("created_at", ""), reverse=True)
    sources.sort(key=lambda source: source["latest_at"], reverse=True)
    recalls.sort(key=lambda run: run.get("created_at") or "", reverse=True)
    return {"memories": items[:MEMORY_LIMIT], "sources": sources, "recalls": recalls[:200],
            **filter_info, "database_present": catalog.database.exists(),
            "database_total": db_total, "database_runs_total": run_total, "memory_limit": MEMORY_LIMIT,
            "observed_total": db_total + sum(s["memory_count"] for s in sources if s["kind"] == "report"),
            "truncated": db_total > MEMORY_LIMIT or len(items) > MEMORY_LIMIT}
