#!/usr/bin/env python3
"""Explicit stopped-runtime migration to the unified configuration layout."""

import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3


def read(path):
    return json.loads(path.read_text())


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def database_receipt(path):
    if not path.exists():
        return None
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
        if db.execute("pragma integrity_check").fetchone()[0] != "ok":
            raise RuntimeError("database integrity check failed")
        counts = {}
        for table in ("runs", "events", "memories", "candidates", "channel_messages"):
            counts[table] = db.execute(f"select count(*) from {table}").fetchone()[0]
    return {"sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "counts": counts}


def migrate(root):
    root = root.resolve(strict=True)
    if (root / "config").exists():
        raise RuntimeError("unified config already exists; refusing to overwrite")
    registrations = root / "channels"
    old_roots = {"dingtalk": root / "dingtalk", "feishu": root / "runtime"}
    # Require the known deployed layout; never guess another operator's roots.
    for name, old in old_roots.items():
        registration = read(registrations / f"channel-{name}.json")
        selected = (registrations / registration["runtime_root"]).resolve()
        if selected != old or not registration["enabled"]:
            raise RuntimeError("migration requires the existing enabled channel pair")
    for old in old_roots.values():
        service = read(old / "config/component-service.json")
        if service.get("discovery_config"):
            raise RuntimeError("discovery migration requires an explicit operator mapping")
    if (root / "knowledge").exists() or (root / "plugins").exists():
        raise RuntimeError("destination data already exists; refusing to overwrite")
    email_state = Path(read(root / "email/data-email-accounts.json")["state_directory"]).resolve()
    dws_state = Path(read(root / "dingtalk/config/data-dingtalk-workspace.json")["state_directory"]).resolve()
    if email_state != root / "email/state" or dws_state != root / "dingtalk/workspace":
        raise RuntimeError("external capability state requires an explicit operator mapping")
    locks = []
    backup = None
    backup_ready = False
    try:
        for old in old_roots.values():
            for path in (old / "run.lock", old / "state/host.lock"):
                lock = path.open("a+")
                locks.append(lock)
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup = root / "backups" / f"unified-config-{stamp}"
        backup.mkdir(parents=True, mode=0o700)
        for name in ("dingtalk", "runtime", "email", "channels"):
            shutil.copytree(root / name, backup / name, symlinks=True)
        backup_ready = True
        before = {name: database_receipt(old / "state/state.db") for name, old in old_roots.items()}
        config = root / "config"
        credentials = config / "credentials"
        config.mkdir(mode=0o700)
        credentials.mkdir(mode=0o700)
        profiles = {}
        path_map = {}
        for name, old in old_roots.items():
            old_config = old / "config"
            for file in old_config.iterdir():
                if file.name.startswith("credential-") or file.suffix == ".secret":
                    target = credentials / f"{name}.{file.name}"
                    shutil.copy2(file, target)
                    target.chmod(0o600)
                    path_map[str(file)] = str(target)
            env = read(old_config / "component-environment.json")
            write(credentials / f"environment.{name}.json", env)
            execution = read(old_config / "component-service.json")
            execution.pop("email_config", None)
            execution.pop("knowledge_config", None)
            execution.pop("channel", None)
            execution["memory_scope"] = str(old / "workspace")
            if execution.get("discovery_config"):
                raise RuntimeError("discovery migration requires an explicit operator mapping")
            if name == "dingtalk":
                agents = read(old_config / "component-agents.json")
                instructions = old_config / "CLAUDE.md"
                if instructions.exists():
                    shutil.copy2(instructions, config / "CLAUDE.md")
                    (config / "CLAUDE.md").chmod(0o600)
                    path_map[str(instructions)] = str(config / "CLAUDE.md")
                for agent in agents["agents"]:
                    key = "instructions_file"
                    if key in agent:
                        agent[key] = path_map.get(agent[key], agent[key])
                write(config / "agents.json", agents)
                execution["agents_config"] = "agents.json"
            profiles[name] = {"execution": execution, "environment_file": f"credentials/environment.{name}.json"}
        write(config / "host.json", {"version": 1, "default_profile": "dingtalk", "profiles": profiles})
        bot = read(old_roots["dingtalk"] / "config/channel-dingtalk.json")
        bot["client_secret_file"] = path_map[bot["client_secret_file"]]
        bot.pop("workspace_config", None)
        bot["enabled"] = True
        dws = read(old_roots["dingtalk"] / "config/data-dingtalk-workspace.json")
        dws.pop("state_directory")
        dws["enabled"] = True
        write(config / "channels.dingtalk.json", {"version": 1, "enabled": True, "execution_profile": "dingtalk", "settings": {"bot": bot, "dws": dws}})
        feishu = read(old_roots["feishu"] / "config/channel-feishu.json")
        write(config / "channels.feishu.json", {"version": 1, "enabled": True, "execution_profile": "feishu", "settings": feishu})
        email = read(root / "email/data-email-accounts.json")
        email.pop("state_directory")
        for account in email["accounts"]:
            source = Path(account["credential_file"])
            target = credentials / source.name
            shutil.copy2(source, target)
            target.chmod(0o600)
            path_map[str(source)] = str(target)
            account["credential_file"] = str(target)
        write(config / "plugins.email.json", {"version": 1, "enabled": True, "access": {"channels": ["dingtalk", "feishu"], "audiences": ["owner"]}, "settings": email})
        knowledge = read(old_roots["dingtalk"] / "config/data-knowledge.json")
        old_knowledge = old_roots["dingtalk"] / "knowledge"
        for source in knowledge["sources"]:
            if "credential_file" in source:
                source["credential_file"] = path_map.get(source["credential_file"], source["credential_file"])
            if source.get("root", "").startswith(str(old_knowledge) + "/"):
                source["root"] = str(root / "knowledge" / Path(source["root"]).relative_to(old_knowledge))
        write(config / "plugins.knowledge.json", {"version": 1, "enabled": True, "access": {"channels": ["dingtalk"], "audiences": ["owner"]}, "settings": knowledge})
        for name, old in old_roots.items():
            target = registrations / name
            old.rename(target)
            shutil.rmtree(target / "config")
        channel_root = registrations / "dingtalk"
        (channel_root / "knowledge").rename(root / "knowledge")
        dws_root = channel_root / "dws"
        dws_root.mkdir(mode=0o700)
        known = {".data.lock", "audit", "identity.json", "keychain", "logs", "profiles.json", "token.json", "host-operations.db", "host-operations.db-wal", "host-operations.db-shm"}
        for file in (channel_root / "workspace").iterdir():
            if file.name in known:
                file.rename(dws_root / file.name)
        plugin_root = root / "plugins"
        plugin_root.mkdir(mode=0o700)
        old_email = root / "email"
        # Move the exact old state root, preserving one shared operation ledger.
        email_state = old_email / "state"
        if email_state.exists():
            email_state.rename(plugin_root / "email")
        else:
            (plugin_root / "email").mkdir(mode=0o700)
        old_email.rename(backup / "email-original")
        for file in registrations.glob("channel-*.json"):
            file.unlink()
        after = {name: database_receipt(registrations / name / "state/state.db") for name in old_roots}
        if before != after:
            raise RuntimeError("database changed during migration; restore the stopped-runtime backup")
        report = {"status": "migrated", "backup": str(backup), "databases_before": before, "databases_after": after,
                  "memory_scope_policy": "retain existing default scope; new channels use stable IDs",
                  "remaining_workspace_files": sorted(p.name for p in (channel_root / "workspace").iterdir()),
                  "channels": {name: str(registrations / name) for name in old_roots}}
        write(backup / "migration.json", report)
        print(json.dumps({"status": "migrated", "backup": str(backup), "databases_unchanged": True}))
    except Exception:
        if backup_ready:
            for name in ("config", "knowledge", "plugins", "dingtalk", "runtime", "email", "channels"):
                target = root / name
                if target.exists():
                    shutil.rmtree(target)
            for name in ("dingtalk", "runtime", "email", "channels"):
                shutil.copytree(backup / name, root / name, symlinks=True)
        raise
    finally:
        for lock in locks:
            lock.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    args = parser.parse_args()
    migrate(args.root)
