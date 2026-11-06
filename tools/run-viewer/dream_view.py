"""Read the fixed, Host-published Dream HOT projection without granting access."""

import datetime
import hashlib
import json
import os
import sqlite3
import stat
from pathlib import Path


PARTITION_LIMIT = 200
ROW_LIMIT = 50
FILE_LIMIT = 8 << 20
DOCUMENT_LIMIT = 1 << 20
RECORD_LIMIT = 1 << 20
RECORD_TOTAL_LIMIT = 4 << 20


def _private_regular(path, limit):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1 or info.st_size > limit:
        raise ValueError(f"unsafe Dream file: {path.name}")
    with path.open("rb") as handle:
        body = handle.read(limit + 1)
    if len(body) > limit:
        raise ValueError(f"Dream file exceeds limit: {path.name}")
    return body


def _object(body, label):
    value = json.loads(body)
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be an object")
    return value


def _partition(value):
    if not isinstance(value, dict) or set(value) != {"PersonID", "AgentID", "AudienceID", "Scope"}:
        raise ValueError("invalid Dream partition identity")
    result = tuple(value[name] for name in ("PersonID", "AgentID", "AudienceID", "Scope"))
    if any(not isinstance(item, str) or not item or item.strip() != item for item in result):
        raise ValueError("invalid Dream partition identity")
    return result


def _catalogs(catalog):
    values = getattr(catalog, "catalogs", None)
    if isinstance(values, dict):
        return values
    return {"local": catalog}


class DreamReader:
    def __init__(self, root, catalog):
        self.root = Path(root) if root is not None else None
        self.catalogs = _catalogs(catalog)
        self.homes = {str(item.database.parent): (name, item) for name, item in self.catalogs.items()}

    def _root_state(self):
        if self.root is None:
            return "unconfigured", None
        if not self.root.is_absolute():
            return "unsafe", "Dream root must be absolute"
        try:
            info = self.root.lstat()
        except FileNotFoundError:
            return "missing", None
        except OSError as error:
            return "unsafe", str(error)
        try:
            canonical = self.root.resolve(strict=True)
        except (OSError, RuntimeError) as error:
            return "unsafe", f"Dream root cannot be resolved safely: {error}"
        if canonical != self.root or not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            return "unsafe", "Dream root must be canonical, owner-private and free of symlinks"
        return "ready", None

    def _entries(self):
        root_state, root_error = self._root_state()
        if root_state != "ready":
            return [], [root_error] if root_error else [], root_state
        rows, errors = [], []
        try:
            with os.scandir(self.root) as entries:
                for index, entry in enumerate(entries):
                    if index >= PARTITION_LIMIT:
                        return [], ["Dream partition limit exceeded"], "unsafe"
                    path = Path(entry.path)
                    if len(path.name) != 24 or any(char not in "0123456789abcdef" for char in path.name):
                        errors.append(f"Unexpected Dream root entry: {path.name}")
                        continue
                    try:
                        rows.append(self._metadata(path))
                    except (OSError, ValueError, json.JSONDecodeError) as error:
                        rows.append({"id": path.name, "error": str(error)})
        except OSError as error:
            return [], [f"Dream root read failed: {error}"], "unsafe"
        rows.sort(key=lambda row: row["id"])
        return rows, errors, "ready"

    def _metadata(self, path):
        info = path.lstat()
        if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or path.resolve() != path:
            raise ValueError("unsafe Dream partition directory")
        value = _object(_private_regular(path / "partition.json", 4 << 20), "partition metadata")
        if set(value) != {"version", "id", "partition", "source", "manifest_sha256"} or value.get("version") != "dream-current-v1" or value.get("id") != path.name:
            raise ValueError("invalid Dream partition metadata")
        partition = _partition(value["partition"])
        expected_id = hashlib.sha256("\0".join(partition).encode()).hexdigest()[:24]
        if expected_id != path.name:
            raise ValueError("Dream partition ID does not match its exact identity")
        source = value.get("source")
        if not isinstance(source, dict) or set(source) != {"state_home", "config_id"} or not isinstance(source.get("config_id"), str) or not source["config_id"]:
            raise ValueError("invalid Dream source metadata")
        mounted = self.homes.get(source.get("state_home"))
        if mounted is None:
            raise ValueError("Dream source is not mounted in this workbench")
        manifests = value.get("manifest_sha256")
        if not isinstance(manifests, dict) or any(not isinstance(key, str) or not isinstance(digest, str) or len(key) != 64 or len(digest) != 64 or any(char not in "0123456789abcdef" for char in key + digest) for key, digest in manifests.items()):
            raise ValueError("invalid Dream manifest index")
        return {"id": path.name, "partition": value["partition"], "source": {"name": mounted[0], "config_id": source["config_id"]}, "_path": path, "_metadata": value, "_catalog": mounted[1]}

    def partitions(self):
        rows, errors, root_state = self._entries()
        safe = [{key: value for key, value in row.items() if not key.startswith("_")} for row in rows]
        return {"enabled": root_state == "ready",
                "status": "unsafe" if root_state == "unsafe" else "ready" if safe else "empty",
                "partitions": safe, "errors": errors}

    def partition(self, identifier):
        if len(identifier) != 24 or any(char not in "0123456789abcdef" for char in identifier):
            raise KeyError("invalid Dream partition ID")
        rows, errors, root_state = self._entries()
        if root_state == "unsafe":
            return {"id": identifier, "status": "error", "error": "; ".join(errors) or "Dream root is unsafe", "root_errors": errors}
        row = next((item for item in rows if item["id"] == identifier), None)
        if row is None:
            raise KeyError("Dream partition not found")
        if row.get("error"):
            return {"id": identifier, "status": "error", "error": row["error"], "root_errors": errors}
        try:
            current = self._current(row)
            records = self._records(row)
            return {"id": identifier, "status": current["status"], "partition": row["partition"], "source": row["source"],
                    "current": current, "records": records, "root_errors": errors}
        except (OSError, ValueError, sqlite3.Error, json.JSONDecodeError) as error:
            return {"id": identifier, "status": "error", "partition": row["partition"], "source": row["source"], "error": str(error), "root_errors": errors}

    def _current(self, row):
        link = row["_path"] / "current"
        try:
            info = link.lstat()
        except FileNotFoundError:
            return {"status": "missing", "snapshot_valid": False, "documents": {}, "memories": []}
        if not stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1:
            raise ValueError("Dream current is not a symbolic link")
        target = os.readlink(link)
        if len(target) != len("versions/") + 64 or not target.startswith("versions/") or any(char not in "0123456789abcdef" for char in target[9:]):
            raise ValueError("unsafe Dream current link")
        digest = target[9:]
        expected = row["_metadata"]["manifest_sha256"].get(digest)
        if not isinstance(expected, str) or len(expected) != 64:
            raise ValueError("Dream current has no verified manifest record")
        base = row["_path"] / "versions" / digest
        if base.resolve() != base:
            raise ValueError("Dream current escapes its fixed partition root")
        for directory in (base.parent, base):
            info = directory.lstat()
            if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
                raise ValueError("unsafe Dream version directory")
        entries = {item.name for item in base.iterdir()}
        if entries != {"USER.md", "MEMORY.md", "TASKS.md", "manifest.json"}:
            raise ValueError("Dream current snapshot contains unexpected files")
        manifest_raw = _private_regular(base / "manifest.json", FILE_LIMIT)
        manifest_info = (base / "manifest.json").lstat()
        if hashlib.sha256(manifest_raw).hexdigest() != expected:
            raise ValueError("Dream manifest bytes changed")
        manifest = _object(manifest_raw, "Dream manifest")
        if manifest.get("version") != "dream-hot-v1" or manifest.get("digest") != digest or _partition(manifest.get("partition")) != _partition(row["partition"]):
            raise ValueError("Dream manifest identity mismatch")
        documents = manifest.get("documents")
        if not isinstance(documents, dict) or set(documents) != {"USER.md", "MEMORY.md", "TASKS.md"}:
            raise ValueError("Dream manifest documents are invalid")
        for name, text in documents.items():
            if not isinstance(text, str) or _private_regular(base / name, DOCUMENT_LIMIT) != text.encode():
                raise ValueError(f"Dream document bytes changed: {name}")
        expired = False
        now = datetime.datetime.now(datetime.timezone.utc)
        memories = manifest.get("memories") or []
        if not isinstance(memories, list):
            raise ValueError("Dream manifest memory versions are invalid")
        for memory in memories:
            expiry = memory.get("expires_at") if isinstance(memory, dict) else None
            if expiry:
                try:
                    expired = expired or datetime.datetime.fromisoformat(expiry.replace("Z", "+00:00")) <= now
                except (TypeError, ValueError):
                    raise ValueError("Dream manifest expiry is invalid")
        source_mtime = datetime.datetime.fromtimestamp(manifest_info.st_mtime, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
        return {"status": "expired" if expired else "current", "snapshot_valid": not expired, "digest": digest,
                "documents": documents, "memories": memories, "manifest_sha256": expected,
                "source_mtime": source_mtime, "source_mtime_kind": "filesystem_mtime_not_content_generation"}

    def _records(self, row):
        person, agent, audience, scope = _partition(row["partition"])
        conn = row["_catalog"].connection()
        try:
            total = 0
            def page(sql, args, ordering):
                nonlocal total
                values = []
                for size, payload in conn.execute(sql, (RECORD_LIMIT + 1, *args)):
                    if not isinstance(size, int) or size < 0 or size > RECORD_LIMIT:
                        raise ValueError("Dream record exceeds its per-record byte limit")
                    total += size
                    if total > RECORD_TOTAL_LIMIT:
                        raise ValueError("Dream records exceed the response byte limit")
                    values.append(_object(payload, "Dream record"))
                return {"items": values[:ROW_LIMIT], "limit": ROW_LIMIT, "has_more": len(values) > ROW_LIMIT,
                        "truncated": len(values) > ROW_LIMIT, "ordering": ordering}
            projection = "SELECT length(CAST(payload AS BLOB)), substr(CAST(payload AS BLOB),1,?) "
            cycles = page(projection + "FROM dream_cycles WHERE config_id=? ORDER BY started_at DESC,id DESC LIMIT ?", (row["_metadata"]["source"]["config_id"], ROW_LIMIT + 1), "recent")
            common = (person, agent, audience, scope)
            work = page(projection + "FROM dream_work_items WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? ORDER BY updated_at DESC,id LIMIT ?", common + (ROW_LIMIT + 1,), "recent")
            candidates = page(projection + "FROM dream_candidates WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? ORDER BY id LIMIT ?", common + (ROW_LIMIT + 1,), "bounded")
            cleanup = page(projection + "FROM dream_cleanup WHERE person_id=? AND agent_id=? AND audience_id=? AND scope=? ORDER BY id LIMIT ?", common + (ROW_LIMIT + 1,), "bounded")
            return {"cycles": cycles, "work": work, "candidates": candidates, "cleanup": cleanup}
        finally:
            conn.close()
