"""Link the source-built CLI and memory skill into supported local clients."""

import argparse
import os
from pathlib import Path
import shutil
import sys


AGENTS = ("codex", "claude", "opencode", "gemini", "cursor", "copilot")
SKILL = "agent-host-memory"


def configured_path(env, key, default):
    value = env.get(key)
    if not value:
        return default
    path = Path(value).expanduser()
    if not path.is_absolute():
        raise ValueError(f"{key} must be an absolute path")
    return path


def agent_roots(home, env):
    config = configured_path(env, "XDG_CONFIG_HOME", home / ".config")
    return {
        "codex": configured_path(env, "CODEX_HOME", home / ".codex"),
        "claude": configured_path(env, "CLAUDE_CONFIG_DIR", home / ".claude"),
        "opencode": configured_path(env, "OPENCODE_CONFIG_DIR", config / "opencode"),
        "gemini": home / ".gemini",
        "cursor": home / ".cursor",
        "copilot": home / ".copilot",
    }


def link(source, target):
    """Never replace an existing file, directory, or foreign/dangling symlink."""
    source = source.resolve(strict=True)
    if target.is_symlink():
        try:
            if target.resolve() == source:
                return "already-linked"
        except (OSError, RuntimeError):
            pass
        return "preserved"
    if target == source:
        return "already-linked"
    if target.exists():
        return "preserved"
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        target.symlink_to(source, target_is_directory=source.is_dir())
    except FileExistsError:
        # Another installer or a user won the race; never overwrite its work.
        return "preserved"
    return "installed"


def install(repo, home, env, bin_dir, agents=None, which=shutil.which):
    repo = repo.resolve(strict=True)
    binary, skill = repo / "bin" / "agent-host", repo / "skills" / SKILL
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("build bin/agent-host first, or run make install")
    if not (skill / "SKILL.md").is_file() or not (skill / "references" / "protocol.md").is_file():
        raise ValueError("the source checkout is missing the complete memory skill")
    roots = agent_roots(home, env)
    rows = []

    def attempt(name, source, target):
        try:
            status = link(source, target)
            rows.append((name, status, str(target)))
        except OSError as error:
            rows.append((name, "failed", f"{target}: {error}"))

    attempt("cli", binary, bin_dir / "agent-host")
    for name, root in roots.items():
        # Current Codex uses the shared user-level directory. An explicitly
        # configured CODEX_HOME retains its existing profile-local convention.
        skills_root = home / ".agents" / "skills" if name == "codex" and not env.get("CODEX_HOME") else root / "skills"
        target = skills_root / SKILL
        if agents is not None and name not in agents:
            rows.append((name, "not-selected", str(target)))
            continue
        detected = root.is_dir() or which(name, path=env.get("PATH", "")) is not None
        if agents is None and not detected:
            rows.append((name, "not-detected", str(target)))
            continue
        attempt(name, skill, target)
    return rows


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agents", help="Comma-separated supported agents; default: detect locally")
    parser.add_argument("--bin-dir", type=Path, default=Path.home() / ".local" / "bin")
    parser.add_argument("--no-autostart", action="store_true", help="Install CLI and skills only")
    parser.add_argument("--config-dir", type=Path, help="Private runtime config directory for automatic startup")
    args = parser.parse_args(argv)
    selected = None
    if args.agents is not None:
        selected = set(args.agents.split(","))
        if not selected or not selected <= set(AGENTS):
            parser.error("--agents must contain only: " + ", ".join(AGENTS))
    if not args.bin_dir.is_absolute():
        parser.error("--bin-dir must be an absolute path")
    if args.config_dir is not None and not args.config_dir.is_absolute():
        parser.error("--config-dir must be an absolute path")
    try:
        rows = install(Path(__file__).resolve().parents[1], Path.home(), os.environ,
                       args.bin_dir, selected)
    except (ValueError, OSError) as error:
        print(f"Install failed: {error}", file=sys.stderr)
        return 1
    for name, status, target in rows:
        print(f"{name}: {status}: {target}")
    print("\nLinks use this source checkout; keep it in place and rerun make install after moving it.")
    print("New skills are available in the next agent session; no memory connection was changed.")
    paths = [Path(p).expanduser().resolve() for p in os.environ.get("PATH", "").split(os.pathsep) if p]
    if args.bin_dir.resolve() not in paths:
        print(f"CLI directory is not on PATH: {args.bin_dir}. Add it to PATH or use the checkout's bin/agent-host.")
    if any(status == "preserved" for _, status, _ in rows):
        print("Existing custom targets were preserved; resolve them explicitly to replace an installation.")
    failed = any(status == "failed" or name == "cli" and status == "preserved"
                 for name, status, _ in rows)
    if failed:
        return 1
    if not args.no_autostart:
        from service import autostart
        repo = Path(__file__).resolve().parents[1]
        config = args.config_dir or repo / ".agent-host/config"
        if args.config_dir is None and not (config / "host.json").is_file():
            print("Autostart not configured: prepare .agent-host/config, then rerun make install.")
        else:
            try:
                target = autostart.install(repo, config, Path.home(), os.environ)
            except (OSError, ValueError, RuntimeError) as error:
                print(f"Autostart failed: {error}", file=sys.stderr)
                return 1
            print(f"Autostart installed and all enabled channels connected: {target}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
