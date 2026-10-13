#!/usr/bin/env python3
"""Serve an owner-local, read-only HTML view of host records and saved reports."""

import argparse
import hashlib
import json
from pathlib import Path
import sqlite3
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

from memory_view import memory_catalog, normalize_report

ROOT = Path(__file__).resolve().parents[2]
ASSETS = Path(__file__).resolve().parent
MAX_REPORT_BYTES = 32 << 20
RUN_LIMIT = 200


def decode(payload):
    value = json.loads(payload)
    if not isinstance(value, dict):
        raise ValueError("record payload must be an object")
    return value


def partition(record):
    return record.get("agent_id") or "default", record.get("audience_id") or "owner"


def partition_sql(payload="payload"):
    return (f"COALESCE(NULLIF(json_extract({payload}, '$.agent_id'), ''), 'default')=? AND "
            f"COALESCE(NULLIF(json_extract({payload}, '$.audience_id'), ''), 'owner')=?")


def summary(key, run, source, event_count=None, review_events=0):
    return {
        "key": key, "id": run["id"], "session_id": run.get("session_id"),
        "native_session_id": run.get("native_session_id"),
        "agent_id": partition(run)[0], "audience_id": partition(run)[1],
        "project_id": run.get("project_id", ""), "project_name": run.get("project_name", ""),
        "projects": run.get("projects", []), "additional_directories": run.get("additional_directories", []),
        "workspace": run.get("workspace") or run.get("scope", ""),
        "runtime_fingerprint": run.get("runtime_fingerprint", ""),
        "input": run.get("input", ""), "scope": run.get("scope", ""),
        "created_at": run.get("created_at", ""), "label": run.get("label", ""),
        "execution_status": run.get("execution_status", "unknown"),
        "acceptance_status": run.get("acceptance_status", "unverified"),
        "memory_status": run.get("memory_status", ""),
        "experience_status": run.get("experience_status", ""),
        "model": run.get("model") or run.get("requested_model", ""),
        "event_count": event_count, "review_events": review_events, "source": source,
    }


def report_records(report, source):
    """Retain snapshots and later review events without inventing current DB state."""
    report = normalize_report(report)
    if not isinstance(report.get("runs"), list):
        raise ValueError("report must contain a runs array")
    if not isinstance(report.get("commands", []), list):
        raise ValueError("report commands must be an array")
    events_by_run = {}
    candidate_snapshots = {}
    memory_snapshots = {}
    for command in report.get("commands", []):
        if not isinstance(command, dict) or not isinstance(command.get("arguments", []), list):
            raise ValueError("command must be an object with an arguments array")
        events = command.get("events", [])
        if not isinstance(events, list):
            raise ValueError("command events must be an array")
        for event in events:
            if not isinstance(event, dict) or not isinstance(event.get("data", {}), dict):
                raise ValueError("event must be an object with object data")
            run_id = event.get("run_id")
            if not run_id or type(event.get("seq")) is not int or "type" not in event:
                raise ValueError("event requires run_id and integer seq")
            known = events_by_run.setdefault(run_id, {})
            if event["seq"] in known and known[event["seq"]] != event:
                raise ValueError("conflicting events share the same run and sequence")
            known[event["seq"]] = event
            if event.get("type") == "candidate.accepted":
                memory = event.get("data", {}).get("memory")
                if memory:
                    memory_snapshots[memory["id"]] = memory
        output = command.get("output")
        # Inspection/local review outputs are explicit JSON snapshots, not prose.
        if not events and isinstance(output, str) and command.get("arguments", [])[:1] == ["memory"]:
            parsed = json.loads(output)
            items = parsed if isinstance(parsed, list) else [parsed]
            for item in items:
                if not isinstance(item, dict):
                    raise ValueError("memory inspection must return objects")
                candidate = item.get("candidate", item)
                if "draft" in candidate and "id" in candidate:
                    candidate_snapshots[candidate["id"]] = candidate
                elif "source_type" in item and "id" in item:
                    memory_snapshots[item["id"]] = item
    result = {}
    for row in report["runs"]:
        if not isinstance(row, dict):
            raise ValueError("run must be an object")
        run_id = row.get("run_id", row.get("id"))
        if not run_id or run_id in result:
            raise ValueError("report requires unique run IDs")
        events = sorted(events_by_run.get(run_id, {}).values(), key=lambda e: e["seq"])
        start = next((e for e in events if e["type"] == "run.started"), {})
        receipt_event = next((e for e in reversed(events) if e["type"] == "run.finished"), {})
        receipt = row.get("receipt", receipt_event.get("data", {}))
        if not isinstance(receipt, dict):
            raise ValueError("run receipt must be an object")
        recall = next((e for e in events if e["type"] == "memory.recalled"), {})
        command = next((c for c in report.get("commands", []) if c.get("label") == row.get("label")), {})
        arguments = command.get("arguments", [])
        input_text = row.get("input", "")
        for flag in ("-m", "--message"):
            if flag in arguments:
                if arguments.index(flag) + 1 == len(arguments):
                    raise ValueError("message option requires a value")
                input_text = arguments[arguments.index(flag) + 1]
                break
        started = start.get("data", {})
        scope = row.get("scope") or started.get("scope") or row.get("workspace") or started.get("workspace", "")
        workspace = row.get("workspace") or started.get("workspace") or scope
        native_session_id = row.get("native_session_id")
        if not native_session_id:
            for event_type in ("agent.started", "agent.dispatched"):
                native_session_id = next((e["data"]["native_session_id"] for e in events
                                          if e["type"] == event_type and e.get("data", {}).get("native_session_id")), None)
                if native_session_id:
                    break
        run = {
            "id": run_id, "label": row.get("label", ""), "session_id": row.get("session_id"),
            "agent_id": row.get("agent_id") or started.get("agent_id") or "default",
            "audience_id": row.get("audience_id") or started.get("audience_id") or "owner",
            "native_session_id": native_session_id, "input": input_text,
            "output": row.get("output", ""), "scope": scope, "workspace": workspace,
            **{key: row.get(key, started.get(key, "")) for key in
               ("project_id", "project_name", "runtime_fingerprint")},
            **{key: row.get(key, started.get(key, [])) for key in
               ("projects", "additional_directories", "supplements")},
            "created_at": start.get("at", report.get("started_at", report.get("date", ""))),
            "agent": "local-claude", "requested_model": report.get("requested_claude_model", ""),
            "model": receipt_event.get("model", ""), "context": row.get("recalled", recall.get("data", {}).get("memories", [])),
            **receipt,
        }
        candidates = [c for c in candidate_snapshots.values() if c.get("run_id") == run_id]
        recalled_ids = {m["id"] for m in (run["context"] or [])}
        memories = [m for m in memory_snapshots.values() if m.get("source_run_id") == run_id or m["id"] in recalled_ids]
        key = source["id"] + ":" + run_id
        result[run_id] = {
            "key": key, "run": run, "events": events, "candidates": candidates,
            "memories": memories, "source": source, "snapshot": True,
            "receipt": receipt_event, "experiment": {
                "passed": report.get("passed"), "failure": report.get("failure"),
                "assertions": report.get("assertions", []), "native_version": report.get("native_version"),
                "binary_sha256": report.get("binary_sha256"), "external_tests": report.get("external_tests", []),
            },
        }
    return result


class Catalog:
    def __init__(self, home, reports, workspace=None, project=None, agent="default", audience="owner"):
        if workspace and project:
            raise ValueError("--project and --workspace are mutually exclusive")
        self.database = Path(home).expanduser().resolve() / "state.db"
        self.reports = Path(reports).expanduser().resolve() if reports is not None else None
        self.workspace = str(Path(workspace).expanduser().resolve()) if workspace else None
        self.project = project
        self.agent, self.audience = agent, audience
        if not agent or not audience:
            raise ValueError("agent and audience must be nonempty identifiers")

    def connection(self):
        conn = sqlite3.connect(self.database.as_uri() + "?mode=ro", uri=True, timeout=2)
        conn.execute("PRAGMA query_only=ON")
        conn.execute("BEGIN")
        return conn

    def schema_version(self, conn):
        version = conn.execute("PRAGMA user_version").fetchone()[0]
        if version not in (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14):
            raise ValueError(f"unsupported state schema: {version}")
        return version

    def filter_context(self, conn=None):
        """Resolve a current project selection to its stable memory namespace."""
        version = self.schema_version(conn) if conn else None
        result = {"project": self.project, "project_name": None,
                  "workspace": self.workspace, "scope": self.workspace,
                  "agent_id": self.agent, "audience_id": self.audience}
        if self.project or self.workspace:
            row = None
            if version is not None and version >= 7:
                field, value = ("id", self.project) if self.project else ("workspace", self.workspace)
                row = conn.execute(f"SELECT id,name,workspace,scope FROM projects WHERE {field}=?", (value,)).fetchone()
            if row:
                result.update(zip(("project", "project_name", "workspace", "scope"), row))
            elif self.project:
                raise ValueError(f"unknown project: {self.project}")
        return result

    def archives(self):
        if self.reports is None:
            return {}
        records = {}
        for path in sorted(self.reports.glob("*.json")):
            if path.stat().st_size > MAX_REPORT_BYTES:
                raise ValueError(f"report exceeds 32 MiB: {path.name}")
            report = decode(path.read_text())
            source = {"id": "report-" + hashlib.sha256(str(path).encode()).hexdigest()[:16],
                      "kind": "report", "name": path.name, "passed": report.get("passed")}
            for record in report_records(report, source).values():
                if partition(record["run"]) == (self.agent, self.audience):
                    records[record["key"]] = record
        return records

    def run_filter(self, context):
        scope, project = context["scope"], context["project"]
        condition = " WHERE " + partition_sql("runs.payload")
        args = (self.agent, self.audience)
        if project:
            return (condition + " AND (scope=? OR EXISTS (SELECT 1 FROM json_each(runs.payload, '$.projects') "
                    "WHERE json_extract(value, '$.id')=?))", args + (scope, project))
        return (condition + " AND scope=?", args + (scope,)) if scope is not None else (condition, args)

    def memory_filter(self, context, version, alias="m"):
        prefix = alias + "." if alias else ""
        condition = prefix + "agent_id=? AND " + prefix + "audience_id=?" if version >= 10 else partition_sql(prefix + "payload")
        args = (self.agent, self.audience)
        if context["scope"] is not None:
            condition += " AND " + prefix + "scope=?"
            args += (context["scope"],)
        return " WHERE " + condition, args

    def index(self):
        rows = []
        total = 0
        if self.database.exists():
            conn = self.connection()
            try:
                context = self.filter_context(conn)
                scope = context["scope"]
                condition, arguments = self.run_filter(context)
                total = conn.execute("SELECT count(*) FROM runs" + condition, arguments).fetchone()[0]
                for (payload,) in conn.execute("SELECT payload FROM runs" + condition + " ORDER BY created_at DESC,rowid DESC LIMIT ?", arguments + (RUN_LIMIT,)):
                    run = decode(payload)
                    rows.append(summary("db:" + run["id"], run, {"id": "db", "kind": "database", "name": "本机 state.db"}))
            finally:
                conn.close()
        else:
            context = self.filter_context()
        for record in self.archives().values():
            review_count = sum(e["type"] == "candidate.accepted" for e in record["events"])
            rows.append(summary(record["key"], record["run"], record["source"], len(record["events"]), review_count))
        rows.sort(key=lambda row: row["created_at"], reverse=True)
        return {"records": rows, "database_present": self.database.exists(), "database_count": total,
                "database_limit": RUN_LIMIT, **context,
                "default_key": next((r["key"] for r in rows if r["execution_status"] == "running"),
                                    next((r["key"] for r in rows if r["review_events"]), rows[0]["key"] if rows else None))}

    def record(self, key):
        if not key.startswith("db:"):
            records = self.archives()
            if key not in records:
                raise KeyError("record not found")
            return records[key]
        conn = self.connection()
        try:
            schema = self.schema_version(conn)
            context = self.filter_context(conn)
            scope = context["scope"]
            row = conn.execute("SELECT payload FROM runs WHERE id=?", (key[3:],)).fetchone()
            if row is None:
                raise KeyError("run not found")
            run = decode(row[0])
            if partition(run) != (self.agent, self.audience):
                raise KeyError("run outside configured agent or audience")
            member = any(p.get("id") == context["project"] for p in run.get("projects", [])) if context["project"] else False
            if scope is not None and run.get("scope") != scope and not member:
                raise KeyError("run outside configured project or workspace")
            events = [decode(row[0]) for row in conn.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (run["id"],))]
            candidates = [] if schema == 1 else [decode(row[0]) for row in conn.execute("SELECT payload FROM candidates WHERE run_id=? ORDER BY rowid", (run["id"],))]
            scopes = sorted({p["scope"] for p in run.get("projects", [])}) or [run["scope"]]
            memory_where, memory_args = self.memory_filter({"scope": None}, schema, "")
            memories = [decode(row[0]) for row in conn.execute(
                "SELECT payload FROM memories" + memory_where + " AND scope IN (" + ",".join("?" for _ in scopes) + ")", memory_args + tuple(scopes))]
            return {"key": key, "run": run, "events": events, "candidates": candidates, "memories": memories,
                    "source": {"id": "db", "kind": "database", "name": "本机 state.db"}, "snapshot": False,
                    "receipt": next((e for e in reversed(events) if e["type"] == "run.finished"), {})}
        finally:
            conn.close()


class Handler(BaseHTTPRequestHandler):
    def send(self, status, data, content_type="application/json; charset=utf-8"):
        body = json.dumps(data, ensure_ascii=False).encode() if isinstance(data, dict) else data
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.headers.get("Host") not in {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"}:
            self.send(403, {"error": "loopback Host required"})
            return
        target = urlsplit(self.path)
        try:
            if target.path == "/api/records":
                self.send(200, self.server.catalog.index())
            elif target.path == "/api/memories":
                self.send(200, memory_catalog(self.server.catalog))
            elif target.path == "/api/record":
                keys = parse_qs(target.query).get("key", [])
                if len(keys) != 1:
                    self.send(400, {"error": "provide exactly one record key"})
                else:
                    self.send(200, self.server.catalog.record(keys[0]))
            elif target.path in {"/", "/index.html", "/app.js", "/style.css", "/memory.html", "/memory.js", "/memory.css"}:
                name = "index.html" if target.path == "/" else target.path[1:]
                mime = {".html": "text/html", ".js": "text/javascript", ".css": "text/css"}[Path(name).suffix]
                self.send(200, (ASSETS / name).read_bytes(), mime + "; charset=utf-8")
            else:
                self.send(404, {"error": "route not found"})
        except KeyError as error:
            self.send(404, {"error": str(error)})
        except (OSError, ValueError, sqlite3.Error) as error:
            self.send(500, {"error": f"record read failed: {error}"})

    def do_POST(self):
        self.send(405, {"error": "viewer is read-only"})

    do_PUT = do_DELETE = do_PATCH = do_POST

    def log_message(self, format, *args):
        # Do not log record payloads or request query strings.
        if args and isinstance(args[0], str) and args[0].startswith("GET /api/"):
            return
        super().log_message(format, *args)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, default=Path.cwd() / ".agent-host" / "runtime" / "state")
    parser.add_argument("--reports", type=Path, default=ROOT / "experiments/experience/results")
    parser.add_argument("--no-reports", action="store_true", help="Read only current database state, without archived reports")
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--workspace", help="Limit database records to this workspace's registered project or legacy scope")
    selection.add_argument("--project", help="Limit database records to this registered project ID")
    parser.add_argument("--agent", default="default", help="Agent instance ID (default: default)")
    parser.add_argument("--audience", default="owner", help="Audience ID (default: owner)")
    parser.add_argument("--port", type=int, default=8765, help="Loopback port; 0 chooses an available port")
    args = parser.parse_args()
    catalog = Catalog(args.home, None if args.no_reports else args.reports, args.workspace, args.project, args.agent, args.audience)
    try:
        if catalog.database.exists():
            conn = catalog.connection()
            try:
                catalog.filter_context(conn)
            finally:
                conn.close()
        else:
            catalog.filter_context()
    except (OSError, ValueError, sqlite3.Error) as error:
        parser.error(str(error))
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.catalog = catalog
    print(f"Execution viewer: http://127.0.0.1:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
