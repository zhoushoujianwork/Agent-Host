"""Read-only views of explicitly configured, integrity-checked Skill catalogs."""

import json
from pathlib import Path
import re
import subprocess

from settings_store import private_bytes, redact


def preview(value):
    """Apply settings-field masking and common credential-text redaction."""
    if isinstance(value, dict):
        return {k: preview(v) for k, v in redact(value, token=lambda _: "[redacted]").items()}
    if isinstance(value, list):
        return [preview(v) for v in value]
    if not isinstance(value, str):
        return value
    value = re.sub(r"-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----", "[redacted private key]", value, flags=re.S)
    value = re.sub(r"(?i)(\b(?:[\w-]*(?:token|password|secret|api[_-]?key)|authorization)\b[\"']?\s*[:=]\s*)([^\r\n,}]+)", r"\1[redacted]", value)
    value = re.sub(r"(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*", "Bearer [redacted]", value)
    return re.sub(r"(https?://)[^/@\s]+:[^/@\s]+@", r"\1[redacted]@", value)


def absolute_path(value):
    if not isinstance(value, str) or not value or not Path(value).is_absolute() or any(c in value for c in "\x00\r\n"):
        raise ValueError("Skill destination requires an absolute configured path.")
    return Path(value)


def cli_root(path):
    config = json.loads(private_bytes(Path(path)))
    args = config.get("args")
    if not isinstance(args, list) or not all(isinstance(a, str) for a in args):
        raise ValueError("Invalid configured CLI destination.")

    def option(flag):
        values = [args[i + 1] for i, a in enumerate(args) if a == flag and i + 1 < len(args)]
        values += [a.split("=", 1)[1] for a in args if a.startswith(flag + "=")]
        if len(values) > 1 or args and args[-1] == flag:
            raise ValueError("Ambiguous configured CLI destination.")
        return values[0] if values else None

    if option("--skills-root"):
        return absolute_path(option("--skills-root"))
    home = absolute_path(option("--home"))
    marker = home / "file-memory.json"
    if marker.exists() or marker.is_symlink():
        memory = json.loads(private_bytes(marker))
        if memory.get("version") != 1 or memory.get("state_home") != str(home):
            raise ValueError("Invalid file-memory destination.")
        return absolute_path(memory.get("memory_root")).parent / "managed-skills"
    return home / "managed-skills"


def destinations(store):
    if store is None:
        return []
    result = []

    def add(identifier, label, resolve):
        entry = {"id": identifier, "label": label}
        try:
            root = resolve()
            # Explicit overrides remain subject to the Go catalog's canonical-path check.
            entry["root"] = str(root)
        except (OSError, ValueError, TypeError, KeyError):
            entry["error"] = "Cannot resolve this configured Skill destination."
        result.append(entry)

    if store.web_config:
        add("web", "Web Chat", lambda: cli_root(store.web_config))
    if store.handoff and store.handoff != store.web_config:
        add("cli", "CLI handoff", lambda: cli_root(store.handoff))
    host_path = store.config / "host.json"
    if host_path.exists():
        try:
            host = json.loads(private_bytes(host_path))
        except (OSError, ValueError):
            result.append({"id": "host", "label": "Host", "error": "Cannot read Host configuration."})
            return result
        for path in sorted(store.config.glob("channels.*.json")):
            name = path.name.removeprefix("channels.").removesuffix(".json")

            def channel_root(path=path):
                channel = json.loads(private_bytes(path))
                profile = channel.get("execution_profile") or host.get("default_profile")
                execution = host["profiles"][profile].get("execution", {})
                return absolute_path(execution.get("skills_root") or str(store.root / "managed-skills"))

            add(name, name, channel_root)
    return result


def inspect(store, destination="", selectors=None):
    choices = destinations(store)
    if not choices:
        return {"enabled": False, "destinations": [], "records": [], "coverage": "unavailable"}
    selected = next((d for d in choices if d["id"] == destination), None) if destination else choices[0]
    if selected is None:
        raise KeyError("Unknown configured Skill destination.")
    result = {"enabled": True, "destinations": choices, "destination": selected, "coverage": "authored_registry",
              "notice": "Authored publications only. Experience and plugin catalogs are visible in recorded Agent snapshots; current runtime readiness is not inferred."}
    if selected.get("error"):
        return {**result, "error": selected["error"], "records": []}
    args = [str(store.binary), "skills", "inspect", "--root", selected["root"]]
    if selectors:
        if set(selectors) != {"id", "revision", "digest", "file"}:
            raise ValueError("File reads require exactly id, revision, digest and file.")
        for key, value in selectors.items():
            args += ["--" + key + "=" + str(value)]
    try:
        completed = subprocess.run(args, capture_output=True, timeout=15, check=False)
        if completed.returncode:
            # CLI diagnostics may include authored content or local credentials.
            raise ValueError("Skill inspection failed. Check catalog integrity, revision and CLI compatibility.")
        data = json.loads(completed.stdout)
        if not isinstance(data, dict) or (not selectors and data.get("version") != "managed-skills-v1"):
            raise ValueError("Unsupported Skill inspection response.")
        if selectors and (not isinstance(data.get("content"), str) or data.get("path") != selectors["file"] or
                          data.get("id") != selectors["id"] or data.get("digest") != selectors["digest"] or
                          str(data.get("revision")) != str(selectors["revision"])):
            raise ValueError("Skill file response does not match the inspected revision.")
    except (OSError, subprocess.TimeoutExpired, ValueError):
        return {**result, "error": "Cannot inspect the pinned Skill catalog or file. Check destination, revision, integrity and CLI compatibility.", "records": []}
    safe = preview(data)
    if selectors:
        safe["redacted"] = safe.get("content") != data.get("content")
        safe["complete"] = True
    return {**result, **safe}
