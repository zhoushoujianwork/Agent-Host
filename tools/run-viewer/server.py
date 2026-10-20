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
import fcntl
import subprocess
import threading
import time
import signal
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

from memory_view import memory_catalog, normalize_report
from settings_store import SettingsStore, Conflict
from chat_runtime import ChatRuntime, ChatConflict, default_config

ROOT = Path(__file__).resolve().parents[2]
ASSETS = Path(__file__).resolve().parent
MAX_REPORT_BYTES = 32 << 20
RUN_LIMIT = 200
MAX_MEMORY_FILE_BYTES = 4 << 20


class MemoryConnection(sqlite3.Connection):
    """Keep a file-publication read lock until the projection has finished."""

    publication_lock = None

    def close(self):
        try:
            super().close()
        finally:
            if self.publication_lock is not None:
                fcntl.flock(self.publication_lock.fileno(), fcntl.LOCK_UN)
                self.publication_lock.close()
                self.publication_lock = None


def bounded_memory_file(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise ValueError(f"file memory requires a private regular file: {path}")
    if path.resolve() != path:
        raise ValueError(f"file memory path contains a symlink: {path}")
    with path.open("rb") as handle:
        body = handle.read(MAX_MEMORY_FILE_BYTES + 1)
    if len(body) > MAX_MEMORY_FILE_BYTES:
        raise ValueError(f"file memory exceeds its read bound: {path}")
    return body


def memory_object(path):
    return decode(bounded_memory_file(path))


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
    def __init__(self, home, reports, workspace=None, project=None, agent="default", audience="owner", host_binary=None, all_chats=False, private_home=False):
        if workspace and project:
            raise ValueError("--project and --workspace are mutually exclusive")
        self.database = Path(home).expanduser().resolve() / "state.db"
        self.private_home = Path(home).expanduser().absolute() if private_home else None
        self.reports = Path(reports).expanduser().resolve() if reports is not None else None
        self.workspace = str(Path(workspace).expanduser().resolve()) if workspace else None
        self.project = project
        self.agent, self.audience = agent, audience
        self.all_chats = all_chats
        self.host_binary = Path(host_binary).expanduser().resolve() if host_binary else ROOT / "bin" / "agent-host"
        self._memory_validation_lock = threading.Lock()
        self._verified_memory_signature = None
        self._memory_verification_error = None
        if not agent or not audience:
            raise ValueError("agent and audience must be nonempty identifiers")

    def connection(self):
        self.validate_private_home()
        conn = sqlite3.connect(self.database.as_uri() + "?mode=ro", uri=True, timeout=2, factory=MemoryConnection)
        conn.execute("PRAGMA query_only=ON")
        conn.execute("BEGIN")
        return conn

    def validate_private_home(self):
        if self.private_home is None:
            return
        if self.private_home.resolve() != self.private_home:
            raise ValueError("CLI state home must not contain symlinks")
        for path, directory in ((self.private_home, True), (self.private_home / "state.db", False)):
            try:
                info = path.lstat()
            except FileNotFoundError:
                continue
            valid_type = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
            if not valid_type or info.st_uid != os.getuid() or info.st_mode & 0o077:
                raise ValueError("CLI state requires private current-user directories and regular files")

    def file_memory_marker(self):
        path = self.database.parent / "file-memory.json"
        try:
            path.lstat()
        except FileNotFoundError:
            return None
        marker = memory_object(path)
        if set(marker) != {"version", "memory_root", "cache_root", "source_id", "state_home"} or type(marker.get("version")) is not int or marker["version"] != 1 or marker.get("state_home") != str(self.database.parent) or not isinstance(marker.get("source_id"), str) or not marker["source_id"]:
            raise ValueError("invalid file-memory backend marker")
        for key in ("memory_root", "cache_root"):
            value = marker[key]
            if not isinstance(value, str) or not Path(value).is_absolute() or str(Path(value)) != value:
                raise ValueError(f"invalid file-memory {key}")
        return marker

    def verify_file_memory(self):
        if not self.host_binary.is_file() or not os.access(self.host_binary, os.X_OK):
            raise ValueError("file memory requires an executable Host verifier; provide --host-binary")
        try:
            completed = subprocess.run([str(self.host_binary), "memory", "verify", "--home", str(self.database.parent)],
                                       capture_output=True, timeout=15, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ValueError(f"file-memory verification unavailable: {error}") from error
        if completed.returncode != 0:
            diagnostic = completed.stderr[:2000].decode("utf-8", errors="replace").strip()
            raise ValueError(f"file-memory verification failed: {diagnostic or 'Host verifier returned an error'}")
        if len(completed.stdout) > 1 << 20:
            raise ValueError("file-memory verification report exceeded its bound")
        result = decode(completed.stdout)
        if type(result.get("generation")) is not int or result["generation"] < 1:
            raise ValueError("file-memory verifier did not report a publication generation")
        return result

    def memory_connection(self):
        # A Catalog is shared by HTTP threads. Only one request verifies a changed
        # publication; followers reuse the successful result, never an old one.
        with self._memory_validation_lock:
            return self._memory_connection()

    def memory_signature(self, marker, manifest, source, index):
        """Track all published revisions, including edits without a new generation."""
        paths = {self.database.parent / "file-memory.json", source / "current.json",
                 source / "publish.lock", self.host_binary}
        domains, entries = manifest.get("domains") or [], manifest.get("entries") or []
        if not isinstance(domains, list) or not isinstance(entries, list):
            raise ValueError("file-memory publication requires domain and entry arrays")
        for domain in domains:
            if not isinstance(domain, dict):
                raise ValueError("invalid file-memory domain identity")
            identifier = domain.get("id")
            if not isinstance(identifier, str) or len(identifier) != 32 or any(c not in "0123456789abcdef" for c in identifier):
                raise ValueError("invalid file-memory domain identity")
            paths.add(Path(marker["memory_root"]) / "domains" / identifier / "scope.json")
        for entry in entries:
            if not isinstance(entry, dict) or not isinstance(entry.get("revisions"), list):
                raise ValueError("invalid file-memory revision set")
            identifier, domain, directory = entry.get("id"), entry.get("domain"), entry.get("directory")
            if (not isinstance(identifier, str) or not identifier or identifier in {".", ".."}
                    or any(c in identifier for c in "/\\\0") or not isinstance(domain, str)
                    or len(domain) != 32 or any(c not in "0123456789abcdef" for c in domain)
                    or directory not in {"entries", "skills"}):
                raise ValueError("invalid file-memory publication entry")
            for revision in entry.get("revisions", []):
                number = revision.get("revision") if isinstance(revision, dict) else None
                if type(number) is not int or number < 1:
                    raise ValueError("invalid file-memory revision")
                base = Path(marker["memory_root"]) / "domains" / domain / directory / identifier / "revisions" / str(number)
                paths.update((base / "record.json", base / ("SKILL.md" if directory == "skills" else "content.md")))

        def fingerprint(files):
            # Include ancestors so replacing a directory with a symlink cannot
            # preserve a cached result. Directory timestamps are irrelevant.
            dependencies = set(files)
            for path in files:
                dependencies.update(path.parents)
            result = []
            for path in sorted(dependencies):
                try:
                    info = path.lstat()
                except FileNotFoundError:
                    result.append((str(path), None))
                    continue
                if stat.S_ISLNK(info.st_mode):
                    raise ValueError("file-memory path contains a symlink")
                identity = (info.st_dev, info.st_ino, info.st_mode)
                if not stat.S_ISDIR(info.st_mode):
                    identity += (info.st_size, info.st_mtime_ns, info.st_ctime_ns)
                result.append((str(path), identity))
            return tuple(result)

        return fingerprint(paths), fingerprint({index, Path(str(index) + "-wal")})

    def _memory_connection(self, verified_authority=None):
        marker = self.file_memory_marker()
        if marker is None:
            self._verified_memory_signature = None
            return self.connection()
        source_key = hashlib.sha256(marker["source_id"].encode()).hexdigest()[:24]
        source = Path(marker["memory_root"]) / "sources" / source_key
        lock_path = source / "publish.lock"
        info = lock_path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or lock_path.resolve() != lock_path:
            raise ValueError("unsafe file-memory publication lock")
        lock = lock_path.open("rb")
        conn = None
        try:
            deadline = time.monotonic() + 2
            while True:
                try:
                    fcntl.flock(lock.fileno(), fcntl.LOCK_SH | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise ValueError("file-memory publication is busy; refresh after it completes")
                    time.sleep(0.01)
            # Re-read the marker after taking the lock; never mix backend roots.
            if marker != self.file_memory_marker():
                raise ValueError("file-memory backend changed during verification; refresh")
            manifest = memory_object(source / "current.json")
            index = Path(marker["cache_root"]) / source_key / "index.db"
            signature = self.memory_signature(marker, manifest, source, index)
            if verified_authority is not None and signature[0] != verified_authority:
                raise ValueError("file-memory publication changed during verification; refresh")
            if signature != self._verified_memory_signature and verified_authority is None:
                self._verified_memory_signature = None
                failure = self._memory_verification_error
                if failure and failure[0] == signature and time.monotonic() < failure[1]:
                    raise ValueError(failure[2])
                # Host verification takes the exclusive publication lock. Release
                # this read lock first, then recheck under a fresh shared lock.
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
                lock.close()
                lock = None
                try:
                    self.verify_file_memory()
                except ValueError as error:
                    # Waiting readers see the same failure promptly. A later
                    # refresh may retry; changed files bypass this short cooldown.
                    self._memory_verification_error = (signature, time.monotonic() + 1, str(error))
                    raise
                self._memory_verification_error = None
                return self._memory_connection(signature[0])
            if index.resolve() != index or not index.is_file():
                raise ValueError("verified file-memory cache is missing or has an unsafe path")
            conn = self.connection()
            conn.publication_lock = lock
            conn.execute("ATTACH DATABASE ? AS published_memory", (index.as_uri() + "?mode=ro",))
            if signature != self._verified_memory_signature:
                self.validate_memory_cache(conn, marker, manifest)
                if signature != self.memory_signature(marker, manifest, source, index):
                    raise ValueError("file-memory files changed during validation; refresh")
                self._verified_memory_signature = signature
            return conn
        except Exception:
            if conn is not None:
                conn.close()
            elif lock is not None:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)
                lock.close()
            raise

    def validate_memory_cache(self, conn, marker, manifest):
        if manifest.get("version") != 1 or manifest.get("source_id") != marker["source_id"] or manifest.get("state_home") != marker["state_home"] or type(manifest.get("generation")) is not int or manifest["generation"] < 1:
            raise ValueError("file-memory publication identity is invalid")
        row = conn.execute("SELECT generation FROM published_memory.file_index_checkpoint WHERE id=1").fetchone()
        if row is None or row[0] != manifest["generation"]:
            raise ValueError("file-memory cache generation differs from publication; refresh to rebuild")
        if conn.execute("PRAGMA published_memory.quick_check").fetchone() != ("ok",):
            raise ValueError("file-memory cache integrity check failed")
        memories = {}
        # Go encodes nil publication slices as null for an empty source. Only
        # these two arrays have that empty representation; other types still fail.
        domain_records = manifest.get("domains")
        entry_records = manifest.get("entries")
        if domain_records is None:
            domain_records = []
        if entry_records is None:
            entry_records = []
        if not isinstance(domain_records, list) or not isinstance(entry_records, list):
            raise ValueError("file-memory publication requires domain and entry arrays")
        domains = {}
        for domain in domain_records:
            if not isinstance(domain, dict) or not isinstance(domain.get("id"), str) or domain["id"] in domains:
                raise ValueError("file-memory publication contains an invalid or duplicate domain")
            domains[domain["id"]] = domain
        for entry in entry_records:
            if not isinstance(entry, dict) or not isinstance(entry.get("revisions"), list) or not all(isinstance(reference, dict) for reference in entry["revisions"]):
                raise ValueError("file-memory publication contains an invalid revision set")
            identifier, domain_id, directory = entry.get("id"), entry.get("domain"), entry.get("directory")
            if not isinstance(identifier, str) or not identifier or identifier in {".", ".."} or any(character in identifier for character in "/\\\0") or identifier in memories or not isinstance(domain_id, str) or domain_id not in domains or directory not in {"entries", "skills"}:
                raise ValueError("file-memory publication contains an invalid entry")
            domain = domains[domain_id]
            if domain.get("source_id") != marker["source_id"] or domain.get("state_home") != marker["state_home"] or not isinstance(domain_id, str) or len(domain_id) != 32 or any(character not in "0123456789abcdef" for character in domain_id):
                raise ValueError("file-memory domain identity is invalid")
            reference = next((revision for revision in entry.get("revisions", []) if revision.get("revision") == entry.get("current")), None)
            if reference is None or type(entry.get("current")) is not int or entry["current"] < 1:
                raise ValueError("file-memory publication has no current revision")
            base = Path(marker["memory_root"]) / "domains" / domain_id / directory / identifier / "revisions" / str(entry["current"])
            raw = bounded_memory_file(base / "record.json")
            content = bounded_memory_file(base / ("SKILL.md" if directory == "skills" else "content.md"))
            if hashlib.sha256(raw).hexdigest() != reference.get("record_sha256") or hashlib.sha256(content).hexdigest() != reference.get("content_sha256"):
                raise ValueError(f"file-memory publication checksum mismatch: {identifier}")
            record = decode(raw)
            memory = record.get("memory")
            if record.get("version") != 1 or record.get("source_id") != marker["source_id"] or record.get("state_home") != marker["state_home"] or not isinstance(memory, dict) or memory.get("id") != identifier or memory.get("revision") != entry["current"] or memory.get("scope") != domain.get("scope") or partition(memory) != (domain.get("agent_id"), domain.get("audience_id")):
                raise ValueError("file-memory record attribution differs from publication")
            memories[identifier] = memory
        indexed = {identifier: decode(payload) for identifier, payload in conn.execute("SELECT id,payload FROM published_memory.memories")}
        if indexed != memories:
            raise ValueError("file-memory cache differs from authoritative records; rebuild the cache")

    @staticmethod
    def memory_table(conn, table="memories"):
        if table not in {"memories", "memory_annotations"}:
            raise ValueError("unsupported memory projection table")
        names = {row[1] for row in conn.execute("PRAGMA database_list")}
        return ("published_memory." if "published_memory" in names else "") + table

    def memory_source(self):
        return {"id": "db", "kind": "database", "name": "文件记忆（已验证索引）" if self.file_memory_marker() else "本机 state.db"}

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
                     "can_manage": self.control_available() and partition(c) == (self.agent, self.audience)} for (raw,) in rows for c in [decode(raw)]]
        finally:
            conn.close()

    def memories(self):
        result = memory_catalog(self)
        result["candidates"] = self.candidates()
        available = self.control_available()
        for item in result["memories"]:
            item["can_manage"] = available and not item["snapshot"] and partition(item["memory"]) == (self.agent, self.audience)
        return result

    def action(self, key, action, note, revision):
        if not key.startswith("db:") or not self.control_available():
            raise ValueError("only live owner records in a running Host can be managed")
        identifier = key[3:]
        conn = self.memory_connection() if action in {"invalidate", "restructure"} else self.connection()
        try:
            context = {**self.filter_context(conn), "all_chats": False}
            table = self.memory_table(conn) if action in {"invalidate", "restructure"} else "candidates"
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
        if version not in (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15):
            raise ValueError(f"unsupported state schema: {version}")
        return version

    def filter_context(self, conn=None):
        """Resolve a current project selection to its stable memory namespace."""
        version = self.schema_version(conn) if conn else None
        result = {"project": self.project, "project_name": None,
                  "workspace": self.workspace, "scope": self.workspace,
                  "agent_id": self.agent, "audience_id": self.audience, "all_chats": self.all_chats}
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
                if self.all_chats or partition(record["run"]) == (self.agent, self.audience):
                    records[record["key"]] = record
        return records

    def run_filter(self, context):
        scope, project = context["scope"], context["project"]
        condition = " WHERE 1=1" if self.all_chats else " WHERE " + partition_sql("runs.payload")
        args = () if self.all_chats else (self.agent, self.audience)
        if project:
            return (condition + " AND (scope=? OR EXISTS (SELECT 1 FROM json_each(runs.payload, '$.projects') "
                    "WHERE json_extract(value, '$.id')=?))", args + (scope, project))
        return (condition + " AND scope=?", args + (scope,)) if scope is not None else (condition, args)

    def memory_filter(self, context, version, alias="m"):
        prefix = alias + "." if alias else ""
        condition = prefix + "agent_id=? AND " + prefix + "audience_id=?" if version >= 10 else partition_sql(prefix + "payload")
        args = (context.get("agent_id", self.agent), context.get("audience_id", self.audience))
        if context.get("all_chats", self.all_chats):
            condition, args = "1=1", ()
        if context["scope"] is not None:
            condition += " AND " + prefix + "scope=?"
            args += (context["scope"],)
        return " WHERE " + condition, args

    def index(self):
        self.validate_private_home()
        rows = []
        total = 0
        source_status = "missing_database"
        if self.database.exists():
            conn = self.connection()
            try:
                context = self.filter_context(conn)
                scope = context["scope"]
                condition, arguments = self.run_filter(context)
                total = conn.execute("SELECT count(*) FROM runs" + condition, arguments).fetchone()[0]
                source_status = "ready" if total else ("scope_filtered" if conn.execute("SELECT EXISTS(SELECT 1 FROM runs)").fetchone()[0] else "no_runs")
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
                "sources": [{"id": "db", "kind": "database", "name": "本机 state.db",
                             "home": str(self.database.parent), "status": source_status,
                             "record_count": total}],
                "database_limit": RUN_LIMIT, **context,
                "default_key": next((r["key"] for r in rows if r["execution_status"] == "running"),
                                    next((r["key"] for r in rows if r["review_events"]), rows[0]["key"] if rows else None))}

    def record(self, key):
        if not key.startswith("db:"):
            records = self.archives()
            if key not in records:
                raise KeyError("record not found")
            return records[key]
        conn = self.memory_connection()
        try:
            schema = self.schema_version(conn)
            context = self.filter_context(conn)
            scope = context["scope"]
            row = conn.execute("SELECT payload FROM runs WHERE id=?", (key[3:],)).fetchone()
            if row is None:
                raise KeyError("run not found")
            run = decode(row[0])
            if not self.all_chats and partition(run) != (self.agent, self.audience):
                raise KeyError("run outside configured agent or audience")
            member = any(p.get("id") == context["project"] for p in run.get("projects", [])) if context["project"] else False
            if scope is not None and run.get("scope") != scope and not member:
                raise KeyError("run outside configured project or workspace")
            events = [decode(row[0]) for row in conn.execute("SELECT payload FROM events WHERE run_id=? ORDER BY seq", (run["id"],))]
            candidates = [] if schema == 1 else [decode(row[0]) for row in conn.execute("SELECT payload FROM candidates WHERE run_id=? ORDER BY rowid", (run["id"],))]
            scopes = sorted({p["scope"] for p in run.get("projects", [])}) or [run["scope"]]
            memory_where, memory_args = self.memory_filter({"scope": None, "all_chats": False, "agent_id": partition(run)[0], "audience_id": partition(run)[1]}, schema, "")
            memories = [decode(row[0]) for row in conn.execute(
                "SELECT payload FROM " + self.memory_table(conn) + memory_where + " AND scope IN (" + ",".join("?" for _ in scopes) + ")", memory_args + tuple(scopes))]
            return {"key": key, "run": run, "events": events, "candidates": candidates, "memories": memories,
                    "source": {"id": "db", "kind": "database", "name": "本机 state.db"}, "snapshot": False,
                    "receipt": next((e for e in reversed(events) if e["type"] == "run.finished"), {})}
        finally:
            conn.close()


class MultiCatalog:
    """Combine channel projections without merging their underlying state."""

    def __init__(self, sources, agent="default", audience="owner", host_binary=None, all_chats=False):
        self.catalogs = {}
        self.agent, self.audience = agent, audience
        self.all_chats = all_chats
        for source in sources:
            name, separator, home = source.partition("=")
            if not separator or not name or not home or name in self.catalogs:
                raise ValueError("--channel-home requires a unique NAME=PATH")
            self.catalogs[name] = Catalog(home, None, agent=agent, audience=audience, host_binary=host_binary, all_chats=all_chats, private_home=name == "cli")

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
        indexes = []
        for name, catalog in self.catalogs.items():
            try:
                index = catalog.index()
            except (OSError, ValueError, sqlite3.Error) as error:
                # A failed source remains visible as a failure alongside healthy
                # sources. It must never look like an empty successful read.
                index = {"records": [], "database_present": catalog.database.exists(), "database_count": 0,
                         "sources": [{"id": "db", "kind": "database", "name": "本机 state.db",
                                      "home": str(catalog.database.parent), "status": "read_failed",
                                      "error": str(error), "record_count": None}]}
            indexes.append(self.qualify(index, name))
        rows = [row for index in indexes for row in index["records"]]
        rows.sort(key=lambda row: row["created_at"], reverse=True)
        return {"records": rows, "database_present": any(index["database_present"] for index in indexes),
                "sources": [source for index in indexes for source in index["sources"]],
                "database_count": sum(index["database_count"] for index in indexes),
                "database_limit": RUN_LIMIT * len(indexes), "channels": list(self.catalogs),
                "agent_id": self.agent, "audience_id": self.audience, "all_chats": self.all_chats,
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
                      agent_id=self.agent, audience_id=self.audience, all_chats=self.all_chats, channels=list(self.catalogs))
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
        self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.headers.get("Host") not in {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"}:
            self.send(403, {"error": "loopback Host required"})
            return
        target = urlsplit(self.path)
        try:
            if target.path == "/api/chat":
                chat = getattr(self.server, "chat_runtime", None)
                result = chat.snapshot() if chat else {"enabled": False, "notice": "Chat 未配置。", "turns": [], "active_id": None}
                catalogs = self.server.catalog.catalogs.items() if isinstance(self.server.catalog, MultiCatalog) else [("", self.server.catalog)]
                prefix = next((name + ":" if name else "" for name, source in catalogs
                               if chat and source.database.parent.resolve() == chat.state_home), None)
                for turn in result["turns"]:
                    turn["record_key"] = prefix + "db:" + turn["run_id"] if prefix is not None and turn["run_id"] else None
                result["mutation_token"] = getattr(self.server, "mutation_token", None)
                self.send(200, result)
            elif target.path == "/api/records":
                self.send(200, self.server.catalog.index())
            elif target.path == "/api/settings":
                store = getattr(self.server, "settings_store", None)
                result = store.index() if store else {"enabled": False, "files": []}
                result["mutation_token"] = getattr(self.server, "mutation_token", None) if store else None
                self.send(200, result)
            elif target.path == "/api/backup":
                root = getattr(self.server, "backup_root", None)
                result = {"enabled": False}
                if root is not None:
                    policy_path = root / "config" / "backup.json"
                    status_path = root / "backup-state" / "status.json"
                    if policy_path.exists():
                        policy = json.loads(policy_path.read_text())
                        if status_path.exists():
                            result = json.loads(status_path.read_text())
                        result.update(enabled=policy.get("enabled", False), destination=policy.get("destination"))
                self.send(200, result)
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
            elif target.path in {"/", "/index.html", "/logo.svg", "/markdown-parser.js", "/markdown.js", "/chat.js", "/shortcuts.js", "/settings-fields.js", "/settings-simple.css", "/workspace.js", "/workspace.css", "/theme.js", "/theme.css", "/pages.css", "/embedded.css", "/executions.html", "/app.js", "/style.css", "/memory.html", "/memory.js", "/memory.css", "/settings.html", "/settings.js", "/settings.css"}:
                name = "index.html" if target.path == "/" else target.path[1:]
                mime = {".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml"}[Path(name).suffix]
                self.send(200, (ASSETS / name).read_bytes(), mime + "; charset=utf-8")
            else:
                self.send(404, {"error": "route not found"})
        except KeyError as error:
            self.send(404, {"error": str(error)})
        except (OSError, ValueError, sqlite3.Error) as error:
            self.send(500, {"error": f"record read failed: {error}"})

    def do_POST(self):
        token = getattr(self.server, "mutation_token", None)
        route = urlsplit(self.path).path
        if route not in {"/api/memory-action", "/api/settings", "/api/chat", "/api/chat/stop", "/api/chat/new"} or not token or route == "/api/settings" and not getattr(self.server, "settings_store", None):
            self.send(405, {"error": "this route is read-only"})
            return
        host = self.headers.get("Host", "")
        if host not in {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"} or self.headers.get("Origin") != "http://" + host or not secrets.compare_digest(self.headers.get("X-Host-Action", "").encode(), token.encode()):
            self.send(403, {"error": "same-origin owner action required"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            limit = 1 << 20 if route == "/api/settings" else 128 * 1024 if route == "/api/chat" else 8192
            if not 0 < length <= limit or self.headers.get("Content-Type") != "application/json" or self.headers.get("Transfer-Encoding"):
                raise ValueError("provide a bounded JSON action")
            self.connection.settimeout(10)
            value = decode(self.rfile.read(length))
            if route.startswith("/api/chat"):
                chat = getattr(self.server, "chat_runtime", None)
                if chat is None:
                    raise ChatConflict("Chat 执行环境尚不可用。")
                if route == "/api/chat" and set(value) == {"request_id", "message"}:
                    turn_id = chat.start(value["request_id"], value["message"])
                    self.send(202, {"turn_id": turn_id})
                elif route == "/api/chat/stop" and set(value) == {"turn_id"} and isinstance(value["turn_id"], str):
                    chat.stop(value["turn_id"])
                    self.send(200, {"status": "stop_requested"})
                elif route == "/api/chat/new" and not value:
                    chat.reset()
                    self.send(200, {"status": "new_conversation"})
                else:
                    raise ValueError("无效的 Chat 请求。")
                return
            if route == "/api/settings":
                if set(value) != {"file", "revision", "value"} or not isinstance(value["file"], str) or not isinstance(value["revision"], str):
                    raise ValueError("Provide a settings file, expected revision and value.")
                self.send(200, self.server.settings_store.save(value["file"], value["revision"], value["value"]))
                return
            if set(value) != {"key", "action", "note", "revision"} or not all(isinstance(value[k], str) for k in ("key", "action", "note")) or type(value["revision"]) is not int or len(value["note"]) > 2000 or len(value["key"]) > 256 or value["action"] not in {"accept", "reject", "invalidate", "restructure"}:
                raise ValueError("invalid memory action")
            status, result = self.server.catalog.action(value["key"], value["action"], value["note"], value["revision"])
            self.send(status, result)
        except (Conflict, ChatConflict) as error:
            self.send(409, {"error": str(error)})
        except KeyError:
            self.send(404, {"error": "Settings file is outside this console." if route == "/api/settings" else "record outside the selected source or scope"})
        except (ValueError, sqlite3.Error) as error:
            self.send(400, {"error": str(error)})
        except (TypeError, RecursionError):
            self.send(400, {"error": "Invalid settings value or nesting depth."})
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
    parser.add_argument("--all-chats", action="store_true", help="Show every Agent and chat partition in this local console")
    parser.add_argument("--host-binary", type=Path, default=ROOT / "bin" / "agent-host", help="Host executable used to verify file memory and rebuild its derived index")
    parser.add_argument("--backup-root", type=Path, help="Explicit Host root for backup health and local settings")
    parser.add_argument("--chat-config", type=Path, help="Private CLI handoff config for Web Chat; defaults only to a destination within --backup-root")
    parser.add_argument("--port", type=int, default=8765, help="Loopback port; 0 chooses an available port")
    args = parser.parse_args()
    try:
        if args.channel_home:
            if args.workspace or args.project or not args.no_reports:
                parser.error("--channel-home requires --no-reports and no project/workspace filter")
            catalog = MultiCatalog(args.channel_home, args.agent, args.audience, args.host_binary, args.all_chats)
            catalog.index()
        else:
            catalog = Catalog(args.home, None if args.no_reports else args.reports, args.workspace, args.project, args.agent, args.audience, args.host_binary, args.all_chats)
            catalog.index()
    except (OSError, ValueError, sqlite3.Error) as error:
        parser.error(str(error))
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.catalog = catalog
    server.backup_root = args.backup_root
    server.settings_store = SettingsStore(args.backup_root, args.host_binary,
                                        Path.home() / ".config/agent-host/task-handoff.json") if args.backup_root else None
    server.mutation_token = secrets.token_urlsafe(32)
    server.chat_runtime = ChatRuntime(args.chat_config or default_config(args.backup_root))
    def stop_server(_signal, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, stop_server)
    print(f"Execution viewer: http://127.0.0.1:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.chat_runtime.close()
        server.server_close()


if __name__ == "__main__":
    main()
