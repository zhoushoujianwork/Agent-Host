#!/usr/bin/env python3
"""Serve an owner-local workbench with Host-owned memory controls."""

import argparse
import hashlib
import json
import http.client
from pathlib import Path
import secrets
import socket
import sqlite3
import stat
import os
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

    def control_available(self):
        path = self.database.parent / "memory-control.sock"
        try:
            info = path.lstat()
            return self.audience == "owner" and stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid() and not info.st_mode & 0o077
        except FileNotFoundError:
            return False

    def candidates(self):
        if not self.database.exists():
            return []
        conn = self.connection()
        try:
            version = self.schema_version(conn)
            if version < 2:
                return []
            context = self.filter_context(conn)
            where, args = self.memory_filter(context, version, "c")
            rows = conn.execute("SELECT c.payload FROM candidates c" + where + " ORDER BY c.rowid DESC LIMIT 200", args)
            return [{"key": "db:" + c["id"], "candidate": c,
                     "source": {"id": "db", "kind": "database", "name": "本机 state.db"},
                     "origin_run": {"key": "db:" + c["run_id"]},
                     "can_manage": self.control_available()} for (raw,) in rows for c in [decode(raw)]]
        finally:
            conn.close()

    def memories(self):
        result = memory_catalog(self)
        result["candidates"] = self.candidates()
        available = self.control_available()
        for item in result["memories"]:
            item["can_manage"] = available and not item["snapshot"]
        return result

    def action(self, key, action, note, revision):
        if not key.startswith("db:") or not self.control_available():
            raise ValueError("only live owner records in a running Host can be managed")
        identifier = key[3:]
        conn = self.connection()
        try:
            context = self.filter_context(conn)
            table = "memories" if action in {"invalidate", "restructure"} else "candidates"
            where, args = self.memory_filter(context, self.schema_version(conn), "m")
            row = conn.execute("SELECT m.payload FROM " + table + " m" + where + " AND m.id=?", args + (identifier,)).fetchone()
            if row is None:
                raise KeyError("record outside the selected source or scope")
        finally:
            conn.close()
        connection = http.client.HTTPConnection("localhost", timeout=100)
        connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.sock.settimeout(100)
        try:
            connection.sock.connect(str(self.database.parent / "memory-control.sock"))
            body = {"action": action, "id": identifier, "agent_id": self.agent,
                    "audience_id": self.audience, "note": note, "revision": revision}
            connection.request("POST", "/memory-action", json.dumps(body), {"Content-Type": "application/json"})
            response = connection.getresponse()
            raw = response.read((1 << 20) + 1)
            if len(raw) > 1 << 20:
                raise ValueError("Host response exceeded its bound; refresh to inspect the outcome")
            return response.status, decode(raw)
        finally:
            connection.close()

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


class MultiCatalog:
    """Combine channel projections without merging their underlying state."""

    def __init__(self, sources, agent="default", audience="owner"):
        self.catalogs = {}
        self.agent, self.audience = agent, audience
        for source in sources:
            name, separator, home = source.partition("=")
            if not separator or not name or not home or name in self.catalogs:
                raise ValueError("--channel-home requires a unique NAME=PATH")
            self.catalogs[name] = Catalog(home, None, agent=agent, audience=audience)

    @staticmethod
    def qualify(value, channel):
        # Change projection links only; stored IDs and evidence stay intact.
        if isinstance(value, list):
            return [MultiCatalog.qualify(item, channel) for item in value]
        if not isinstance(value, dict):
            return value
        result = dict(value)
        for key in ("records", "memories", "candidates", "sources", "recalls", "uses", "origin_run", "source"):
            if key in result:
                result[key] = MultiCatalog.qualify(result[key], channel)
        for key in ("key", "annotation_record_key"):
            if isinstance(result.get(key), str):
                result[key] = channel + ":" + result[key]
        if result.get("kind") in ("database", "report") and "id" in result:
            result["id"] = channel + ":" + result["id"]
            result["name"] = channel + " · " + result.get("name", "")
            result["channel"] = channel
        return result

    def index(self):
        indexes = [self.qualify(catalog.index(), name) for name, catalog in self.catalogs.items()]
        rows = [row for index in indexes for row in index["records"]]
        rows.sort(key=lambda row: row["created_at"], reverse=True)
        return {"records": rows, "database_present": any(index["database_present"] for index in indexes),
                "database_count": sum(index["database_count"] for index in indexes),
                "database_limit": RUN_LIMIT * len(indexes), "channels": list(self.catalogs),
                "agent_id": self.agent, "audience_id": self.audience,
                "default_key": next((row["key"] for row in rows if row["execution_status"] == "running"),
                                    rows[0]["key"] if rows else None)}

    def record(self, key):
        channel, separator, reference = key.partition(":")
        if not separator or channel not in self.catalogs:
            raise KeyError("record channel not found")
        return self.qualify(self.catalogs[channel].record(reference), channel)

    def memories(self):
        catalogs = [self.qualify(catalog.memories(), name) for name, catalog in self.catalogs.items()]
        result = {key: [item for catalog in catalogs for item in catalog[key]]
                  for key in ("memories", "candidates", "sources", "recalls")}
        result["memories"].sort(key=lambda item: item["memory"].get("created_at", ""), reverse=True)
        result["recalls"].sort(key=lambda item: item.get("created_at") or "", reverse=True)
        result.update({key: sum(catalog[key] for catalog in catalogs)
                       for key in ("database_total", "database_runs_total", "observed_total", "memory_limit")})
        result.update(database_present=any(catalog["database_present"] for catalog in catalogs),
                      truncated=any(catalog["truncated"] for catalog in catalogs),
                      agent_id=self.agent, audience_id=self.audience, channels=list(self.catalogs))
        return result

    def action(self, key, action, note, revision):
        channel, separator, reference = key.partition(":")
        if not separator or channel not in self.catalogs:
            raise KeyError("record channel not found")
        return self.catalogs[channel].action(reference, action, note, revision)


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
                result = self.server.catalog.memories()
                result["mutation_token"] = getattr(self.server, "mutation_token", None)
                self.send(200, result)
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
        token = getattr(self.server, "mutation_token", None)
        if urlsplit(self.path).path != "/api/memory-action" or not token:
            self.send(405, {"error": "this route is read-only"})
            return
        host = self.headers.get("Host", "")
        if host not in {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"} or self.headers.get("Origin") != "http://" + host or not secrets.compare_digest(self.headers.get("X-Host-Action", "").encode(), token.encode()):
            self.send(403, {"error": "same-origin owner action required"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 8192 or self.headers.get("Content-Type") != "application/json" or self.headers.get("Transfer-Encoding"):
                raise ValueError("provide a bounded JSON action")
            self.connection.settimeout(10)
            value = decode(self.rfile.read(length))
            if set(value) != {"key", "action", "note", "revision"} or not all(isinstance(value[k], str) for k in ("key", "action", "note")) or type(value["revision"]) is not int or len(value["note"]) > 2000 or len(value["key"]) > 256 or value["action"] not in {"accept", "reject", "invalidate", "restructure"}:
                raise ValueError("invalid memory action")
            status, result = self.server.catalog.action(value["key"], value["action"], value["note"], value["revision"])
            self.send(status, result)
        except KeyError:
            self.send(404, {"error": "record outside the selected source or scope"})
        except (ValueError, sqlite3.Error) as error:
            self.send(400, {"error": str(error)})
        except (OSError, http.client.HTTPException):
            self.send(503, {"error": "Host control unavailable or interrupted; refresh to inspect the outcome before retrying"})

    def do_PUT(self):
        self.send(405, {"error": "unsupported operation"})

    do_DELETE = do_PATCH = do_PUT

    def log_message(self, format, *args):
        # Do not log record payloads or request query strings.
        if args and isinstance(args[0], str) and args[0].startswith("GET /api/"):
            return
        super().log_message(format, *args)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, default=Path.cwd() / ".agent-host" / "runtime" / "state")
    parser.add_argument("--channel-home", action="append", default=[], help="Unified channel source NAME=PATH; repeat for each enabled channel")
    parser.add_argument("--reports", type=Path, default=ROOT / "experiments/experience/results")
    parser.add_argument("--no-reports", action="store_true", help="Read only current database state, without archived reports")
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--workspace", help="Limit database records to this workspace's registered project or legacy scope")
    selection.add_argument("--project", help="Limit database records to this registered project ID")
    parser.add_argument("--agent", default="default", help="Agent instance ID (default: default)")
    parser.add_argument("--audience", default="owner", help="Audience ID (default: owner)")
    parser.add_argument("--port", type=int, default=8765, help="Loopback port; 0 chooses an available port")
    args = parser.parse_args()
    try:
        if args.channel_home:
            if args.workspace or args.project or not args.no_reports:
                parser.error("--channel-home requires --no-reports and no project/workspace filter")
            catalog = MultiCatalog(args.channel_home, args.agent, args.audience)
            catalog.index()
        else:
            catalog = Catalog(args.home, None if args.no_reports else args.reports, args.workspace, args.project, args.agent, args.audience)
            catalog.index()
    except (OSError, ValueError, sqlite3.Error) as error:
        parser.error(str(error))
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.catalog = catalog
    server.mutation_token = secrets.token_urlsafe(32)
    print(f"Execution viewer: http://127.0.0.1:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
