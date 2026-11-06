"""Read the fixed, Host-published Dream HOT projection without granting access."""

import datetime
import fcntl
import hashlib
import json
import os
import sqlite3
import stat
import time
from contextlib import contextmanager
from pathlib import Path


PARTITION_LIMIT = 200
ROW_LIMIT = 50
FILE_LIMIT = 8 << 20
DOCUMENT_LIMIT = 1 << 20
RECORD_LIMIT = 1 << 20
RECORD_TOTAL_LIMIT = 4 << 20
PROJECTION_SOURCE_LIMIT = 8
PROJECTION_MEMORY_LIMIT = 1600
COORDINATION_FILE_LIMIT = 512 << 10
COORDINATION_EVENT_LIMIT = 4096
COORDINATION_LEDGER_LIMIT = 32 << 20
CLEANUP_INTENT_LIMIT = 512
SHARED_KINDS = {"preference", "project_fact", "active_task"}
SAFE_ID_CHARACTERS = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"


def _private_regular(path, limit):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1 or info.st_size > limit:
        raise ValueError(f"unsafe Dream file: {path.name}")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        opened = os.fstat(descriptor)
        if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid() or opened.st_mode & 0o077
                or opened.st_nlink != 1 or (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino)):
            raise ValueError(f"unsafe Dream file: {path.name}")
        with os.fdopen(descriptor, "rb", closefd=False) as handle:
            body = handle.read(limit + 1)
    finally:
        os.close(descriptor)
    if len(body) > limit:
        raise ValueError(f"Dream file exceeds limit: {path.name}")
    return body


def _object(body, label):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError(f"{label} contains duplicate fields")
            value[key] = item
        return value
    value = json.loads(body, object_pairs_hook=unique)
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be an object")
    return value


def _private_directory(path, label):
    info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.getuid()
            or info.st_mode & 0o077 or path.resolve(strict=True) != path):
        raise ValueError(f"{label} is unsafe")
    return path


def _bounded_names(path, limit, label):
    names = []
    with os.scandir(path) as entries:
        for entry in entries:
            names.append(entry.name)
            if len(names) > limit:
                raise ValueError(f"{label} limit exceeded")
    names.sort()
    return names


def _go_json(value):
    encoded = json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    return (encoded.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
            .replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")).encode()


def _partition(value):
    if not isinstance(value, dict) or set(value) != {"PersonID", "AgentID", "AudienceID", "Scope"}:
        raise ValueError("invalid Dream partition identity")
    result = tuple(value[name] for name in ("PersonID", "AgentID", "AudienceID", "Scope"))
    if any(not isinstance(item, str) or not item or len(item) > 512 or item.strip() != item for item in result):
        raise ValueError("invalid Dream partition identity")
    return result


def _safe_id(value):
    return (isinstance(value, str) and 1 <= len(value) <= 64 and value[0] in SAFE_ID_CHARACTERS[:-2]
            and all(character in SAFE_ID_CHARACTERS for character in value))


def _digest(value):
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _projection(value, memories):
    required = {"canonical_person_id", "mount_id", "generation", "policy_digest", "binding_digest", "sources", "memories"}
    if not isinstance(value, dict) or set(value) != required or not _safe_id(value["canonical_person_id"]) or not _safe_id(value["mount_id"]):
        raise ValueError("invalid Dream shared projection identity")
    if type(value["generation"]) is not int or value["generation"] < 1 or not _digest(value["policy_digest"]) or not _digest(value["binding_digest"]):
        raise ValueError("invalid Dream shared projection authorization")
    sources = value["sources"]
    references = value["memories"]
    if not isinstance(sources, list) or not 1 <= len(sources) <= PROJECTION_SOURCE_LIMIT or not isinstance(references, list) or len(references) > PROJECTION_MEMORY_LIMIT:
        raise ValueError("invalid Dream shared projection limits")
    source_ids = set()
    previous_source = ""
    for source in sources:
        if not isinstance(source, dict) or not {"id", "state_home", "partition", "kinds"}.issubset(source) or not set(source) <= {"id", "state_home", "partition", "account", "local_owner", "kinds"}:
            raise ValueError("invalid Dream shared source")
        identifier, home, kinds = source["id"], source["state_home"], source["kinds"]
        local_owner, account = source.get("local_owner", False), source.get("account")
        if not _safe_id(identifier) or identifier in source_ids or identifier <= previous_source or not isinstance(home, str) or len(home) > 4096 or not os.path.isabs(home) or os.path.normpath(home) != home:
            raise ValueError("invalid Dream shared source identity")
        if type(local_owner) is not bool or (account is None) == (not local_owner):
            raise ValueError("invalid Dream shared source admission")
        if account is not None:
            if not isinstance(account, dict) or set(account) != {"channel", "account_id", "tenant_id", "sender_id"} or account["channel"] not in {"dingtalk", "feishu"}:
                raise ValueError("invalid Dream shared source account")
            if any(not isinstance(account[name], str) or not account[name] or len(account[name]) > 512 or account[name].strip() != account[name] for name in ("account_id", "tenant_id", "sender_id")):
                raise ValueError("invalid Dream shared source account")
        if not isinstance(kinds, list) or not kinds or kinds != sorted(kinds) or len(kinds) > len(SHARED_KINDS) or len(set(kinds)) != len(kinds) or any(kind not in SHARED_KINDS for kind in kinds):
            raise ValueError("invalid Dream shared source kinds")
        _partition(source["partition"])
        source_ids.add(identifier)
        previous_source = identifier
    expected, expected_order = set(), []
    if len(memories) > PROJECTION_MEMORY_LIMIT:
        raise ValueError("invalid Dream shared memory reference limit")
    for memory in memories:
        if not isinstance(memory, dict) or not isinstance(memory.get("id"), str) or ":" not in memory["id"]:
            raise ValueError("invalid Dream shared manifest memory")
        source_id, memory_id = memory["id"].split(":", 1)
        item = (source_id, memory_id, memory.get("revision"), memory.get("kind"))
        if item in expected:
            raise ValueError("duplicate Dream shared manifest memory")
        expected.add(item)
        expected_order.append(item)
    observed, observed_order = set(), []
    for reference in references:
        if not isinstance(reference, dict) or set(reference) != {"source_id", "memory_id", "revision", "kind"}:
            raise ValueError("invalid Dream shared memory reference")
        item = (reference["source_id"], reference["memory_id"], reference["revision"], reference["kind"])
        if reference["source_id"] not in source_ids or not isinstance(reference["memory_id"], str) or not reference["memory_id"] or type(reference["revision"]) is not int or reference["revision"] < 1 or reference["kind"] not in SHARED_KINDS or item in observed:
            raise ValueError("invalid Dream shared memory reference")
        observed.add(item)
        observed_order.append(item)
    if observed != expected or observed_order != expected_order or observed_order != sorted(observed_order):
        raise ValueError("Dream shared memory references do not match the manifest")
    return value


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
                        with self._publish_lock(path):
                            rows.append(self._metadata(path))
                    except (OSError, ValueError, json.JSONDecodeError) as error:
                        rows.append({"id": path.name, "error": str(error)})
        except OSError as error:
            return [], [f"Dream root read failed: {error}"], "unsafe"
        rows.sort(key=lambda row: row["id"])
        return rows, errors, "ready"

    @contextmanager
    def _publish_lock(self, path):
        lock = path / ".publish.lock"
        descriptor = os.open(lock, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        try:
            before = lock.lstat()
            opened = os.fstat(descriptor)
            if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid() or opened.st_mode & 0o077
                    or opened.st_nlink != 1 or stat.S_ISLNK(before.st_mode)
                    or (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino)):
                raise ValueError("Dream publication lock is unsafe")
            deadline = time.monotonic() + 0.5
            while True:
                try:
                    fcntl.flock(descriptor, fcntl.LOCK_SH | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise ValueError("Dream publication lock wait exceeded 0.5s")
                    time.sleep(0.01)
            after = lock.lstat()
            if (after.st_dev, after.st_ino) != (opened.st_dev, opened.st_ino):
                raise ValueError("Dream publication lock changed while opening")
            yield
        finally:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            finally:
                os.close(descriptor)

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
            # Re-read metadata after acquiring SH; the list row is discovery
            # only and must never be paired with a newer current link.
            with self._publish_lock(row["_path"]):
                row = self._metadata(row["_path"])
                current = self._current(row)
            authority = self._authorization(row)
            unified_cleanup = self._cleanup_intents(row)
        except (OSError, ValueError, sqlite3.Error, json.JSONDecodeError) as error:
            return {"id": identifier, "status": "error", "partition": row["partition"], "source": row["source"], "error": str(error), "root_errors": errors}
        try:
            records, records_error = self._records(row), None
        except (OSError, ValueError, sqlite3.Error, json.JSONDecodeError) as error:
            records, records_error = {}, str(error)
        display_errors = errors + ([f"business records unavailable: {records_error}"] if records_error else [])
        return {"id": identifier, "status": current["status"], "partition": row["partition"], "source": row["source"],
                "current": current, "authorization": authority, "current_cleanup": unified_cleanup,
                "records": records, "records_error": records_error, "root_errors": display_errors}

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
        entries = set(_bounded_names(base, 4, "Dream current file"))
        if entries != {"USER.md", "MEMORY.md", "TASKS.md", "manifest.json"}:
            raise ValueError("Dream current snapshot contains unexpected files")
        manifest_raw = _private_regular(base / "manifest.json", FILE_LIMIT)
        manifest_info = (base / "manifest.json").lstat()
        if hashlib.sha256(manifest_raw).hexdigest() != expected:
            raise ValueError("Dream manifest bytes changed")
        manifest = _object(manifest_raw, "Dream manifest")
        version = manifest.get("version")
        if version not in {"dream-hot-v1", "dream-hot-v2"} or manifest.get("digest") != digest or _partition(manifest.get("partition")) != _partition(row["partition"]):
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
        projection = manifest.get("projection")
        if version == "dream-hot-v1" and projection is not None:
            raise ValueError("Dream v1 manifest cannot contain a shared projection")
        if version == "dream-hot-v2":
            projection = _projection(projection, memories)
        authority = manifest.get("coordination_authority")
        if authority is not None and (version != "dream-hot-v2" or not isinstance(authority, dict)
                                      or set(authority) != {"id", "revision"} or not _safe_id(authority["id"])
                                      or type(authority["revision"]) is not int or authority["revision"] < 1):
            raise ValueError("Dream coordination authority is invalid")
        source_mtime = datetime.datetime.fromtimestamp(manifest_info.st_mtime, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
        return {"status": "expired" if expired else "current", "snapshot_valid": not expired, "digest": digest,
                "documents": documents, "memories": memories, "manifest_sha256": expected,
                "source_mtime": source_mtime, "source_mtime_kind": "filesystem_mtime_not_content_generation",
                "projection": projection, "authority": authority}

    def _coordination_root(self):
        root = self.root.parent / "coordination"
        return _private_directory(root, "Dream coordination root")

    def _authorization(self, row):
        anchor_path = Path(row["_metadata"]["source"]["state_home"]) / ".dream-coordination" / "targets" / f"{row['id']}.json"
        try:
            _private_directory(anchor_path.parent.parent, "Dream coordination anchor root")
            _private_directory(anchor_path.parent, "Dream coordination anchor directory")
            anchor = _object(_private_regular(anchor_path, COORDINATION_FILE_LIMIT), "Dream coordination anchor")
        except FileNotFoundError:
            return {"status": "unavailable", "error": "no shared authorization anchor"}
        except (OSError, ValueError, json.JSONDecodeError, KeyError) as error:
            return {"status": "unknown", "needs_recovery": True, "error": str(error)}
        try:
            root = self._coordination_root()
            required = {"version", "target_key", "coordination_root", "state_home", "config_id", "config_path", "hot_root", "policy_path", "policy_key", "partition"}
            if (set(anchor) != required or anchor["version"] != "dream-coordination-v1" or anchor["target_key"] != row["id"]
                    or anchor["coordination_root"] != str(root) or anchor["hot_root"] != str(self.root)
                    or anchor["state_home"] != row["_metadata"]["source"]["state_home"]
                    or anchor["config_id"] != row["_metadata"]["source"]["config_id"]
                    or _partition(anchor["partition"]) != _partition(row["partition"])):
                raise ValueError("Dream coordination anchor identity mismatch")
            if any(not isinstance(anchor[name], str) or not os.path.isabs(anchor[name]) or os.path.normpath(anchor[name]) != anchor[name]
                   for name in ("coordination_root", "state_home", "config_path", "hot_root", "policy_path")):
                raise ValueError("Dream coordination anchor path is invalid")
            policy_key = hashlib.sha256(anchor["policy_path"].encode()).hexdigest()
            if anchor["policy_key"] != policy_key:
                raise ValueError("Dream coordination policy key mismatch")
            policy_root, target_root = root / "policies" / policy_key, root / "targets" / row["id"]
            stable = None
            for _ in range(2):
                policy, target = self._ledger(policy_root, "policy", anchor), self._ledger(target_root, "target", anchor)
                policy_check, target_check = self._ledger(policy_root, "policy", anchor), self._ledger(target_root, "target", anchor)
                if policy["token"] == policy_check["token"] and target["token"] == target_check["token"]:
                    stable = policy_check, target_check
                    break
            if stable is None:
                raise ValueError("Dream coordination ledgers changed during read")
            policy, target = stable
            policy_value, value = policy["state"], target["state"]
            status, reason = value["state"], value.get("reason", "")
            if policy_value["state"] == "invalid":
                status, reason = "invalid", policy_value.get("reason", "")
            elif value["highest_generation"] < policy_value["highest_generation"]:
                status, reason = "invalid", "target has not observed the current policy generation"
            return {"status": status, "reason": reason,
                    "highest_generation": max(value["highest_generation"], policy_value["highest_generation"]),
                    "authorization_revision": value["authorization_revision"],
                    "last_committed_hot_digest": value.get("last_committed_hot_digest", ""),
                    "needs_recovery": policy["needs_recovery"] or target["needs_recovery"]}
        except (OSError, ValueError, json.JSONDecodeError, KeyError) as error:
            return {"status": "unknown", "needs_recovery": True, "error": str(error)}

    def _ledger(self, root, kind, anchor):
        _private_directory(root, "Dream coordination ledger")
        events_path = root / "events"
        _private_directory(events_path, "Dream coordination event directory")
        names = _bounded_names(events_path, COORDINATION_EVENT_LIMIT, "Dream coordination event")
        if not names or len(names) > COORDINATION_EVENT_LIMIT:
            raise ValueError("Dream coordination event count is invalid")
        previous = "0" * 64
        states = []
        total_bytes = 0
        previous_generation, previous_revision = 0, 0
        for index, name in enumerate(names, 1):
            if name != f"{index:020d}.json":
                raise ValueError("Dream coordination event sequence is invalid")
            event_raw = _private_regular(events_path / name, COORDINATION_FILE_LIMIT)
            total_bytes += len(event_raw)
            if total_bytes > COORDINATION_LEDGER_LIMIT:
                raise ValueError("Dream coordination ledger byte limit exceeded")
            event = _object(event_raw, "Dream coordination event")
            state_value = event.get("state")
            state_raw = _go_json(state_value)
            state_hash = hashlib.sha256(state_raw).hexdigest()
            if (set(event) != {"version", "sequence", "previous_state_hash", "state_hash", "actor", "reason", "at", "state"}
                    or event.get("version") != "dream-coordination-v1" or event.get("sequence") != index
                    or event.get("previous_state_hash") != previous or event.get("state_hash") != state_hash):
                raise ValueError("Dream coordination event hash chain is invalid")
            if not isinstance(event.get("actor"), str) or not event["actor"] or not isinstance(event.get("reason"), str) or not event["reason"] or not isinstance(event.get("at"), str):
                raise ValueError("Dream coordination event metadata is invalid")
            self._ledger_identity(kind, state_value, anchor)
            generation = state_value["highest_generation"]
            revision = state_value.get("authorization_revision", 0)
            if generation < previous_generation or revision < previous_revision:
                raise ValueError("Dream coordination state history rolled back")
            previous_generation, previous_revision = generation, revision
            states.append((state_hash, state_value)); previous = state_hash
        try:
            head_raw = _private_regular(root / "head.json", COORDINATION_FILE_LIMIT)
        except FileNotFoundError:
            return {"state": states[-1][1], "needs_recovery": True,
                    "token": (len(states), states[-1][0], "missing")}
        total_bytes += len(head_raw)
        if total_bytes > COORDINATION_LEDGER_LIMIT:
            raise ValueError("Dream coordination ledger byte limit exceeded")
        head = _object(head_raw, "Dream coordination head")
        sequence = head.get("sequence")
        if (set(head) != {"version", "sequence", "state_hash", "state"} or head.get("version") != "dream-coordination-v1" or type(sequence) is not int or not 1 <= sequence <= len(states)
                or head.get("state_hash") != states[sequence - 1][0] or head.get("state") != states[sequence - 1][1]):
            raise ValueError("Dream coordination head does not match its event")
        return {"state": states[-1][1], "needs_recovery": sequence != len(states),
                "token": (len(states), states[-1][0], sequence, head.get("state_hash"))}

    def _ledger_identity(self, kind, value, anchor):
        if not isinstance(value, dict) or value.get("version") != "dream-coordination-v1":
            raise ValueError("Dream coordination state is invalid")
        if kind == "policy":
            allowed = {"version", "policy_key", "policy_path", "highest_generation", "policy_digest", "state", "reason"}
            required = allowed - {"reason"}
            if (not required.issubset(value) or not set(value) <= allowed or value.get("policy_key") != anchor["policy_key"]
                    or value.get("policy_path") != anchor["policy_path"] or type(value.get("highest_generation")) is not int
                    or value["highest_generation"] < 1 or not _digest(value.get("policy_digest"))
                    or value.get("state") not in {"active", "invalid"}
                    or (value["state"] == "active" and value.get("reason", "") != "")
                    or (value["state"] == "invalid" and not isinstance(value.get("reason"), str))
                    or (value["state"] == "invalid" and not value.get("reason"))):
                raise ValueError("Dream policy ledger identity mismatch")
        else:
            allowed = {"version", "target_key", "state_home", "config_id", "config_path", "hot_root", "policy_path",
                       "policy_key", "partition", "mount_id", "canonical_person_id", "local_target", "highest_generation",
                       "policy_digest", "binding_digest", "state", "reason", "authorization_revision", "last_committed_hot_digest"}
            required = allowed - {"local_target", "reason", "last_committed_hot_digest"}
            if (not required.issubset(value) or not set(value) <= allowed or value.get("target_key") != anchor["target_key"]
                    or value.get("state_home") != anchor["state_home"] or value.get("config_id") != anchor["config_id"]
                    or value.get("config_path") != anchor["config_path"] or value.get("hot_root") != anchor["hot_root"]
                    or value.get("policy_path") != anchor["policy_path"] or value.get("policy_key") != anchor["policy_key"]
                    or _partition(value.get("partition")) != _partition(anchor["partition"])
                    or not _safe_id(value.get("mount_id")) or not _safe_id(value.get("canonical_person_id"))
                    or type(value.get("local_target", False)) is not bool or type(value.get("highest_generation")) is not int
                    or value["highest_generation"] < 1 or not _digest(value.get("policy_digest"))
                    or not _digest(value.get("binding_digest")) or value.get("state") not in {"active", "invalid"}
                    or type(value.get("authorization_revision")) is not int or value["authorization_revision"] < 1
                    or (value.get("last_committed_hot_digest", "") != "" and not _digest(value["last_committed_hot_digest"]))
                    or (value["state"] == "active" and value.get("reason", "") != "")
                    or (value["state"] == "invalid" and (not isinstance(value.get("reason"), str) or not value.get("reason")))):
                raise ValueError("Dream target ledger identity mismatch")

    def _cleanup_intents(self, row):
        try:
            root = self._coordination_root() / "cleanup" / row["id"]
            _private_directory(root.parent, "Dream cleanup root")
            _private_directory(root, "Dream cleanup intent root")
            names = _bounded_names(root, CLEANUP_INTENT_LIMIT, "Dream cleanup intent")
            items = []
            for name in names:
                if not name.endswith(".json"):
                    raise ValueError("unexpected Dream cleanup intent entry")
                item = _object(_private_regular(root / name, COORDINATION_FILE_LIMIT), "Dream cleanup intent")
                if (item.get("version") != "dream-current-cleanup-v1" or item.get("target_key") != row["id"]
                        or item.get("state_home") != row["_metadata"]["source"]["state_home"]
                        or item.get("config_id") != row["_metadata"]["source"]["config_id"]
                        or item.get("hot_root") != str(self.root) or _partition(item.get("partition")) != _partition(row["partition"])
                        or item.get("status") not in {"planned", "quarantined", "completed", "skipped"}):
                    raise ValueError("Dream cleanup intent identity mismatch")
                items.append({key: item.get(key) for key in ("id", "digest", "status", "reason", "planned_at", "quarantined_at", "completed_at")})
            return {"status": "ready", "items": items}
        except FileNotFoundError:
            return {"status": "empty", "items": []}
        except (OSError, ValueError, json.JSONDecodeError, KeyError) as error:
            return {"status": "error", "error": str(error), "items": []}

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
