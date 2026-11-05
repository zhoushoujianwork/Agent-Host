# macOS app source updates: details

[Summary](mac-app-updates.md). `AppUpdates.swift` owns the native schedule, consent
and idle handoff; `update.py` builds and installs source-derived candidates.
`NativeBridge.swift` persists drafts and coordinates the viewer restart gate.
Checks run after launch and every 24 hours plus up to 30 minutes of jitter; a
failed check is retried no sooner than 30 minutes. There is one shared controller
for scheduled and manual checks. Development preparation always requires an
explicit Update action, and temporary stable deferral lasts 24 hours.

## Build metadata

`Info.plist` key `AGHBuildMetadata` has exactly these fields: `version`, `revision`
(full 40-hex commit or `unknown`), `channel` (`stable` or `dev`), `dirty` (bool),
`architecture`, `min_os`, `repository`, `development_ref` (default `main`).
`CFBundleShortVersionString` equals `version`.

Defaults when no flags are given: the revision is `git rev-parse HEAD`. The build is
`stable` only when the checkout is clean, HEAD has an exact `vX.Y.Z` tag, and the
built Host reports that same version. Anything else is `dev` with the Host's version.
`make macos-app VERSION=X.Y.Z` is therefore needed for a stable local build.
`build.py` runs `bin/agent-host --version` and exits if its version, revision or dirty
state differs from the app metadata. `--version` and `--channel` must be given
together; `--output` must be a new absolute `.app` path. The bundle also contains
`Resources/tools/macos-app/{update.py,build.py,connection.py,runtime.py}`.

## Commands and results

`--cache` defaults to `~/Library/Caches/Agent-Host/updates`. It must be a directory
owned by the user with mode 0700 (created if missing).

`check` results (`status`): `available` (with `target` {version, revision, channel,
ref}, `automatic_allowed`, `requires_consent`), `up_to_date`, `no_published_version`
(these three have `ok: true`), `offline`, `remote_error`, `incompatible` (other
repository, architecture, older macOS, unknown revision or missing metadata),
`not_updatable` (dirty), `invalid_bundle`. All include `installed`.

`prepare` fetches stable `target.ref` at depth 1 (dev fetches ancestry) into `cache/staging/ID/src`, requires
`FETCH_HEAD` to equal the revision `check` returned (otherwise `ref_moved`), checks
out that commit, runs `make build VERSION=<version>`, then runs the checkout's own
`build.py --output ... --version --channel --revision` with the installed app's
`AGHViewerArguments`. Required tools: git, make, go, swiftc, codesign
(`missing_dependencies` lists them) and Python 3.9+ with sqlite3 and ssl. The result
is verified before it is reported: metadata, code signature (`codesign --verify
--deep --strict`), bundled `agent-host --version` (version, revision, clean) and a
content digest. The source tree is removed. Result on success:

```json
{"ok": true, "status": "prepared",
 "candidate": {"path": ".../staging/ID/Agent-Host.app", "version": "1.1.0",
               "revision": "<sha>", "channel": "stable", "content_digest": "<sha256>"},
 "cache_manifest": {"path": ".../candidates/ID.json", "digest": "<sha256 of the file>"},
 "installed": {...}, "requires_consent": false}
```

`--revision SHA` makes `prepare` fail with `ref_moved` if the current target differs
from the revision the user approved. A dev target without `--allow-dev` returns
`consent_required`. A dev candidate whose history does not contain the installed
revision returns `unsupported_upgrade`; merely differing from the installed hash
is not proof of a forward update.

## Apply contract

The parent, after quiescing the viewer and persisting drafts, writes
`cache/<any>.json` (mode 0600, regular file, same user, inside the cache) with:
`schema: 1`, `install_bundle`, `candidate` (the `prepare` candidate fields plus
`manifest_path`, `manifest_digest`), `owner_pid`, `timestamp` (epoch seconds, within
-60s/+300s), `health_token` (16-64 chars of `A-Za-z0-9_-`, single use) and optional
`allow_dev`. Then it starts `update.py apply --handoff FILE` detached. After
validation the helper writes `cache/ready-TOKEN.json` with its pid, the owner pid
and candidate revision. The app waits at most 25 seconds for that matching
acknowledgement before quitting. A failure leaves the current app open and
resumes viewer mutations.
The helper's result is also written to `cache/apply-result-<token>.json`.

The helper takes the exclusive `cache/update.lock`, refuses unknown fields, symlinks,
a non-writable or foreign-owned install, a candidate or manifest outside the cache,
and any pin mismatch (including the installed revision used at preparation, manifest digest, version, revision, channel, content digest,
codesign, Host `--version`). The channel and viewer arguments must equal the installed
app's; stable must be newer, dev must differ in revision; dev needs `allow_dev`.
It then waits up to 60s for `owner_pid` to exit (that pid must be the installed
executable), refuses if another instance of the installed app still runs, copies the
candidate with `ditto` to a sibling on the same volume and re-verifies it.
The switch is `os.rename` of install to `.Agent-Host.previous-<token>` and the staged
copy to install, with `cache/transaction.json` set to `switching`, `switched`,
`launched`, then `committed`. Launch is
`open -g -n INSTALL --args --agh-update-health TOKEN`. Automatic relaunch stays
in the background; an explicitly requested update restores Workspace.

The app must write, with mode 0600:

- `cache/started-TOKEN.json` `{"pid": N}` immediately at start.
- `cache/health-TOKEN.json` `{"pid": N, "revision": "<new revision>"}` once its own
  `/api/chat` and `/api/records` respond and the workspace document has loaded its
  draft-persistence function.

The pid must equal the started pid, and be a running process whose executable is the
installed candidate path. Without health in 60s the helper sends SIGTERM only to the
pid in the start marker, and only if its executable is the candidate path, waits up to
10s, then restores the previous bundle (the failed one is kept as `.failed-<token>`)
and reopens it. If the start marker is missing or the process will not stop, it
returns `manual_recovery_required`, keeps the previous bundle and kills nothing.

Apply statuses: `updated`, `rolled_back` (with `reason`: `health_timeout`,
`health_invalid`, `launch_failed`), `manual_recovery_required`, `switch_failed`,
`owner_timeout`, `owner_mismatch`, `other_instances_running`, `consent_required`,
`interrupted_transaction`, `locked`, `invalid_handoff`, `invalid_install`,
`invalid_candidate`.

## Recovery

If `transaction.json` is in `switching`, `switched`, `launched` or `manual_recovery`,
`apply` refuses with `interrupted_transaction`. `recover` restores the known previous
bundle when no process runs the candidate executable; otherwise it reports
`manual_recovery_required` and changes nothing. It only renames the install and its
own `.previous`/`.failed` siblings; it deletes no other directory. Previous and failed
bundles and old candidates are retained; the updater has no pruning.

## Limits

Same-user tampering is out of scope; accidental mismatches, symlinks, other
repositories and dirty builds fail closed. Error text drops URL credentials. The
candidate is ad-hoc signed, so the code-signature check detects corruption, not origin.
Process identity comes from `ps` executable paths, not an audit token.
Headless tests: `tools/macos-app/update_test.py` (run by `check.py` and `make check`)
use fake bundles, a scripted process runner and a fake clock. No real remote,
install or relaunch has been exercised.

## Helper commands

```sh
python3 -B update.py --bundle /abs/Agent-Host.app [--cache DIR] check
python3 -B update.py --bundle /abs/Agent-Host.app prepare [--allow-dev] [--revision SHA]
python3 -B update.py --bundle /abs/Agent-Host.app apply --handoff /abs/cache/handoff.json
python3 -B update.py --bundle /abs/Agent-Host.app recover
```

Every command prints one JSON object and exits 0 only when `ok` is true.
