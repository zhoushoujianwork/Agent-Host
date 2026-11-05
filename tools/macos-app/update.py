#!/usr/bin/env python3
"""Source-only updater backend for the macOS app.

Commands (JSON on stdout, exit 0 only when ``ok`` is true):
  update.py --bundle APP [--cache DIR] check
  update.py --bundle APP [--cache DIR] prepare [--allow-dev] [--revision SHA]
  update.py --bundle APP [--cache DIR] apply --handoff FILE
  update.py --bundle APP [--cache DIR] recover

Nothing here starts channel services, replays tasks or touches a user checkout.
Design and contract: docs/mac-app-updates.md.
"""
import argparse
import collections
import contextlib
import dataclasses
import fcntl
import hashlib
import json
import os
import platform
import plistlib
import re
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional

OFFICIAL_REPO = "https://github.com/zhoushoujianwork/Agent-Host.git"
DEFAULT_DEV_REF = "main"
BUNDLE_ID = "local.agent-host.workspace"
STABLE_TAG = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
STABLE_VERSION = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
SHA = re.compile(r"^[0-9a-f]{40}$")
TOKEN = re.compile(r"^[A-Za-z0-9_-]{16,64}$")
DEV_VERSION = re.compile(r"^[0-9A-Za-z._+-]{1,64}$")
REF = re.compile(r"^(?!.*\.\.)[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$")
HOST_VERSION = re.compile(r"^agent-host (\S+) \(commit (\S+), dirty=(true|false|unknown), ")
NETWORK_ERROR = re.compile(r"resolve host|unreachable|connection|timed out|unable to access|could not read from remote|ssl|tls", re.I)
METADATA_KEYS = {"version", "revision", "channel", "dirty", "architecture", "min_os", "repository", "development_ref"}
ACTIVE_STATES = {"switching", "switched", "launched", "manual_recovery"}
MAX_JSON = 16384
Result = collections.namedtuple("Result", "returncode stdout stderr timed_out")


class UpdateError(Exception):
    def __init__(self, status: str, message: str, **extra: Any):
        super().__init__(message)
        self.status, self.message, self.extra = status, message, extra


def scrub(text: str, limit: int = 300) -> str:
    text = re.sub(r"(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@", r"\1", text or "")
    return text.strip()[:limit]


def tool_path() -> str:
    # Finder-launched apps do not inherit an interactive shell's Homebrew/Go
    # PATH. Use the usual installed locations without installing any tools.
    paths = ["/opt/homebrew/bin", "/usr/local/bin", "/usr/local/go/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"]
    paths.extend(os.environ.get("PATH", "").split(os.pathsep))
    return os.pathsep.join(dict.fromkeys(path for path in paths if path))


def find_tool(name: str) -> Optional[str]:
    return shutil.which(name, path=tool_path())


def default_runner(argv: List[str], cwd: Optional[str] = None, timeout: float = 60) -> Result:
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0", LC_ALL="C", PATH=tool_path())
    try:
        done = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, timeout=timeout, stdin=subprocess.DEVNULL)
    except subprocess.TimeoutExpired:
        return Result(124, "", "timed out", True)
    except OSError as error:
        return Result(127, "", error.strerror or "cannot execute", False)
    return Result(done.returncode, done.stdout.decode("utf-8", "replace"), done.stderr.decode("utf-8", "replace"), False)


@dataclasses.dataclass
class Context:
    runner: Callable = default_runner
    which: Callable = find_tool
    sleep: Callable = time.sleep
    monotonic: Callable = time.monotonic
    wall: Callable = time.time
    kill: Callable = os.kill
    uid: int = dataclasses.field(default_factory=os.geteuid)
    pid: int = dataclasses.field(default_factory=os.getpid)
    machine: str = dataclasses.field(default_factory=platform.machine)
    mac_version: Callable = lambda: platform.mac_ver()[0]
    python: str = sys.executable
    owner_wait: float = 60.0
    health_wait: float = 60.0
    term_wait: float = 10.0
    poll: float = 0.5


# ---------------------------------------------------------------- metadata

def parse_host_version(text: str) -> Optional[Dict[str, str]]:
    match = HOST_VERSION.match(text.strip())
    return {"version": match.group(1), "revision": match.group(2), "dirty": match.group(3)} if match else None


def is_official_url(url: str) -> bool:
    url = url.strip().rstrip("/")
    if url.endswith(".git"):
        url = url[:-4]
    return url.lower() in {OFFICIAL_REPO[:-4].lower(), "git@github.com:zhoushoujianwork/agent-host"}


def _version_tuple(text: str, pattern=STABLE_VERSION):
    match = pattern.match(text)
    return tuple(int(part) for part in match.groups()) if match else None


def validate_metadata(meta: Any) -> Dict[str, Any]:
    if not isinstance(meta, dict) or set(meta) != METADATA_KEYS:
        raise UpdateError("invalid_bundle", "AGHBuildMetadata has missing or unknown fields")
    for key in METADATA_KEYS - {"dirty"}:
        if not isinstance(meta[key], str) or len(meta[key]) > 200:
            raise UpdateError("invalid_bundle", "AGHBuildMetadata field %s must be a short string" % key)
    if type(meta["dirty"]) is not bool:
        raise UpdateError("invalid_bundle", "AGHBuildMetadata dirty must be boolean")
    if meta["channel"] not in ("stable", "dev"):
        raise UpdateError("invalid_bundle", "unknown channel")
    if meta["channel"] == "stable" and not STABLE_VERSION.match(meta["version"]):
        raise UpdateError("invalid_bundle", "stable version must be MAJOR.MINOR.PATCH")
    if meta["channel"] == "dev" and not DEV_VERSION.match(meta["version"]):
        raise UpdateError("invalid_bundle", "invalid development version")
    if not (SHA.match(meta["revision"]) or meta["revision"] == "unknown"):
        raise UpdateError("invalid_bundle", "revision must be a full commit or unknown")
    if not REF.match(meta["development_ref"]):
        raise UpdateError("invalid_bundle", "invalid development_ref")
    if not re.match(r"^\d+(\.\d+){0,2}$", meta["min_os"]) or not re.match(r"^\w{1,16}$", meta["architecture"]):
        raise UpdateError("invalid_bundle", "invalid architecture or min_os")
    return meta


def read_bundle_info(bundle: Path) -> Dict[str, Any]:
    try:
        with (bundle / "Contents/Info.plist").open("rb") as handle:
            info = plistlib.load(handle)
    except (OSError, plistlib.InvalidFileException, ValueError):
        raise UpdateError("invalid_bundle", "cannot read bundle Info.plist")
    if not isinstance(info, dict):
        raise UpdateError("invalid_bundle", "Info.plist is not a dictionary")
    return info


def read_manifest(bundle: Path) -> Dict[str, Any]:
    info = read_bundle_info(bundle)
    if "AGHBuildMetadata" not in info:
        raise UpdateError("incompatible", "bundle predates build metadata; rebuild it from source once")
    return validate_metadata(info["AGHBuildMetadata"])


def installability(ctx: Context, meta: Dict[str, Any]) -> Optional[UpdateError]:
    if not is_official_url(meta["repository"]):
        return UpdateError("incompatible", "bundle was not built from the official repository")
    if meta["revision"] == "unknown":
        return UpdateError("incompatible", "bundle revision is unknown")
    if meta["architecture"] != ctx.machine:
        return UpdateError("incompatible", "bundle architecture differs from this machine")
    found = re.match(r"^(\d+)(?:\.(\d+))?(?:\.(\d+))?", ctx.mac_version() or "")
    if found:
        have = tuple(int(part or 0) for part in found.groups())
        need = tuple(int(part) for part in meta["min_os"].split("."))
        if have < need + (0,) * (3 - len(need)):
            return UpdateError("incompatible", "macOS is older than the bundle minimum")
    if meta["dirty"]:
        return UpdateError("not_updatable", "dirty source builds are never updated automatically")
    return None


# ------------------------------------------------------------------ check

def _remote(ctx: Context, *args: str) -> str:
    done = ctx.runner(["git", "-c", "credential.helper=", "ls-remote", OFFICIAL_REPO] + list(args), timeout=30)
    if done.returncode != 0:
        status = "offline" if done.timed_out or NETWORK_ERROR.search(done.stderr) else "remote_error"
        raise UpdateError(status, "cannot read the official repository", detail=scrub(done.stderr))
    return done.stdout


def latest_stable(ctx: Context) -> Optional[Dict[str, Any]]:
    direct, peeled = {}, {}
    for line in _remote(ctx, "refs/tags/v*").splitlines():
        sha, _, ref = line.partition("\t")
        if not SHA.match(sha) or not ref.startswith("refs/tags/"):
            continue
        name, is_peeled = (ref[10:-3], True) if ref.endswith("^{}") else (ref[10:], False)
        if STABLE_TAG.match(name):
            (peeled if is_peeled else direct)[name] = sha
    tags = [(tuple(int(p) for p in STABLE_TAG.match(n).groups()), n) for n in direct]
    if not tags:
        return None
    key, name = max(tags)
    return {"version": name[1:], "revision": peeled.get(name, direct[name]), "channel": "stable", "ref": "refs/tags/" + name}


def remote_dev(ctx: Context, ref: str) -> Optional[Dict[str, Any]]:
    wanted = "refs/heads/" + ref
    for line in _remote(ctx, wanted).splitlines():
        sha, _, name = line.partition("\t")
        if name == wanted and SHA.match(sha):
            return {"version": "dev-" + sha[:12], "revision": sha, "channel": "dev", "ref": wanted}
    return None


def check(ctx: Context, bundle: Path) -> Dict[str, Any]:
    try:
        return _check(ctx, bundle)
    except UpdateError as error:
        return error_result(error)


def _check(ctx: Context, bundle: Path) -> Dict[str, Any]:
    meta = read_manifest(bundle)
    installed = {k: meta[k] for k in ("version", "revision", "channel")}
    base = {"repository": OFFICIAL_REPO, "installed": installed}
    problem = installability(ctx, meta)
    if problem:
        return dict(error_result(problem), **base)
    if meta["channel"] == "stable":
        target = latest_stable(ctx)
        if target is None:
            return dict(base, ok=True, status="no_published_version", message="no stable vMAJOR.MINOR.PATCH tag is published")
        newer = _version_tuple(target["version"]) > _version_tuple(meta["version"])
        auto = True
    else:
        target = remote_dev(ctx, meta["development_ref"])
        if target is None:
            return dict(base, ok=False, status="remote_error", message="development ref not found on the official repository")
        newer = target["revision"] != meta["revision"]
        auto = False
    if not newer:
        return dict(base, ok=True, status="up_to_date", message="installed build is current")
    return dict(base, ok=True, status="available", target=target, automatic_allowed=auto, requires_consent=not auto,
                message="update available; development builds always need explicit consent" if not auto else "update available")


def error_result(error: UpdateError) -> Dict[str, Any]:
    return dict(error.extra, ok=error.status in ("up_to_date", "available", "no_published_version"), status=error.status, message=error.message)


# ----------------------------------------------------------- filesystem

def inside(child: Path, parent: Path) -> bool:
    return child != parent and child.parts[:len(parent.parts)] == parent.parts


def ensure_cache(ctx: Context, path: Any) -> Path:
    cache = Path(str(path))
    if not cache.is_absolute() or "\0" in str(path):
        raise UpdateError("invalid_cache", "cache path must be absolute")
    cache.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = os.lstat(cache)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != ctx.uid or info.st_mode & 0o077:
        raise UpdateError("invalid_cache", "cache must be a private directory owned by the current user")
    real = cache.resolve()
    for name in ("staging", "candidates"):
        (real / name).mkdir(mode=0o700, exist_ok=True)
    return real


@contextlib.contextmanager
def updater_lock(cache: Path):
    try:
        descriptor = os.open(str(cache / "update.lock"), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    except OSError:
        raise UpdateError("invalid_cache", "cannot open the updater lock")
    try:
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            raise UpdateError("locked", "another updater operation is running")
        yield
    finally:
        os.close(descriptor)


def write_json(path: Path, value: Any) -> str:
    data = json.dumps(value, sort_keys=True, indent=1).encode()
    temporary = path.with_name(path.name + ".tmp-" + secrets.token_hex(4))
    descriptor = os.open(str(temporary), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as handle:
        handle.write(data)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(str(temporary), str(path))
    return hashlib.sha256(data).hexdigest()


def read_json_file(ctx: Context, path: Path, mask: int = 0o022, missing_ok: bool = True) -> Optional[Dict[str, Any]]:
    """Return None when absent or still partial; raise for unsafe files."""
    try:
        info = os.lstat(path)
    except FileNotFoundError:
        if missing_ok:
            return None
        raise UpdateError("invalid_file", "required file is missing")
    if not stat.S_ISREG(info.st_mode) or info.st_uid != ctx.uid or info.st_mode & mask or info.st_size > MAX_JSON:
        raise UpdateError("invalid_file", "file must be a small regular file owned by the current user, not writable by others")
    try:
        data = json.loads(path.read_bytes())
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict):
        raise UpdateError("invalid_file", "file must contain a JSON object")
    return data


def tree_digest(root: Path) -> str:
    """Deterministic content digest; any symlink or special file is refused."""
    digest = hashlib.sha256()

    def walk(directory: Path, prefix: str):
        for entry in sorted(os.scandir(directory), key=lambda item: item.name):
            relative = prefix + entry.name
            info = entry.stat(follow_symlinks=False)
            if stat.S_ISLNK(info.st_mode):
                raise UpdateError("invalid_candidate", "bundle contains a symbolic link")
            if stat.S_ISDIR(info.st_mode):
                digest.update(b"D\0" + relative.encode() + b"\0")
                walk(Path(entry.path), relative + "/")
            elif stat.S_ISREG(info.st_mode):
                digest.update(b"F\0" + relative.encode() + b"\0" + (b"x" if info.st_mode & 0o111 else b"-"))
                inner = hashlib.sha256()
                with open(entry.path, "rb") as handle:
                    for block in iter(lambda: handle.read(1 << 20), b""):
                        inner.update(block)
                digest.update(inner.digest())
            else:
                raise UpdateError("invalid_candidate", "bundle contains a special file")

    walk(root, "")
    return digest.hexdigest()


# --------------------------------------------------------------- bundles

def inspect_bundle(ctx: Context, app: Path, expect: Optional[Dict[str, str]] = None) -> Dict[str, Any]:
    """Verify a candidate bundle; return its manifest and content digest."""
    if app.is_symlink() or not app.is_dir():
        raise UpdateError("invalid_candidate", "candidate is not a plain directory")
    info = read_bundle_info(app)
    meta = validate_metadata(info.get("AGHBuildMetadata"))
    if info.get("CFBundleIdentifier") != BUNDLE_ID or info.get("CFBundleExecutable") != "Agent-Host":
        raise UpdateError("invalid_candidate", "candidate bundle identity differs")
    problem = installability(ctx, meta)
    if problem:
        raise UpdateError("invalid_candidate", problem.message)
    for key in ("version", "revision", "channel"):
        if expect and meta[key] != expect[key]:
            raise UpdateError("invalid_candidate", "candidate %s differs from the pinned value" % key)
    if not (app / "Contents/MacOS/Agent-Host").is_file():
        raise UpdateError("invalid_candidate", "candidate executable is missing")
    done = ctx.runner(["codesign", "--verify", "--deep", "--strict", str(app)], timeout=120)
    if done.returncode != 0:
        raise UpdateError("invalid_candidate", "candidate code signature does not verify", detail=scrub(done.stderr))
    host = ctx.runner([str(app / "Contents/Resources/bin/agent-host"), "--version"], timeout=30)
    parsed = parse_host_version(host.stdout) if host.returncode == 0 else None
    if not parsed or parsed["version"] != meta["version"] or parsed["revision"] != meta["revision"] or parsed["dirty"] != "false":
        raise UpdateError("invalid_candidate", "bundled Host version or revision does not match the app metadata")
    digest = tree_digest(app)
    if expect and "content_digest" in expect and digest != expect["content_digest"]:
        raise UpdateError("invalid_candidate", "candidate content digest differs")
    return {"manifest": meta, "info": info, "content_digest": digest}


def candidate_names(install: Path, token: str) -> Dict[str, Path]:
    return {kind: install.parent / (".%s.%s-%s" % (install.stem, kind, token[:16])) for kind in ("new", "previous", "failed")}


def executable_of(install: Path) -> Path:
    name = read_bundle_info(install).get("CFBundleExecutable")
    if not isinstance(name, str) or "/" in name or not name:
        raise UpdateError("invalid_install", "installed bundle has no valid executable")
    return install / "Contents/MacOS" / name


def process_table(ctx: Context) -> List[Any]:
    done = ctx.runner(["ps", "-axo", "pid=,stat=,comm="], timeout=15)
    if done.returncode != 0:
        raise UpdateError("process_listing_failed", "cannot list processes")
    rows = []
    for line in done.stdout.splitlines():
        parts = line.split(None, 2)
        if len(parts) == 3 and parts[0].isdigit():
            rows.append((int(parts[0]), parts[1], parts[2].strip()))
    return rows


def processes_running(ctx: Context, executable: Path) -> List[int]:
    wanted = os.path.realpath(str(executable))
    return [pid for pid, state, comm in process_table(ctx)
            if not state.startswith("Z") and comm.startswith("/") and os.path.basename(comm) == executable.name
            and os.path.realpath(comm) == wanted]


# --------------------------------------------------------------- prepare

DEPENDENCIES = ("git", "make", "go", "swiftc", "codesign")


def prepare(ctx: Context, bundle: Path, cache: Path, allow_dev: bool = False, expect_revision: Optional[str] = None) -> Dict[str, Any]:
    try:
        with updater_lock(cache):
            return _prepare(ctx, bundle, cache, allow_dev, expect_revision)
    except UpdateError as error:
        return error_result(error)


def _prepare(ctx, bundle, cache, allow_dev, expect_revision):
    verdict = _check(ctx, bundle)
    if verdict["status"] != "available":
        return verdict
    target, meta = verdict["target"], read_manifest(bundle)
    if target["channel"] == "dev" and not allow_dev:
        raise UpdateError("consent_required", "development updates need explicit consent (--allow-dev)", target=target)
    if expect_revision and expect_revision != target["revision"]:
        raise UpdateError("ref_moved", "the selected ref no longer matches the approved revision", target=target)
    missing = [name for name in DEPENDENCIES if not ctx.which(name)]
    for module in ("sqlite3", "plistlib", "json", "ssl"):
        try:
            __import__(module)
        except ImportError:
            missing.append("python:" + module)
    if sys.version_info < (3, 9):
        missing.append("python>=3.9")
    if missing:
        raise UpdateError("missing_dependencies", "build dependencies are missing: " + ", ".join(missing), missing=missing)
    viewer_arguments = read_bundle_info(bundle).get("AGHViewerArguments")
    if not isinstance(viewer_arguments, list) or not all(isinstance(item, str) for item in viewer_arguments):
        raise UpdateError("invalid_bundle", "installed AGHViewerArguments is not a string list")
    identifier = target["revision"][:12] + "-" + secrets.token_hex(4)
    staging = cache / "staging" / identifier
    staging.mkdir(mode=0o700)
    try:
        return _build_candidate(ctx, cache, staging, identifier, target, meta, viewer_arguments)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise


def _step(ctx, argv, cwd, timeout, status, message):
    done = ctx.runner(argv, cwd=str(cwd), timeout=timeout)
    if done.returncode != 0:
        kind = "offline" if status == "fetch_failed" and (done.timed_out or NETWORK_ERROR.search(done.stderr)) else status
        raise UpdateError(kind, message, detail=scrub(done.stderr or done.stdout))
    return done.stdout.strip()


def _build_candidate(ctx, cache, staging, identifier, target, meta, viewer_arguments):
    source, app = staging / "src", staging / "Agent-Host.app"
    git = ["git", "-c", "credential.helper="]
    _step(ctx, git + ["init", "-q", str(source)], staging, 60, "fetch_failed", "cannot create the staging repository")
    _step(ctx, git + ["-C", str(source), "remote", "add", "origin", OFFICIAL_REPO], staging, 60, "fetch_failed", "cannot configure the official remote")
    depth = ["--depth", "1"] if target["channel"] == "stable" else []
    _step(ctx, git + ["-C", str(source), "fetch", "-q"] + depth + ["--no-tags", "origin", target["ref"]], staging, 600, "fetch_failed", "cannot fetch the selected ref")
    fetched = _step(ctx, git + ["-C", str(source), "rev-parse", "FETCH_HEAD^{commit}"], staging, 60, "fetch_failed", "cannot resolve the fetched ref")
    if fetched != target["revision"]:
        raise UpdateError("ref_moved", "the selected ref moved after it was checked; run check again", target=target)
    if target["channel"] == "dev":
        ancestry = ctx.runner(git + ["-C", str(source), "merge-base", "--is-ancestor", meta["revision"], target["revision"]], timeout=60)
        if ancestry.returncode != 0:
            raise UpdateError("unsupported_upgrade", "development target does not contain this build; a downgrade or divergent source is not applied")
    _step(ctx, git + ["-C", str(source), "checkout", "-q", "--detach", target["revision"]], staging, 300, "fetch_failed", "cannot check out the pinned commit")
    if _step(ctx, git + ["-C", str(source), "rev-parse", "HEAD"], staging, 60, "fetch_failed", "cannot verify the checkout") != target["revision"]:
        raise UpdateError("ref_moved", "checked-out commit differs from the pinned commit")
    _step(ctx, ["make", "build", "VERSION=" + target["version"]], source, 900, "build_failed", "Host build failed")
    _step(ctx, [ctx.python, "-B", str(source / "tools/macos-app/build.py"), "--output", str(app), "--version", target["version"],
                "--channel", target["channel"], "--revision", target["revision"]] + viewer_arguments, source, 1800, "build_failed", "app build failed")
    expect = {k: target[k] for k in ("version", "revision", "channel")}
    result = inspect_bundle(ctx, app, expect)
    if read_bundle_info(app).get("AGHViewerArguments") != viewer_arguments:
        raise UpdateError("invalid_candidate", "candidate did not preserve the viewer arguments")
    shutil.rmtree(source)
    candidate = dict(expect, path=str(app), content_digest=result["content_digest"])
    document = {"schema": 1, "candidate": candidate, "installed_revision": meta["revision"],
                "repository": OFFICIAL_REPO, "created": ctx.wall()}
    manifest_path = cache / "candidates" / (identifier + ".json")
    digest = write_json(manifest_path, document)
    return {"ok": True, "status": "prepared", "candidate": candidate, "installed": verdict_installed(meta),
            "cache_manifest": {"path": str(manifest_path), "digest": digest}, "requires_consent": target["channel"] == "dev",
            "message": "candidate built and verified; the installed app was not changed"}


def verdict_installed(meta):
    return {k: meta[k] for k in ("version", "revision", "channel")}


# ----------------------------------------------------------------- apply

HANDOFF_KEYS = {"schema", "install_bundle", "candidate", "owner_pid", "timestamp", "health_token", "allow_dev"}
CANDIDATE_KEYS = {"path", "version", "revision", "channel", "content_digest", "manifest_path", "manifest_digest"}


def _text(value, limit=1024):
    if not isinstance(value, str) or not value or len(value) > limit or "\0" in value:
        raise UpdateError("invalid_handoff", "handoff contains an invalid string")
    return value


def load_handoff(ctx: Context, cache: Path, path: Any) -> Dict[str, Any]:
    handoff_path = Path(_text(str(path)))
    if not handoff_path.is_absolute() or not inside(handoff_path.parent.resolve() / handoff_path.name, cache):
        raise UpdateError("invalid_handoff", "handoff must be a file inside the update cache")
    try:
        data = read_json_file(ctx, handoff_path, mask=0o077, missing_ok=False)
    except UpdateError:
        raise UpdateError("invalid_handoff", "handoff must be a private regular file owned by the current user")
    if data is None:
        raise UpdateError("invalid_handoff", "handoff is not valid JSON")
    required = HANDOFF_KEYS - {"allow_dev"}
    if not required <= set(data) <= HANDOFF_KEYS or data["schema"] != 1:
        raise UpdateError("invalid_handoff", "handoff has missing or unknown fields")
    candidate = data["candidate"]
    if not isinstance(candidate, dict) or set(candidate) != CANDIDATE_KEYS:
        raise UpdateError("invalid_handoff", "handoff candidate has missing or unknown fields")
    for key in CANDIDATE_KEYS:
        _text(candidate[key])
    _text(data["install_bundle"])
    if not isinstance(data["health_token"], str) or not TOKEN.match(data["health_token"]):
        raise UpdateError("invalid_handoff", "health_token must be 16-64 URL-safe characters")
    pid = data["owner_pid"]
    if type(pid) is not int or pid <= 1 or pid == ctx.pid:
        raise UpdateError("invalid_handoff", "owner_pid is invalid")
    stamp = data["timestamp"]
    if type(stamp) not in (int, float) or not -60 <= ctx.wall() - stamp <= 300:
        raise UpdateError("invalid_handoff", "handoff timestamp is stale or in the future")
    if type(data.get("allow_dev", False)) is not bool:
        raise UpdateError("invalid_handoff", "allow_dev must be boolean")
    for key in ("path", "manifest_path"):
        if not Path(candidate[key]).is_absolute():
            raise UpdateError("invalid_handoff", "candidate paths must be absolute")
    for key in ("content_digest", "manifest_digest"):
        if not re.match(r"^[0-9a-f]{64}$", candidate[key]):
            raise UpdateError("invalid_handoff", "candidate digest is malformed")
    return data


def validate_install(ctx: Context, bundle: Path, handoff: Dict[str, Any]) -> Path:
    if bundle.is_symlink() or not bundle.is_absolute():
        raise UpdateError("invalid_install", "installed bundle must be an absolute, non-symlink path")
    install = bundle.resolve()
    if install != Path(handoff["install_bundle"]).resolve() or install.suffix != ".app" or not install.is_dir():
        raise UpdateError("invalid_install", "installed bundle differs from the handoff or is not an .app directory")
    if os.lstat(install).st_uid != ctx.uid or not os.access(install, os.W_OK) or not os.access(install.parent, os.W_OK):
        raise UpdateError("invalid_install", "installed bundle and its parent must be owned by and writable by the current user")
    return install


def validate_candidate(ctx: Context, cache: Path, handoff: Dict[str, Any]) -> Dict[str, Any]:
    pinned = handoff["candidate"]
    app, manifest_path = Path(pinned["path"]), Path(pinned["manifest_path"])
    if app.is_symlink() or manifest_path.is_symlink() or not inside(app.resolve(), cache / "staging") or not inside(manifest_path.resolve(), cache / "candidates"):
        raise UpdateError("invalid_candidate", "candidate and manifest must be inside the update cache")
    try:
        raw = manifest_path.read_bytes()
        document = json.loads(raw)
    except (OSError, ValueError):
        raise UpdateError("invalid_candidate", "cache manifest is unreadable")
    if hashlib.sha256(raw).hexdigest() != pinned["manifest_digest"]:
        raise UpdateError("invalid_candidate", "cache manifest digest differs")
    installed_revision = read_manifest(Path(handoff["install_bundle"]))["revision"]
    if not isinstance(document, dict) or document.get("schema") != 1 or document.get("repository") != OFFICIAL_REPO or document.get("installed_revision") != installed_revision:
        raise UpdateError("invalid_candidate", "candidate was not prepared for this installed revision")
    recorded = document.get("candidate") if isinstance(document, dict) else None
    if not isinstance(recorded, dict) or any(recorded.get(k) != pinned[k] for k in ("path", "version", "revision", "channel", "content_digest")):
        raise UpdateError("invalid_candidate", "handoff candidate differs from the cache manifest")
    return inspect_bundle(ctx, app.resolve(), {k: pinned[k] for k in ("version", "revision", "channel", "content_digest")})


def write_transaction(cache: Path, state: str, **fields: Any) -> None:
    write_json(cache / "transaction.json", dict(fields, schema=1, state=state))


def apply_update(ctx: Context, bundle: Path, cache: Path, handoff_path: Any) -> Dict[str, Any]:
    token = None
    try:
        handoff = load_handoff(ctx, cache, handoff_path)
        token = handoff["health_token"]
        with updater_lock(cache):
            result = _apply(ctx, bundle, cache, handoff)
    except UpdateError as error:
        result = error_result(error)
    if token:
        with contextlib.suppress(OSError):
            write_json(cache / ("apply-result-" + token + ".json"), result)
    return result


def _apply(ctx, bundle, cache, handoff):
    token, pinned = handoff["health_token"], handoff["candidate"]
    marker = read_json_file(ctx, cache / "transaction.json")
    if marker is not None and marker.get("state") in ACTIVE_STATES:
        raise UpdateError("interrupted_transaction", "a previous update was interrupted; run recover first")
    install = validate_install(ctx, bundle, handoff)
    installed = read_manifest(install)
    problem = installability(ctx, installed)
    if problem:
        raise problem
    result = validate_candidate(ctx, cache, handoff)
    candidate = result["manifest"]
    if "dev" in (installed["channel"], candidate["channel"]) and not handoff.get("allow_dev", False):
        raise UpdateError("consent_required", "development builds are never updated without allow_dev")
    if candidate["channel"] != installed["channel"] or result["info"].get("AGHViewerArguments") != read_bundle_info(install).get("AGHViewerArguments"):
        raise UpdateError("invalid_candidate", "candidate channel or viewer arguments differ from the installed app")
    if candidate["channel"] == "stable":
        if _version_tuple(candidate["version"]) <= _version_tuple(installed["version"]):
            raise UpdateError("invalid_candidate", "candidate is not newer than the installed version")
    elif candidate["revision"] == installed["revision"]:
        raise UpdateError("invalid_candidate", "candidate has the installed revision")
    for name in ("started-", "health-"):
        if os.path.lexists(cache / (name + token + ".json")):
            raise UpdateError("invalid_handoff", "health token was already used")
    executable = executable_of(install)
    write_json(cache / ("ready-" + token + ".json"), {"pid": ctx.pid, "owner_pid": handoff["owner_pid"], "revision": candidate["revision"]})
    _await_owner(ctx, handoff["owner_pid"], executable)
    if processes_running(ctx, executable):
        raise UpdateError("other_instances_running", "another instance of the installed app is running")
    names = candidate_names(install, token)
    for path in names.values():
        if os.path.lexists(path):
            raise UpdateError("stage_failed", "a staging path already exists next to the installed app")
    done = ctx.runner(["ditto", pinned["path"], str(names["new"])], timeout=300)
    try:
        if done.returncode != 0:
            raise UpdateError("stage_failed", "cannot stage the candidate next to the installed app", detail=scrub(done.stderr))
        staged = inspect_bundle(ctx, names["new"], {k: pinned[k] for k in ("version", "revision", "channel", "content_digest")})
        del staged
    except BaseException:
        _remove_own(names["new"], install)
        raise
    base = {"token": token, "install": str(install), "old_revision": installed["revision"], "new_revision": candidate["revision"], "started": ctx.wall()}
    write_transaction(cache, "switching", **base)
    try:
        os.rename(install, names["previous"])
        try:
            os.rename(names["new"], install)
        except OSError:
            os.rename(names["previous"], install)
            raise
    except OSError:
        _remove_own(names["new"], install)
        outcome = _restore(ctx, cache, install, token, base)
        raise UpdateError("manual_recovery_required" if outcome["status"] == "manual_recovery_required" else "switch_failed", "could not switch bundles")
    write_transaction(cache, "switched", **base)
    summary = {"install_bundle": str(install), "previous_bundle": str(names["previous"]), "version": candidate["version"],
               "revision": candidate["revision"], "channel": candidate["channel"]}
    launch = ctx.runner(["open", "-g", "-n", str(install), "--args", "--agh-update-health", token], timeout=30)
    failure = None
    if launch.returncode != 0:
        failure = ("launch_failed", "the updated app could not be launched")
    else:
        write_transaction(cache, "launched", **base)
        failure = _await_health(ctx, cache, install, executable, token, candidate["revision"])
    if failure is None:
        write_transaction(cache, "committed", previous=str(names["previous"]), **base)
        return dict(summary, ok=True, status="updated", message="updated and healthy; previous bundle retained")
    return _fail_after_switch(ctx, cache, install, executable, token, base, summary, failure)


def _remove_own(path: Path, install: Path) -> None:
    if path.parent == install.parent and path.name.startswith(".") and not path.is_symlink() and path.is_dir():
        shutil.rmtree(path, ignore_errors=True)


def _owner_state(ctx, pid):
    done = ctx.runner(["ps", "-p", str(pid), "-o", "stat=,comm="], timeout=15)
    parts = done.stdout.strip().split(None, 1) if done.returncode == 0 else []
    if len(parts) < 2 or parts[0].startswith("Z"):
        return None
    return os.path.realpath(parts[1].strip()) if parts[1].strip().startswith("/") else parts[1].strip()


def _await_owner(ctx, pid, executable):
    wanted = os.path.realpath(str(executable))
    current = _owner_state(ctx, pid)
    if current is not None and current != wanted:
        raise UpdateError("owner_mismatch", "owner_pid is not the installed app")
    deadline = ctx.monotonic() + ctx.owner_wait
    while _owner_state(ctx, pid) == wanted:
        if ctx.monotonic() >= deadline:
            raise UpdateError("owner_timeout", "the running app did not exit in time; nothing was changed")
        ctx.sleep(ctx.poll)


def _valid_pid(value):
    return type(value) is int and 1 < value < 4194304


def _await_health(ctx, cache, install, executable, token, revision):
    deadline, started_pid = ctx.monotonic() + ctx.health_wait, None
    while True:
        try:
            started = read_json_file(ctx, cache / ("started-" + token + ".json"))
            if started is not None and started_pid is None:
                started_pid = started.get("pid")
                if not _valid_pid(started_pid):
                    return ("health_invalid", "start marker has no valid pid")
            health = read_json_file(ctx, cache / ("health-" + token + ".json"))
        except UpdateError:
            return ("health_invalid", "start or health marker is unsafe")
        if health is not None:
            pid = health.get("pid")
            if health.get("revision") != revision or not _valid_pid(pid) or (started_pid and pid != started_pid):
                return ("health_invalid", "health report does not match the candidate")
            try:
                if pid in processes_running(ctx, executable):
                    return None
            except UpdateError:
                return ("health_invalid", "cannot verify the reporting process")
            return ("health_invalid", "health report is not from the installed candidate process")
        if ctx.monotonic() >= deadline:
            return ("health_timeout", "the updated app did not report healthy within the time limit")
        ctx.sleep(ctx.poll)


def _stop_candidate(ctx, executable, started_pid):
    """TERM only the recorded candidate pid; return processes still running the candidate path."""
    try:
        live = processes_running(ctx, executable)
        if _valid_pid(started_pid) and started_pid in live:
            ctx.kill(started_pid, signal.SIGTERM)
            deadline = ctx.monotonic() + ctx.term_wait
            while started_pid in processes_running(ctx, executable) and ctx.monotonic() < deadline:
                ctx.sleep(ctx.poll)
        return processes_running(ctx, executable)
    except (UpdateError, OSError):
        return [-1]


def _fail_after_switch(ctx, cache, install, executable, token, base, summary, failure):
    reason, message = failure
    started = None
    with contextlib.suppress(UpdateError):
        started = (read_json_file(ctx, cache / ("started-" + token + ".json")) or {}).get("pid")
    if _stop_candidate(ctx, executable, started):
        write_transaction(cache, "manual_recovery", reason=reason, **base)
        return dict(summary, ok=False, status="manual_recovery_required", reason=reason,
                    message="the candidate may still be running; the previous bundle is retained, quit the app and run recover")
    outcome = _restore(ctx, cache, install, token, base)
    if outcome["status"] == "rolled_back":
        relaunch = ctx.runner(["open", "-n", str(install)], timeout=30)
        return dict(summary, ok=False, status="rolled_back", reason=reason, message=message + "; previous app restored",
                    previous_relaunched=relaunch.returncode == 0)
    return dict(summary, ok=False, status="manual_recovery_required", reason=reason, message="rollback failed; previous bundle is retained next to the app")


def _restore(ctx, cache, install, token, base):
    """Return the known previous bundle to the install path. Never deletes the failed candidate."""
    names = candidate_names(install, token)
    exists = os.path.lexists
    try:
        if exists(names["previous"]):
            if exists(install):
                if processes_running(ctx, executable_of_failed(install)):
                    write_transaction(cache, "manual_recovery", **base)
                    return {"status": "manual_recovery_required"}
                os.rename(install, names["failed"])
                try:
                    os.rename(names["previous"], install)
                except OSError:
                    os.rename(names["failed"], install)
                    raise
            else:
                os.rename(names["previous"], install)
        elif not exists(install):
            write_transaction(cache, "manual_recovery", **base)
            return {"status": "manual_recovery_required"}
        _remove_own(names["new"], install)
    except (OSError, UpdateError):
        write_transaction(cache, "manual_recovery", **base)
        return {"status": "manual_recovery_required"}
    write_transaction(cache, "rolled_back", **base)
    return {"status": "rolled_back"}


def executable_of_failed(install: Path) -> Path:
    return install / "Contents/MacOS/Agent-Host"


def recover(ctx: Context, bundle: Path, cache: Path) -> Dict[str, Any]:
    try:
        with updater_lock(cache):
            marker = read_json_file(ctx, cache / "transaction.json")
            if marker is None or marker.get("state") not in ACTIVE_STATES:
                return {"ok": True, "status": "nothing_to_recover", "message": "no interrupted update transaction"}
            token, base = marker.get("token"), {k: marker.get(k) for k in ("token", "install", "old_revision", "new_revision", "started")}
            if not isinstance(token, str) or not TOKEN.match(token) or bundle.is_symlink() or Path(str(marker.get("install"))) != bundle.resolve():
                raise UpdateError("invalid_transaction", "transaction does not belong to this bundle")
            install = bundle.resolve()
            if install.parent == install or not os.access(install.parent, os.W_OK):
                raise UpdateError("invalid_install", "install location is not writable")
            outcome = _restore(ctx, cache, install, token, base)
            if outcome["status"] == "rolled_back":
                return {"ok": True, "status": "recovered", "message": "previous app restored"}
            return {"ok": False, "status": "manual_recovery_required",
                    "message": "quit any running Agent-Host app, then run recover again; previous bundle is retained"}
    except UpdateError as error:
        return error_result(error)


# ------------------------------------------------------------------- CLI

def main(argv: Optional[List[str]] = None, ctx: Optional[Context] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--cache", default=str(Path.home() / "Library/Caches/Agent-Host/updates"))
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("check")
    prep = commands.add_parser("prepare")
    prep.add_argument("--allow-dev", action="store_true")
    prep.add_argument("--revision")
    commands.add_parser("recover")
    commands.add_parser("apply").add_argument("--handoff", required=True)
    args = parser.parse_args(argv)
    ctx = ctx or Context()
    try:
        bundle = Path(args.bundle)
        if not bundle.is_absolute():
            raise UpdateError("invalid_install", "--bundle must be an absolute path")
        if args.command == "check":
            result = check(ctx, bundle)
        else:
            cache = ensure_cache(ctx, args.cache)
            if args.command == "prepare":
                if args.revision and not SHA.match(args.revision):
                    raise UpdateError("invalid_argument", "--revision must be a full lowercase commit")
                result = prepare(ctx, bundle, cache, args.allow_dev, args.revision)
            elif args.command == "apply":
                result = apply_update(ctx, bundle, cache, args.handoff)
            else:
                result = recover(ctx, bundle, cache)
    except UpdateError as error:
        result = error_result(error)
    except Exception as error:
        result = {"ok": False, "status": "internal_error", "message": type(error).__name__}
    print(json.dumps(result, sort_keys=True))
    return 0 if result.get("ok") else 1


if __name__ == "__main__":
    sys.exit(main())
