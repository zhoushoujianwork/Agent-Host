# Runtime implementation and migration details

## Foreground restart through make run

`make run` supplies `service run --restart` for both `CONFIG_DIR` and explicit
`RUNTIME_ROOT`. Direct service starts remain fail-on-duplicate unless they also
request `--restart`. Internal worker mode cannot request a managed restart.

The supervisor checks every enabled configuration and reads the source viewer
assets before stopping an existing instance. A busy `run.lock` triggers inspection of
the private `service.pid`: the live process must have the current user, matching
Host executable and exact runtime `--root`. When its parent is the matching Host
supervisor, SIGTERM goes to that parent so all workers and the workbench stop
normally. Older standalone runners fall back to stopping the verified worker.
An unrelated, missing or unverifiable PID is never signaled.

The new process waits for the existing flock to become available, bounded by
30 seconds and caller cancellation. Lock release follows worker/workbench cleanup;
PID-file existence alone does not establish completion. Locks are retained through
startup, so concurrent invocations cannot create duplicate runtime owners. No
lock files are deleted and no forced SIGKILL or task replay is added.

Regression tests use isolated subprocesses to verify a successful replacement,
normal old-supervisor exit, invalid-preflight preservation, duplicate direct starts,
state-file preservation, unrelated-PID rejection and bounded lock waiting.
Production startup verification is recorded separately from these offline tests.

On 2026-10-15, the 15 focused startup/restart tests and
`make check GOFLAGS=-p=1` passed. An isolated source snapshot containing only
this task's changes was built into the project binary. Two successive
`make -o build run RUN_ARGS=--no-browser` invocations replaced the live supervisor
without manual stopping; `-o build` reused that already verified binary to avoid
including concurrent workspace changes. Each old runner exited with status 0,
each worker PID changed, both channels produced fresh connection events, and the
workbench returned HTTP 200. Private evidence is retained in
`.agent-host/acceptance/make-restart-20261015T030705Z/`.

The [main guide](service-install.md) defines the current project-local workflow.
The old system installation instructions below are retained as historical
reference for prior evidence; `make install` is no longer a Make target.

## Unified configuration runtime

The current make run default scans ROOT/config/channels.*.json and
datasource.plugins.*.json.
See [the current configuration details](configuration-layout-detail.md) for the
loader contract, bot/DWS optional branches, migration and live connection evidence.
Use `python3 -B tools/service/migrate_config.py --root ABSOLUTE_PATH` only for the
known stopped paired deployment; it refuses running locks, existing destinations
and discovery configurations needing an explicit mapping. Failures after backup
restore the old layout. The migration report retains database hashes and counts.

### File-memory runtime, 2026-10-13

Explicit `memory migrate` creates versioned documents/Skills and a private
state/file-memory.json receipt for each stopped state home. `memory verify`
checks authoritative publication and rebuilds stale or missing cache indexes.
`host.json` may explicitly select `memory.backend` as `files`; startup rejects
missing/invalid migration receipts. Selecting `sqlite` requires explicit rollback
when a receipt exists, preserving file-era changes before switching.
Commands, recovery boundaries and history comparison are in the
[file-memory details](file-memory-design-detail.md#迁移命令与回退).

The local DingTalk, Feishu and CLI state homes have been migrated with distinct
sources and shared memory/cache roots. Both channel workers reconnected, and the
workbench memory API returned 3 memories, 1 candidate and 2 channels. Old channel
memory/revision/evidence and original derived tables remain identical to the
stopped backup. The final isolated history receipt is
`.agent-host/validation/file-memory-history-20261013-final/report.json`; cutover
backup is `.agent-host/backups/file-memory-cutover-20261013.tar.gz.age`
(an encrypted directory archive for manual historical recovery).

The default Agent now uses user-maintained AGENTS/SOUL/IDENTITY through
instructions_dir, preserving the prior CLAUDE.md role text. No owner USER was
automatically bound. The 14 owner_notes source documents moved into their
local-documents plugin snapshot with unchanged hashes and grants. Configuration
remains runtime-read-only; generated context and native packages live in cache.

The project-local sections below describe the prior layout and retained evidence.

## Previous project-local runtime

`make run` builds the checkout binary and invokes `agent-host service run`
directly. The Go CLI scans `.agent-host/channels/channel-*.json` for registrations with
`enabled: true` and a `runtime_root`, resolving relative paths against the
registration directory. `CHANNELS_DIR` selects another registration directory;
an explicit `RUNTIME_ROOT` bypasses scanning and selects one runtime.
`RUN_ARGS=--no-browser` suppresses browser launch. Without a registration
directory, startup retains `.agent-host/runtime` as the legacy default.

The CLI validates all enabled runtimes and acquires their exclusive `run.lock`
locks before connecting. It copies tracked viewer assets with private atomic
replacement, then starts one Go worker process per runtime. Child processes
isolate process-wide provider environment variables and native profiles.
The supervisor starts one workbench for all enabled channels at fixed loopback
port 8765 (`--viewer-port` overrides it). Workers use `--no-workbench` and do not
start separate Web processes. The workbench reads each channel's state separately
and qualifies displayed record/memory links with its registration name.
Workers relay stdout/stderr to the
terminal and their private append-only logs, and record `service.pid`.
The supervisor forwards cancellation with SIGTERM, waits for cleanup, removes
its worker PID files, and releases locks. Unexpected worker exit fails the
supervisor and stops peers; it never replays tasks or restarts silently.
The retained Python single-runtime runner is compatibility tooling only;
it is not invoked by `make run`. Python serves the workbench projection and
forwards explicit memory actions to the corresponding running Host.
The Host owns graceful channel, native execution and viewer shutdown. No launchd
registration, startup-at-login, automatic crash restart or shell daemon is added.
The legacy installer remains available for historical release tooling, but is
not part of the checkout's `make run` workflow.

| Project-local path | Purpose |
| --- | --- |
| `bin/agent-host` | Binary built from the checkout |
| `.agent-host/channels/channel-*.json` | Private enabled-channel registrations |
| `.agent-host/runtime/config` | Current Feishu service/provider/channel configuration |
| `.agent-host/runtime/state` | Feishu task/history/memory database and input artifacts |
| `.agent-host/runtime/native` | Dedicated native profiles and sessions |
| `.agent-host/runtime/workspace` | Default task workspace |
| `.agent-host/runtime/logs` | Service and viewer logs |
| `.agent-host/runtime/viewer` | Private copy of viewer assets |
| `.agent-host/email` | Shared mailbox registry, private credentials and operation ledger |
| `.agent-host/dingtalk` | Independently configured DingTalk runtime |
| `.agent-host/legacy-home` | Preserved remaining former home-directory data and evidence |
| `.agent-host/cli-state` | Default state for ordinary local CLI invocations |

The existing root `.gitignore` rule excludes the complete `.agent-host/` tree.
Registrations require the `channel-` filename prefix. Other classified JSON files
(`data-`, `component-`, `credential-`) are ignored by the channel scanner;
unclassified JSON files fail explicitly. Runtime configuration prefers
`component-service.json`, `component-environment.json`, and `channel-<protocol>.json`.
Legacy names remain compatible when there is no canonical file; coexistence is
an error rather than implicit precedence. Explicit `agents_config`,
`knowledge_config`, `discovery_config`, and `email_config` paths use the names
listed in the [configuration naming guide](../config/README.md).
The CLI requires current-user ownership and private regular files, rejects
symlink runtime paths, and does not write credential values to commands. It uses
`python3` for the viewer unless `--viewer-python` is explicitly supplied.

Go regression tests cover enabled and disabled registrations, relative paths,
duplicate roots, malformed/private/symlink configuration, legacy fallback,
preflight failure before any connection, runtime-lock release, concurrent worker
startup, cancellation, peer failure and PID cleanup.

On 2026-10-13, the prior email deployment mismatch was traced to two separate
services: DingTalk production had email configured, while the Feishu lab still
used an older binary, no email registry and `no_tools: true`. An actual Host CLI
turn using the Feishu model/provider configuration completed the owner's original
mailbox query with email tools. No outgoing mail or test channel message was sent.

The later project-local migration stopped both old services, unloaded and disabled
`com.agent-host.production`, removed its LaunchAgent plist and moved the former
home `.agent-host` tree into the ignored project area. Feishu, DingTalk and email
subtrees became the paths listed above. Absolute live configuration references
were updated; immutable historical evidence retains original recorded paths.
Stopped Feishu and DingTalk database SHA-256 values matched before and after the
move. The remaining legacy files and former plist were preserved locally for
rollback; no state database was initialized in their place. Changed workspace
paths start new native sessions. Historical unregistered workspace scopes remain
recorded under their original paths; registered projects keep their stable scopes.

Forward migration and rollback must stop writers first. Rollback restores private
configuration references and the corresponding preserved runtime; do not run both
versions concurrently against one state directory. Restoring the archived plist
is an explicit operator action, not automatic fallback.

Validation covers runner preflight, private viewer preparation, retained state,
visible log relay, duplicate-run rejection, signal propagation and child cleanup.
`make check` passed after the change. Real `make run` startup on 2026-10-13
produced fresh meeting-entry and Feishu channel connections plus a loopback
workbench URL. A separate Host CLI turn with the migrated runtime configuration
completed the original mailbox query; successful paired results were observed
for `mcp__email__list_accounts` and `mcp__email__search_messages`. No outgoing mail
or new test channel message was sent. These receipts are stored privately under
`.agent-host/runtime/evidence/project-run-mail`; they do not establish a new
Feishu user-message round trip.

## Legacy system installation reference

## Layout and configuration

| Production path under `~/.agent-host/production` | Purpose |
| --- | --- |
| `bin/agent-host` | Installed executable, atomically replaced |
| `config/service.json` | Fixed native executable, model, Jev selection, deadline and tool policy |
| `config/environment.json` | Private provider environment values |
| `config/dingtalk.json`, `config/dingtalk.secret` | Owner/bot identity and application secret |
| `config/agents.json`, `config/CLAUDE.md` (optional) | Instance definitions and explicitly loaded service response instructions |
| `config/knowledge.json`, provider token files (optional) | Private remote/local source declarations and remote credentials |
| `config/discovery.json` (optional) | Fixed DWS profile, selected conversations, registered projects and bounded polling/extraction limits |
| `state` | Production SQLite database, native-run linkage and exclusive writer lock |
| `state/attachments` | Private downloaded task inputs, with source-linked content metadata |
| `workspace` | Native working directory and production memory scope |
| `native` | Enforced `CLAUDE_CONFIG_DIR`, isolated native sessions/settings |
| `logs/service.ndjson`, `logs/service.stderr.log` | Structured output and terminal errors |
| `installation.json` | Receipt of the last successful installation, not live health |

Only OS registration metadata lives outside this hidden root:
`~/Library/LaunchAgents/com.agent-host.production.plist`. The plist contains
paths and a fixed argument vector, with no shell command or credentials. Native
Claude itself remains a separately installed dependency at the configured
absolute path. No checkout runtime file is used by the installed service.

`service.json` requires `claude`, `model`, `jev_provider` (`typesafe` or
`custom`), `jev_model` and `timeout`; `custom` also requires `jev_endpoint`. Optional `no_tools` and `allowed_tools` retain existing native
permission behavior; they cannot be combined. Optional `knowledge_config` is
an absolute path to the private [knowledge configuration](knowledge-sources.md).
Its source project and optional audience lists select the MCP instances for each
turn. An Agent source subset can narrow these grants. Local Markdown uses explicit
root/path declarations without provider credentials. Its source root is separate
from the execution workspace and native directory policy. The
[local-source details](local-knowledge-detail.md) cover owner-only retained
snapshots and provenance. Configured native allow rules must authorize their use
in unattended tasks. Model/provider selection is explicit, without fallback.
Optional `discovery_config` is an absolute path to the owner-private
[DWS discovery configuration](conversation-history-detail.md#dws-configuration-and-operation).
Preflight checks its local shape and existing project registrations without
calling DWS or Claude. The system collector runs independently of channel tasks
and emits compact `discovery.cycle` coverage/failure events. Omission disables
it. Local topic acceptance exports owner-private citations; it creates no task
or memory. History MCP mounts automatically for eligible DingTalk tasks and is
excluded from tool-free execution.
`environment.json` accepts only string values for
`TYPESAFE_API_KEY`, `JEV_API_KEY`, `ANTHROPIC_BASE_URL`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`,
`CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK` and `CLAUDE_CODE_MAX_OUTPUT_TOKENS`.
Configure only the selected providers' entries. Native non-streaming recovery is
disabled by default. An explicitly selected gateway may opt in with
`CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK="0"`. Omitting
`CLAUDE_CODE_MAX_OUTPUT_TOKENS` retains Claude's native output budget; an optional
value must be a canonical positive integer. There is no Host-imposed 8,192-token
ceiling. Both settings are applied to the child environment and native invocation
settings. A configured output limit applies to each native model response,
including thinking tokens; it is not a total task budget. Recovery belongs to
Claude's existing model loop and retains the provider, model and observed tool
results. Gateway support is verified independently; opt-in does not imply that
every gateway accepts a large non-streaming request.

The production `cc` selection resolves the shell alias to its executable,
`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_MODEL`. The executable
and exact model go in `service.json`; the gateway and credential go in
`environment.json`. The daemon uses these explicit values with its dedicated
native profiles and configured Host tools. Shell echo commands, personal hooks
and conversation state are not imported. The current selection is
`http://gateway.example.invalid/api` and `claude-sonnet-5[1m]`, with native recovery
enabled and no explicit output cap. Jev retains its separately configured provider
and credential. These values are a deployment snapshot; later alias changes
require updating the private production configuration and restarting.

Optional `max_concurrent_conversations` bounds active conversation turns to
1–32; omission or zero uses four. The foreground equivalent is
`dingtalk serve --max-concurrent-conversations N`. Each authenticated conversation
and shared agent/audience partition remains FIFO through native work, synchronous
memory processing and reply delivery. Different audiences can run concurrently;
shared project files are not isolated. Configuration changes require restart.
The [conversation/input details](channel-conversations-detail.md) describe limits,
private attachment storage and the absence of an immediate `/status` or `/stop`.

`agent-host service check|run --root ABSOLUTE_PATH` is the installed entry point.
It requires private, current-user-owned configuration files and distinct fixed
state/workspace/native directories. It rejects symbolic links in managed paths,
unknown/duplicate configuration fields and oversized JSON. Native executable
symlinks are allowed. Provider credentials and backend-selection variables
inherited from development are cleared before applying the private environment;
`CLAUDE_CONFIG_DIR` is fixed to the managed native directory. The original
process environment is restored when the command returns.

The local checker opens no network connection, executes no Claude turn, and
creates no database. It confirms configuration shape and executable availability,
not provider authentication. The installer creates the directory layout; it
does not discover/copy user credentials automatically or import test data.

Project registration remains optional and local. Each new channel task uses a
reliably associated project set or the configured agent's default workspace;
an empty catalog skips the project provider entirely. The native assistant owns
business follow-up questions. No new Host project waiting record is created,
and old pending records are never automatically replayed. Default execution
does not mount additional projects or sources. Configuration, selected-directory
and provider failures remain explicit errors.

## Startup workbench

The installer copies the eight runtime viewer assets into `ROOT/viewer` with
owner-only permissions. The LaunchAgent passes the installer's Python executable
through `--viewer-python`, so startup does not rely on checkout files or a shell's
Python search path. `service check` does not launch a viewer or browser.

After local configuration validation, `service run` starts that viewer before
loading managed provider credentials, with `--home ROOT/state`, no archived
report import, and the configured fixed loopback port. It waits up to five seconds for the
viewer URL, prints execution and memory URLs on stderr, then uses macOS `open`
to request the default browser. `--no-browser` suppresses that request while
leaving observation available. Ordinary channel reconnects do not reopen tabs.

Service cancellation stops and reaps the viewer child. Viewer startup or browser
failures are reported separately without stopping channel execution; viewer
runtime errors remain in `ROOT/logs/viewer.log`. The service's structured event
journal remains separate. The current owner-local memory controls use a private
socket per channel Host, while Python retains read-only SQLite access. There is
no remote listener or task submission; source and request checks are documented
in the [workbench details](../tools/run-viewer/README-detail.md#owner-local-memory-controls).

Regression tests start the installed viewer assets against isolated schema-13
state, verify both pages and the memory API, check default/disabled/failed browser
requests, and prove that cancellation closes the listener. Installer checks
verify asset preservation and private permissions before launch.

## Response-instruction configuration

`CLAUDE_CONFIG_DIR` separates native settings and sessions, but Claude can still
discover `CLAUDE.md`, `CLAUDE.local.md` and rules through the execution directory's
ancestors or project imports. A separate profile alone does not prevent personal
instructions from entering a service turn.

Use the existing `instructions_file` field to give the service its own role.
Add an absolute `agents_config` reference to `config/service.json`, then point
the applicable agent in `config/agents.json` at a private instruction file:

```json
{
  "default_agent": "default",
  "agents": [
    {
      "id": "default",
      "name": "General assistant",
      "instructions_file": "/absolute/home/.agent-host/production/config/CLAUDE.md"
    }
  ]
}
```

Preserve existing agent definitions and group bindings when adding the field.
The Host reads this file at startup and appends its contents to trusted channel
instructions for every audience using that agent; its filename does not rely on
native `CLAUDE.md` discovery. Keep the existing business role and add a short
response preference, for example:

```markdown
Respond in the language used by the user unless they request another language.
Give English-learning tips or other language coaching only when the current
request asks for it.
```

The instruction file must be an absolute, current-user-owned regular file of
at most 32 KiB with owner-only permissions (`0600`). Its role extends channel
instructions; it does not grant tool access or replace native permission rules.

In production's `native/settings.json`, merge this setting while preserving
existing permissions and other native settings:

```json
{
  "claudeMdExcludes": ["/absolute/home/.claude/**"]
}
```

Replace the example with the real absolute path to the owner's personal Claude
directory. Claude matches these glob patterns against absolute instruction-file
paths, including rules and imported files. If the personal directory is linked,
exclude both its normal path and resolved target; review project-file links
separately on older Claude versions. This excludes personal
instruction loading without editing the owner's `~/.claude/CLAUDE.md`. The Host
copies shared native settings into each agent/audience profile, so the same
exclusion applies to owner-private and group turns.

Keep project instructions enabled. Managed policy instructions remain in force
and cannot be excluded this way. Personal text copied into a project file or
imported from a different directory needs its own review or exclusion. This
setting controls instruction loading, not ordinary file reads or OS isolation.
See Claude Code's official
[instruction exclusion documentation](https://code.claude.com/docs/en/memory#exclude-specific-claudemd-files).

Wait until the inbox has no `queued` or `claimed` turns before changing
configuration; `claimed` includes memory/experience processing and final reply
delivery after native execution completes. Then run `make install` to validate
configuration, restart the service and verify a fresh connection. Role content
and shared native settings participate in the existing runtime fingerprint:
changes replace incompatible native conversations on the next turn while
retaining Host history and durable memory. No new configuration schema or native
session migration is required.

### Instruction-isolation validation, 2026-10-12

Claude 2.1.223 passed clean-input native receipt probes: the baseline loaded the
personal ancestor instruction; the excluded profile skipped it while retaining
its own user and project instructions. A separate native turn used a temporary
profile with the deployed settings, the actual service role and production cwd.
It completed in Chinese without unsolicited English coaching. Initial probes
inherited the probe script through stdin and were superseded before behavioral
acceptance; the private evidence retains those attempts.

The service restarted at 07:18 UTC with a fresh Stream connection. The personal
instruction file, provider/model configuration and every preexisting run,
session, memory and channel-message row remained unchanged. Independently
arriving channel tasks added records after restart; an unchanged total row count
is not claimed. This task sent no DingTalk test message. The
structural validation report (private validation evidence)
separates native instruction receipts, deployed-policy behavior, startup and
historical-data preservation. Complete output and backups remain owner-private.

## History/discovery upgrade acceptance, 2026-10-11

On 2026-10-11, the [history/discovery upgrade](conversation-history-detail.md)
passed `make check` and isolated native acceptance, then installed with a fresh
Stream connection. A private schema 11 backup was taken first; read-only
comparison after schema 12 migration found unchanged rows in all 23 legacy
tables. DWS configuration remained absent. This is startup/migration acceptance,
not a new DingTalk user-turn or live DWS collection check. The structural
report (private validation evidence) retains
the verification without private message content.

## Directory access policy

The production agent adds the owner's existing `~/github` and `~/gitlab` trees
to Claude's native directory allowlist. This replaces the earlier root-directory
grant. It is a private native-profile setting, not a default granted by
installation. The owner-only `native/settings.json` uses the following shape;
replace the illustrative paths with the verified absolute paths on the host:

```json
{
  "permissions": {
    "additionalDirectories": ["/absolute/path/github", "/absolute/path/gitlab"]
  }
}
```

Resolve `~/github` and `~/gitlab` to their existing real directories before
writing the array. Replace the previous `"/"` entry, preserving unrelated
settings rather than replacing the file. These entries cover the two directory
trees in addition to the session cwd and any explicitly selected project mounts;
they do not create an operating-system sandbox.
The native setting extends working-directory access without changing Claude's
permission mode or pre-approving all Bash commands. Command parsing/approval,
edits and OS permissions remain independent checks. Keep the existing MCP allow
rules and source selection unchanged. The setting affects only Agent-Host's dedicated
`CLAUDE_CONFIG_DIR`, not the owner's personal Claude profile.

These native permission directories are not project participants: they do not
change the Host cwd, project registry, memory scopes or selected knowledge sources.
Unlike project `--add-dir` entries, settings-based directories grant native access
without requesting extra project instruction loading. The profile settings file
participates in the runtime fingerprint, so changing this policy starts a fresh
compatible native conversation on the next message while preserving history and
memory. Tool-free extraction remains tool-free. Check the inbox is idle before
changing the policy and use `make install` to restart and verify the connection.

The native behavior is documented in Claude Code's
[working-directory permissions](https://code.claude.com/docs/en/permissions#working-directories).

On 2026-10-10, a fresh native Claude 2.1.223 session using the production profile
successfully accessed both allowed roots from an isolated cwd. A separate empty
fixture outside the cwd and both roots was rejected by the native directory
permission check, as expected; no retry or alternate access was attempted.
`make check` passed and `make install` restored a fresh Stream connection with
the narrower settings. This is native and startup validation, not a new DingTalk
round trip. Real inputs and full evidence remain outside the repository.

### Historical root-directory validation

The following evidence used the earlier `additionalDirectories: ["/"]` policy,
which is superseded by the two-root allowlist above. It does not establish
acceptance of the narrower policy.

On 2026-10-10, the exact production Claude 2.1.223 binary passed a read-only
probe from an isolated cwd: an external fixture read and a directory inventory
both succeeded with the earlier root grant and no native permission denials.
`make check` passed, and `make install` restored a fresh Stream connection. The production
profile changed without changing the shared command allowlist. Private inputs,
fixture paths, complete outputs and platform records remain outside the repository.

The subsequent owner-DM inventory completed in a fresh `/new` conversation and
matched the independent count. Its final reply was verified from one completed
historical window after the bounded event listener expired; reply and completion
reaction were separately accepted by the Host, and no listener subscriptions or
consumers remained. Memory was unchanged and experience was empty. The run
retains one native shell-syntax approval refusal before successful read-only
commands; directory access does not imply arbitrary command approval.
An earlier repeated-input attempt stopped at an existing routing clarification
and never reached Claude. That failure is preserved separately, and only that
test request was cancelled. This validation does not claim that repeated-input
automatic routing was repaired by a permission setting.

## Installation and lifecycle

`make install` builds the binary and invokes `tools/service/install.py`. The
installer stages a private candidate binary and checks production configuration
before stopping an existing Agent-Host instance. It validates installation
destinations, waits for the old process to exit, atomically replaces the binary
and plist, enables the launchd job, and bootstraps it. A failed step is surfaced;
the installer does not silently restore an old service or switch providers.

The LaunchAgent has `RunAtLoad=true`, `KeepAlive={SuccessfulExit:false}`,
`ThrottleInterval=30`, `ExitTimeOut=30`, and an owner-only umask. Temporary Stream
disconnects recover inside the receiver with the official SDK's cancellable
backoff, retaining running native work and the durable queue. Unexpected process
failures remain visible and launchd starts a replacement, throttled to avoid a
tight restart loop. SIGTERM cancels native execution while allowing the existing
bounded outcome/delivery recording to finish. `launchctl bootout` unloads the job
for an intentional stop. Login starts one fresh process; already claimed work
and ambiguous replies are never replayed.

Schema 11 adds private attachment download capabilities without rewriting old
queued inputs or run evidence. Downloads occur after claim and before native
execution; missing/expired capabilities fail explicitly. Stored attachments and
database backups require the same owner-private protection as the rest of state.
Quoted callback summaries do not trigger historical-message or media retrieval.

Readiness requires a new `channel.connected` event after the previous writer
stopped, plus the same running PID through a short stability window. Historical
log events cannot establish readiness. Startup errors, malformed event output,
PID changes, termination and missing connection evidence fail installation.
This is a startup check, not continued uptime monitoring or end-to-end delivery
proof. The last successful installation receipt may remain after later failure;
use launchd state and current logs for live diagnosis.

Stop the writer before local memory review/retry. Operator stop command:
`launchctl bootout gui/$(id -u)/com.agent-host.production`. Use `make install`
for the next explicit start. This operator action does not affect development
state or delete production history.

## Validation

The historical records below predate the concurrent-conversation and attachment
paths. On 2026-10-11, `make check`, the relevant 886-test Go race suite and isolated
native image/file checks passed. `make install` verified a fresh connection;
separate live tests verified overlapping group/private execution and private
session continuation. Live image/file delivery and quoted-content callbacks
remain unverified, as explained in the
[conversation validation record](channel-conversations-detail.md#migration-recovery-and-validation).
Installation readiness alone does not validate those input paths.

Offline tests cover fixed production paths, native-profile/environment isolation,
private configuration, no-network checks, preservation of production state during
installation, no import of checkout fixtures, stale-log rejection, startup/stop
failures and secret-safe errors. `make check` includes installer tests in addition
to Go formatting, vet and tests. Actual deployment evidence is separate below.
The whole Go race suite passed 499 cases across 18 packages; the installer suite
passed 20 cases. A real native Claude/company-Jev smoke turn ran in temporary
state/workspace/native directories, streamed the expected response, completed
execution and memory processing, and left production state untouched.

## Deployment evidence

At 15:50 Singapore time on 2026-10-12, the final `make install` restoration
verified a fresh Stream connection and running PID 11630 with channel policy
version 6 and shared concise-response/source-link guidance. `make check` passed;
ordinary CLI/channel and tool-free invocation tests preserve structured
extraction isolation. A bounded native turn retained its supplied source URL and
key facts in 317 characters after an initial 605-character reply prompted tighter
default guidance. This probe used supplied public-source material, not a native
external lookup or a new DingTalk test message. Personal instructions and
production configuration remained unchanged; no historical records were deleted.

The first restart used an incomplete idle check and cancelled one pending
experience extraction after its native task completed. Independent
`run retry-experience` recovered that save to `empty`, without task replay or a
new chat message. The original failure remains in channel and execution evidence.
The corrected operator check includes `claimed` turns through finalization and
delivery. The response-guidance report (private validation evidence)
retains this incident and separates prompt transmission, native behavior,
production startup and independent recovery.

At 06:09 UTC on 2026-10-12, the owner's explicit `cc` selection replaced the
production Claude gateway and credential with the alias values and selected
`claude-sonnet-5[1m]`. `make install` started PID 72171 with binary SHA-256
`86b64376b5bdb46648a268c8cb9aa82311cfe0c81613972bd0c9df988d57164e` and a fresh
DingTalk connection. The temporary output override and Host 8192 ceiling were
removed. Jev, dedicated native profiles and configured tools retained their
separate settings. Read-only comparison found all 9,244 historical rows in 28
tables unchanged after the backed-up configuration switch.

`make check` and 294 affected race cases passed. Normal native execution and an
updated-adapter injected empty-stream recovery both completed through the exact
`cc` gateway/model. The latter used the native 64,000-token output budget, one
successful Read and a same-model non-streaming HTTP 200 response. Its observed
native context window was 1,000,000 tokens. The
cc switch report (private validation evidence)
separates this isolated acceptance and production startup from a new DingTalk
user-task round trip, which remains unverified.

At 05:49 UTC on 2026-10-12, `make install` deployed connection recovery and
bounded native stream recovery with PID 39170. The installed binary SHA-256 is
`4a3f84d85ccb5f99977e7f23c3f42c5920326b02aaf5d7b919e1f8d14fdd3af0`.
The idle runtime was backed up before its private environment opted in to native
fallback with an 8,192-token per-response bound. Startup established a fresh
Stream connection; launchd showed the running process and applied unsuccessful
exit restart policy with a 30-second throttle. Read-only comparison found all
9,151 historical rows in 28 tables unchanged.

`make check` and 493 affected race cases passed. A real updated-adapter probe
injected an empty second stream after one successful Read; same-provider/model
native recovery returned HTTP 200 and completed without another tool call.
Production connection, isolated native recovery and local WebSocket reconnect
tests are distinct evidence: no new DingTalk user-task round trip or natural
production reconnect is claimed. The
recovery report (private validation evidence)
preserves those boundaries and the failed comparison probes.

Observed on 2026-10-10 at 06:51 UTC:

- The authorized memgov `service stop` succeeded. Its LaunchAgent was unloaded
  and disabled, its old process exited, and active channel leases reached zero.
  Unified shutdown also stopped proactive monitoring and the associated
  data-source worker. Existing memgov state was preserved.
- `make install` succeeded and confirmed a new DingTalk Stream connection with
  production process PID 92002. Runtime data lives under the managed user root;
  the production inbox was initially empty, with no development-data import.
- Installed binary SHA-256:
  `40593c21b7f53bee6fb5a0c22003d14fdc6ace656143b01a2aebfa3239140adb`.

The first installer attempt incorrectly treated launchd's transitional state
with a PID as termination, even though the receiver connected. The installer
was corrected to distinguish startup from terminal states, with a regression
case including nested launchd state fields. The second `make install` completed
the stop/reinstall/fresh-connection path successfully. No automatic fallback or
task replay was introduced.

At 06:52 UTC the owner's first private message completed the live channel loop:
durable receipt, source-linked native execution, Jev memory classification and
one reply with DingTalk business acceptance (`errcode=0`). Execution completed;
memory was `unchanged`, experience was `empty`, and reply status was `accepted`.
The greeting was not promoted to long-term memory. The host run took 41.5 seconds:
39.4 seconds in native execution and 2.0 seconds in memory classification.
This is one measured turn, not a latency guarantee. Platform acceptance does not
prove the user read the reply or accepted a business outcome. Live follow-up,
`/new` and interrupted-turn cases remain separately unverified; offline tests
cover those behaviors.

At 07:07 UTC, `make install` deployed original-message progress reactions with
PID 18704 and binary SHA-256
`dc81bd0ecccdfba221c887b9c37b599047766efd2ed2a478f6bd100760c17f10`.
The private bot configuration gained the verified legacy `robot_code`; native
session bindings and production memory scope were preserved. Startup migrated
state from v4 to v5 and left the earlier message without retroactive markers.

The next owner message completed processing and final reply delivery. Its
已收到, 处理中 and 已完成 additions were all `accepted`; both predecessor
removals were also `accepted`, leaving only 已完成 known active. The measured
stage operations took 600 ms, 824 ms and 591 ms respectively. The owner replied
“可以了”, confirming the live display. Real-platform failure/timeout recovery
remains covered by offline regression rather than this successful live turn.

At 07:16 UTC, a further `make install` deployed the Markdown reply layout
with PID 32445 and binary SHA-256
`a8db0644d3153d340ba02248f80a3771ef3079fec462292c7ec6ce93f268de57`.
The production inbox was idle before restart and the new process established a
fresh Stream connection. `make check` and the 40-case channel test suite passed;
formatting regressions cover measured metadata, failure visibility, UTF-8
excerpts and code-fence closure. Markdown platform acceptance/client rendering
is awaiting a new owner message; the preceding live reaction success does not
prove the new reply layout.

At 07:17 UTC the next owner message completed with a platform-accepted Markdown
reply. The owner then requested removing the lowest-level status details and ID.
At 07:19 UTC, `make install` deployed that presentation-only adjustment with
PID 36886 and binary SHA-256
`db2eca0c5552777180e82a606c6a2556b69d13145a0fa403f4c693782d3baa9b`.
The reply footer now keeps only measured timing and the reported model; run IDs,
detailed outcome labels and diagnostic commands remain local. Failure warnings
and excerpt notices remain visible. `make check` passed, the inbox was idle
before restart, and the new process established a fresh Stream connection.

At 07:30 UTC, `make install` deployed host-phase reactions from an isolated,
verified source snapshot, with PID 56789 and binary SHA-256
`7bc29a02ef3c07df5d5a95f587b89811bec99b892fefc9e4ca31224a18e70ae4`.
This deployment includes memory retrieval, AI execution, Jev decision, evidence
and conditional experience phases; it excludes concurrent project-routing work.
State migrated from v5 to v6, preserving the four prior completed messages and
their platform-accepted replies. The inbox was idle and startup established a
fresh Stream connection. `make check` and 398 race cases across state, host,
channel, DingTalk transport and CLI passed. A blocked-Jev test confirms its
marker can be accepted before the decision returns. Live acceptance and client
display of the new phase labels await a new owner message.

## Registered project execution

The installed service uses its existing workspace as the default, and resolves
locally registered projects from its own state database. Registry writes retain
the single-host lock requirement. Source checkouts are never registered or
modified implicitly by installation. Native sessions share the isolated native
profile but bind separately to project, physical cwd and host runtime
configuration. Auto memory is disabled for child invocations. Historical default
workspace knowledge is not imported into other projects.

At 07:43 UTC on 2026-10-10, `make install` deployed project routing and runtime
assembly with PID 76308 and binary SHA-256
`229cde9ad09e0d2e733bb9d3d61bde80c0384dc865aadba62e84640f6a4613fd`.
That same binary passed six real Claude turns in two isolated projects. The
production inbox was idle; a stopped-state backup preceded schema v6 → v7.
All 12 historical table digests and four existing native-profile files remained
unchanged. Startup established a fresh Stream connection, and the process was
still running at the subsequent check. The production project registry remains
empty, so existing owner messages keep the default workspace. See the
[runtime assembly validation](runtime-assembly-detail.md#validation) and
installation evidence (private validation evidence).
Real DingTalk project-command round trips remain unverified.

## Runtime knowledge deployment

At 08:39 UTC on 2026-10-10, an isolated `make install` enabled Dokki and Confluence
runtime tools for the default workspace, with PID 68845 and binary SHA-256
`4039619822962c7b1cd613a56d04e01ef474fb55677a9a942d3c3a9d6409c335`.
Private source configuration and provider-specific token files now live under
the production config directory; the installed service has no memgov process,
database or checkout dependency. Five exact native MCP allow rules authorize
these read-only tools. Registered projects still need explicit source selection.

The inbox was idle before restart. The new process established a fresh Stream
connection and passed installed local source/service checks. State remained at
schema 7 with seven completed and one failed historical channel messages.
The binary excludes concurrent runtime-phase/schema work. The
installation receipt (private validation evidence)
and [native-source acceptance](../experiments/knowledge/README.md) record separate
evidence: no new DingTalk knowledge-question round trip is claimed. DWS
conversation intake and learning remain disabled.


## History-reference diagnosis and fixed workbench ports (2026-10-13)

The live DingTalk turns at 14:38–14:39 Singapore time completed execution and
reply delivery. The second normalized receipt contained an `interactiveCard`
quote's platform message ID and type, without body text. Later inspection found the
parser discarded this message type; absence in the receipt did not prove
absence in the raw callback. See the
[card parsing correction](channel-conversations-detail.md#interactive-card-quote-parsing-2026-10-13).
Its three history reads used the platform ID, which the Host incorrectly reported as invalid read bounds.
History search succeeded; the quoted original was absent from admitted history.
The reference error and tool guidance now identify this mismatch explicitly.
No external card retrieval or business-ticket completion is claimed.

The shared service workbench now uses fixed port 8765, with a CLI override.
Regression checks exercise restart URL stability, distinct source links for
colliding run/memory IDs, preserved raw tool evidence, invalid port configuration,
platform-ID rejection and continued reads of valid Host references.


After restart, the shared live API returned 54 runs (47 DingTalk, seven Feishu),
with readable details from both sources and two DingTalk memory records.
Execution and memory pages returned HTTP 200. Both channel connections emitted
fresh readiness events at 14:47 Singapore time. One viewer process listened on
8765, and both workers used the supervisor workbench. `make check` passed;
no task replay or outbound test message was performed.
