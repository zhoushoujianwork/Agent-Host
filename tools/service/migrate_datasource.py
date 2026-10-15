#!/usr/bin/env python3
"""Split stopped unified knowledge/email configuration into datasource plugins."""

import argparse
import datetime
import fcntl
import hashlib
import json
from pathlib import Path
import shutil
import sqlite3


PROVIDERS = {"local_markdown": "local-documents", "dokki": "dokki", "confluence": "confluence"}


def read(path):
    return json.loads(path.read_text())


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def receipt(path):
    if not path.exists():
        return None
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
        if db.execute("pragma integrity_check").fetchone()[0] != "ok":
            raise RuntimeError("database integrity check failed")
    return hashlib.sha256(path.read_bytes()).hexdigest()


def migrate(root):
    root = root.resolve(strict=True)
    config = root / "config"
    if list(config.glob("datasource.plugins.*.json")):
        raise RuntimeError("datasource configuration already exists; refusing mixed migration")
    old_knowledge = config / "plugins.knowledge.json"
    old_email = config / "plugins.email.json"
    if not old_knowledge.exists() and not old_email.exists():
        raise RuntimeError("no legacy datasource configuration to migrate")
    target = root / "datasource/plugins/email"
    if target.exists():
        raise RuntimeError("destination email state already exists")
    groups = {}
    aliases = {}
    if old_knowledge.exists():
        document = read(old_knowledge)
        for source in document.get("settings", {}).get("sources", []):
            source = dict(source)
            provider = source.pop("provider")
            source.pop("projects", None)
            source.pop("audiences", None)
            if provider not in PROVIDERS:
                raise RuntimeError("unknown legacy provider")
            plugin = PROVIDERS[provider]
            name = source["name"]
            if name in aliases:
                raise RuntimeError("ambiguous legacy source reference")
            aliases[name] = f"{plugin}:{name}"
            groups.setdefault(plugin, []).append(source)
        if document["enabled"] and not groups:
            raise RuntimeError("enabled legacy knowledge has no sources")
        if not document["enabled"] and not groups:
            groups = {plugin: [] for plugin in PROVIDERS.values()}
    locks = []
    backup = None
    ready = False
    old_state = root / "plugins/email"
    moved = False
    try:
        for channel in sorted((root / "channels").iterdir()):
            if channel.is_dir() and (channel / "state").exists():
                lock = (channel / "run.lock").open("a+")
                locks.append(lock)
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = {str(path.relative_to(root)): receipt(path) for path in (root / "channels").glob("*/state/state.db")}
        email_before = receipt(old_state / "operations.db")
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup = root / "backups" / f"datasource-config-{stamp}"
        backup.mkdir(parents=True, mode=0o700)
        shutil.copytree(config, backup / "config", symlinks=True)
        if old_state.exists():
            shutil.copytree(old_state, backup / "email", symlinks=True)
        ready = True
        for plugin, sources in groups.items():
            envelope = {key: value for key, value in document.items() if key not in ("settings", "access")}
            envelope["settings"] = {"sources": sources}
            write(config / f"datasource.plugins.{plugin}.json", envelope)
        if old_email.exists():
            email = read(old_email)
            email.pop("access", None)
            email.get("settings", {}).pop("state_directory", None)
            write(config / "datasource.plugins.email.json", email)
        agents_path = config / "agents.json"
        if agents_path.exists():
            agents = read(agents_path)
            for agent in agents["agents"]:
                agent.pop("knowledge_sources", None)
                agent.pop("plugins", None)
            write(agents_path, agents)
        host_path = config / "host.json"
        host = read(host_path)
        tool_map = {f"mcp__knowledge_{name}__": f"mcp__knowledge_{identity.replace(':', '_')}__" for name, identity in aliases.items()}
        for profile in host["profiles"].values():
            rules = profile["execution"].get("allowed_tools", [])
            for index, rule in enumerate(rules):
                for old, new in tool_map.items():
                    if rule.startswith(old):
                        rules[index] = new + rule[len(old):]
                        break
        write(host_path, host)
        if old_state.exists():
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            (root / "datasource").chmod(0o700)
            target.parent.chmod(0o700)
            old_state.rename(target)
            moved = True
        old_knowledge.unlink(missing_ok=True)
        old_email.unlink(missing_ok=True)
        after = {str(path.relative_to(root)): receipt(path) for path in (root / "channels").glob("*/state/state.db")}
        if before != after or email_before != receipt(target / "operations.db"):
            raise RuntimeError("migration changed a state database")
        report = {"status": "migrated", "backup": str(backup), "channel_databases_unchanged": True,
                  "email_ledger_unchanged": True, "source_mapping": aliases,
                  "plugins": sorted(groups) + (["email"] if (config / "datasource.plugins.email.json").exists() else [])}
        write(backup / "migration.json", report)
        print(json.dumps(report))
        return report
    except Exception:
        if ready:
            shutil.rmtree(config)
            shutil.copytree(backup / "config", config, symlinks=True)
            if moved:
                target.rename(old_state)
        raise
    finally:
        for lock in locks:
            lock.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    migrate(parser.parse_args().root)
