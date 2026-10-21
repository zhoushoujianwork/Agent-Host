"""Owner-local configuration editing with secret retention and checked saves."""

import fcntl
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import tempfile
import threading

KEEP_SECRET = "__AGENT_HOST_KEEP_SECRET__"
MAX_BYTES = 1 << 20


class Conflict(ValueError):
    pass


def sensitive(key):
    key = key.lower()
    return not key.endswith(("_file", "_path")) and bool(re.search(r"secret|password|(?:^|_)token$|access_token|refresh_token|auth_token|bearer|api[_-]?key|authorization|^key$", key))


def private_bytes(path, max_bytes=MAX_BYTES):
    info = path.lstat()
    if path.resolve() != path or not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("Settings require an owner-private regular file without symlinks.")
    with path.open("rb") as stream:
        opened = os.fstat(stream.fileno())
        if not os.path.samestat(info, opened):
            raise Conflict("The settings file changed while opening; refresh first.")
        data = stream.read(max_bytes + 1)
    if len(data) > max_bytes:
        raise ValueError("The settings file exceeds the size limit.")
    return data


def digest(data):
    return hashlib.sha256(data).hexdigest()


def redact(value, secret=False, token=None):
    if secret:
        return token(value) if value else ""
    if isinstance(value, dict):
        return {k: redact(v, sensitive(k), token) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v, token=token) for v in value]
    return value


def restore(value, previous, secret=False, tokens=None):
    if isinstance(value, str) and value.startswith(KEEP_SECRET):
        if not secret or value not in tokens:
            raise ValueError("A retained secret marker is outside a secret field.")
        return tokens[value]
    if isinstance(value, dict):
        previous = previous if isinstance(previous, dict) else {}
        return {k: restore(v, previous.get(k), sensitive(k), tokens) for k, v in value.items()}
    if isinstance(value, list):
        previous = previous if isinstance(previous, list) else []
        return [restore(v, previous[i] if i < len(previous) else None, tokens=tokens) for i, v in enumerate(value)]
    return value


def atomic_write(path, data):
    fd, temporary = tempfile.mkstemp(prefix=".settings-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class SettingsStore:
    def __init__(self, root, binary, handoff=None, validator=None, web_config=None):
        self.root = Path(root).resolve()
        self.config = self.root / "config"
        self.binary = Path(binary)
        self.handoff = Path(handoff) if handoff else None
        self.validator = validator or self.check_service
        self.web_config = Path(web_config) if web_config else None
        self.lock = threading.Lock()
        self.secret_key = secrets.token_bytes(32)

    def masked(self, value, secret):
        tokens = {}

        def token(content):
            encoded = json.dumps(content, sort_keys=True).encode()
            marker = KEEP_SECRET + hmac.new(self.secret_key, encoded, "sha256").hexdigest()
            tokens[marker] = content
            return marker

        return redact(value, secret, token), tokens

    def files(self):
        files = {}

        def add(path, category, title=None, secret=False):
            if path.is_file() or path.is_symlink():
                files[digest(str(path).encode())[:20]] = (path, category, title or path.name, secret)

        for pattern, category in (("host.json", "models"), ("agents*.json", "agents"),
                                  ("channels.*.json", "channels"), ("datasource.plugins.*.json", "sources"),
                                  ("users.json", "people"), ("backup.json", "backup")):
            for path in sorted(self.config.glob(pattern)):
                titles = {"host.json": "Execution profiles", "agents.json": "Default agents",
                          "agents.cli.json": "CLI task agents", "agents.feishu.json": "Feishu agents",
                          "channels.dingtalk.json": "DingTalk", "channels.feishu.json": "Feishu",
                          "users.json": "User identities", "backup.json": "Backup policy"}
                title = titles.get(path.name, path.stem.removeprefix("datasource.plugins.").replace("-", " ").title())
                add(path, category, title)
        for name in ("AGENTS.md", "SOUL.md", "IDENTITY.md", "USER.md"):
            for path in sorted(self.config.glob("agents/*/" + name)):
                add(path, "instructions", path.parent.name + " · " + name)
        # Only existing credential files explicitly referenced by known settings.
        def references(value):
            if isinstance(value, dict):
                for key, child in value.items():
                    if key.endswith("_file") and isinstance(child, str) and child:
                        path = Path(child)
                        path = path if path.is_absolute() else self.config / path
                        if path.is_relative_to(self.config / "credentials"):
                            add(path, "providers", secret=key != "environment_file")
                    references(child)
            elif isinstance(value, list):
                for child in value:
                    references(child)
        for path, _, _, _ in list(files.values()):
            try:
                references(json.loads(private_bytes(path)))
            except (OSError, ValueError):
                continue
        if self.handoff and self.handoff.exists():
            add(self.handoff, "handoff", "CLI task handoff")
        return files

    def read(self, entry):
        path, category, title, secret = entry
        data = private_bytes(path)
        structured = path.suffix == ".json" and not secret
        value = json.loads(data) if structured else data.decode()
        return {"category": category, "title": title, "path": str(path),
                "format": "json" if structured else "text", "secret": secret,
                "application": "cli" if path == self.handoff or path.name == "agents.cli.json" else "service",
                "revision": digest(data), "value": self.masked(value, secret)[0]}

    def index(self):
        rows = []
        for key, entry in self.files().items():
            try:
                row = self.read(entry)
            except (OSError, ValueError):
                row = {"category": entry[1], "title": entry[2], "path": str(entry[0]),
                       "error": "Cannot read this private configuration. Check permissions and JSON syntax."}
            rows.append({"id": key, **row})
        return {"enabled": True, "root": str(self.root), "files": rows,
                "notice": "Saved configuration. Service changes require a restart; CLI changes apply on the next task. Provider sign-in and connectivity are not tested."}

    def agent_options(self):
        """Only expose capability names from the actual Web launch destination."""
        if not self.web_config:
            return None
        from chat_runtime import agent_destination
        destination = agent_destination(self.web_config)
        config = json.loads(private_bytes(self.web_config))
        args = config["args"]
        def option(flag):
            if args.count(flag) != 1 or args.index(flag) + 1 >= len(args):
                raise ValueError("Web capability configuration is unavailable.")
            return args[args.index(flag) + 1]
        profile = Path(option("--native-profile"))
        if not profile.is_absolute():
            raise ValueError("Web native profile must be absolute.")
        skills = []
        directory = profile / "skills"
        if directory.exists():
            for path in sorted(directory.iterdir()):
                if path.is_dir() and (path / "SKILL.md").is_file():
                    skills.append({"id": path.name, "name": path.name})
        sources = []
        for i, arg in enumerate(args):
            if arg != "--knowledge-config":
                continue
            if i + 1 >= len(args):
                raise ValueError("Web knowledge configuration is incomplete.")
            path = Path(args[i + 1])
            value = json.loads(private_bytes(path))
            plugin = re.fullmatch(r"datasource\.plugins\.([a-z0-9-]+)\.json", path.name)
            if plugin and not value.get("enabled", False):
                continue
            for source in value.get("settings", value).get("sources", []):
                name = source["name"]
                identity = plugin[1] + ":" + name if plugin else name
                if any(s["id"] == identity for s in sources):
                    raise ValueError("Duplicate Web knowledge source identity.")
                sources.append({"id": identity, "name": identity})
        key = next((key for key, entry in self.files().items() if str(entry[0]) == destination["agents_path"]), None)
        return {"file_id": key, "skills": skills, "knowledge_sources": sources}

    def check_service(self):
        if list(self.config.glob("channels.*.json")):
            result = subprocess.run([str(self.binary), "service", "check", "--config-dir", str(self.config)],
                                    capture_output=True, timeout=45, check=False)
            if result.returncode:
                raise ValueError("Host validation failed. The previous file was restored. Check required fields, model/profile references and file paths.")
        if self.handoff and self.handoff.exists():
            self.validate(self.handoff, json.loads(private_bytes(self.handoff)))

    def validate(self, path, value, plain=False):
        if path.suffix != ".json" or plain:
            if not isinstance(value, str) or "\x00" in value:
                raise ValueError("Provide text without NUL characters.")
            return
        if not isinstance(value, dict):
            raise ValueError("Configuration must be a JSON object.")
        if path.name.startswith("agents"):
            agents = value.get("agents")
            if not isinstance(agents, list) or not 0 < len(agents) <= 128 or not all(isinstance(a, dict) for a in agents):
                raise ValueError("Configure at least one Agent.")
            ids = [a.get("id", "") for a in agents]
            if not all(isinstance(i, str) and re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}", i) for i in ids) or len(set(ids)) != len(ids) or value.get("default_agent") not in ids:
                raise ValueError("Agent IDs must be unique; the default Agent must exist.")
            capability_options = self.agent_options() if self.web_config else None
            web_key = capability_options.get("file_id") if capability_options else None
            is_web = web_key and self.files()[web_key][0] == path
            for agent in agents:
                if not isinstance(agent.get("name"), str) or not agent["name"].strip() or len(agent["name"]) > 200 or any(c in agent["name"] for c in "\x00\r\n"):
                    raise ValueError("Every Agent needs a name.")
                if agent.get("workspace") and (not isinstance(agent["workspace"], str) or not Path(agent["workspace"]).is_absolute() or not Path(agent["workspace"]).is_dir()):
                    raise ValueError("The Agent workspace must be an existing absolute directory.")
                for field, choices in (("native_skills", "skills"), ("knowledge_sources", "knowledge_sources")):
                    selected = agent.get(field)
                    if selected is None:
                        continue
                    if not isinstance(selected, list) or not all(isinstance(s, str) and s and s.strip() == s and not any(c in s for c in "\x00\r\n") for s in selected) or len(set(selected)) != len(selected):
                        raise ValueError("Agent capabilities must contain distinct installed identifiers.")
                    if is_web and not set(selected) <= {c["id"] for c in capability_options[choices]}:
                        raise ValueError("Selected Agent capability is unavailable in this Web connection.")
        if path == self.handoff:
            # Reuse the launcher's validation without starting a native task.
            script = next((directory / "scripts/chat.py" for directory in
                           (self.root / "skills/agent-host-tasks", ROOT_SKILL,
                            Path.home() / ".agents/skills/agent-host-tasks",
                            Path.home() / ".codex/skills/agent-host-tasks")
                           if (directory / "scripts/chat.py").is_file()), None)
            if script is None:
                raise ValueError("CLI handoff validator is unavailable in this installation.")
            # Run in a separate interpreter so sibling imports resolve without
            # mutating the threaded server's process-wide import path.
            result = subprocess.run([sys.executable, "-B", str(script), "--config", str(path), "--check"],
                                    capture_output=True, timeout=45, check=False)
            if result.returncode:
                raise ValueError("CLI handoff validation failed. Check the private configuration and referenced files.")

    def save(self, key, revision, value):
        with self.lock:
            entry = self.files().get(key)
            if not entry:
                raise KeyError("Settings file is outside this console.")
            path, _, _, secret = entry
            lock_path = self.config / ".settings.lock"
            if lock_path.is_symlink():
                raise ValueError("Configuration lock must not be a symlink.")
            fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, "r+b") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                before = private_bytes(path)
                if digest(before) != revision:
                    raise Conflict("Settings changed in another editor. Refresh before saving.")
                structured = path.suffix == ".json" and not secret
                previous = json.loads(before) if structured else before.decode()
                merged = restore(value, previous, secret, self.masked(previous, secret)[1])
                if path != self.handoff:
                    self.validate(path, merged, secret)
                after = (json.dumps(merged, ensure_ascii=False, indent=2) + "\n").encode() if structured else merged.encode()
                if len(after) > MAX_BYTES:
                    raise ValueError("Configuration exceeds its size limit.")
                history = self.config / ".settings-history"
                history.mkdir(mode=0o700, exist_ok=True)
                if history.is_symlink() or history.stat().st_mode & 0o077:
                    raise ValueError("Configuration history must be private without symlinks.")
                atomic_write(history / (key + "-" + revision + ".bak"), before)
                atomic_write(path, after)
                try:
                    self.validate(path, merged, secret)
                    self.validator()
                except Exception:
                    # A checker crash must roll back just like an invalid value.
                    if private_bytes(path) == after:
                        atomic_write(path, before)
                    else:
                        raise Conflict("Settings changed during validation. Inspect the file before retrying.")
                    raise ValueError("Validation failed; the previous file was restored. Check required fields, references and paths.") from None
                if private_bytes(path) != after:
                    raise Conflict("Settings changed during validation. Refresh to inspect the saved version.")
                return {"file": {"id": key, **self.read(entry)}, "status": "saved",
                        "message": "Saved and checked. CLI settings apply on the next task; service settings require a restart."}


ROOT_SKILL = Path(__file__).resolve().parents[2] / "skills/agent-host-tasks"
