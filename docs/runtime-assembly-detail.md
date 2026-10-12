# Runtime assembly implementation details

The [main design](runtime-assembly.md) governs scope and user behavior.

## Identity, configuration and interfaces

The private project registry stores immutable `id` and `scope`, mutable `name`,
description, aliases and canonical `workspace`, and creation/update timestamps.
IDs use lowercase ASCII letters, digits, `_` and `-`; `default` is reserved. A project has one
current directory. IDs, directory paths and knowledge scopes are unique.

Local `project add`, `update`, `list` and `show` use the selected `--home`.
Writes take the existing single-host lock. Inspection does not require provider
credentials or the continued existence of project files. `--project` and explicit
`--workspace` are mutually exclusive. A canonical directory exactly matching a
registered project selects its stable scope; other directories retain legacy
directory-scoped operation. A moved project's reserved old scope cannot be
adopted by another project or unregistered operation.

`assembly.Runtime` carries the selected project set, primary project ID/name,
memory scope, native workspace, additional directories and runtime fingerprint.
The fingerprint includes the sorted project set and its canonical directories,
primary cwd, host assembly policy, Claude
binary/model, effective native profile path, presence and SHA-256 of its shared
`settings.json`, native-loaded project/local permission settings for the primary
cwd, and explicit tool settings. This includes distinct Git-root local settings
when the native loader uses them. Settings changes rotate
the session; credentials and native conversation files are excluded. Only the
digest enters the fingerprint; setting contents are never stored in Host state.
Native auto memory is disabled by child environment and invocation settings.
Ordinary code and instruction files are excluded from configuration fingerprints;
native permission-setting files are included. Additional directories load project
instructions without becoming separate native permission-setting roots.

The [knowledge increment](knowledge-sources-detail.md) changes the assembly policy
to strict explicit MCP mounts and adds the selected-source binding fingerprint.
Source configuration and token changes rotate compatible sessions; tools are
selected again for each turn. Ordinary native authentication remains outside
the fingerprint. This is distinct from the earlier assembly validation below.

Sessions and runs retain `projects`, `additional_directories`, `project_id`,
`project_name`, `workspace` and `runtime_fingerprint` separately from `scope`.
The project-set extension queries each selected memory scope separately;
scope still governs single-project retrieval, review
and memory retry. Workspace governs native process cwd and evidence path bounds.
Historical missing workspace fields are interpreted from the old scope without
altering original payloads. Native extraction uses a private temporary directory;
the source workspace remains in the extraction envelope and evidence identity.
Multi-project runs retain per-project context but pause automatic new statements,
experience extraction and memory retries. A neutral cwd is not a shared memory
namespace. See the [routing details](smart-project-routing-detail.md) for this
extension and its separate acceptance requirements.

Claude receives one main cwd and repeated `--add-dir` arguments. The adapter
revalidates the entire directory set immediately before launch; native acceptance
showed that Claude can ignore a missing additional directory. Child environment
sets `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` to load additional-root
`CLAUDE.md`. Root `AGENTS.md` fallback is attributed to its directory and added
without replacing the native system prompt or editing project files. Tool-free
experience extraction has no additional directories and uses its own temporary
cwd. Native auto-memory settings remain child-local.

## Native provider failures

The Claude adapter defaults `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1` in the
child environment and invocation settings for ordinary execution and extraction.
This prevents a native fallback that gateways may reject with
`400 streaming is required`; a clean empty stream can still fail without a
native retry. Explicit `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0` enables
Claude's same-provider, same-model non-streaming recovery. An omitted
`CLAUDE_CODE_MAX_OUTPUT_TOKENS` retains the native output budget; an explicitly
supplied value must be a canonical positive integer. The earlier 8192 ceiling was
a workaround for one gateway, not a native CLI requirement, and has been removed.
That opt-in uses the native model loop and retained tool results, without starting
the Host task again. A per-response output limit includes thinking tokens. Invalid
settings fail before launching a native process. This is separate from stdout
`--output-format stream-json`; it changes neither the provider nor model and
does not replay a Host task. See the [official environment-variable reference](https://code.claude.com/docs/en/env-vars).
Claude Code 2.1.223 can still fall back after an initial streaming-endpoint
`404`, and native managed settings can take precedence; the adapter does not
claim an absolute transport guarantee.

A valid failed native final result followed by a normal nonzero exit retains
both diagnostics in the local run journal. Protocol and cancellation failures
keep their precedence, and public channel replies still omit raw diagnostics.
Disabling fallback does not establish that the original upstream stream fault
has been repaired.

## Ordered routing and recovery

Receipt retains authenticated sender, audience, source text and startup queue
identity. The single message worker processes Host controls and tasks in receipt
order. Every new task selects a reliably associated project set or the configured
default environment; saved historical manual selections are not consulted. An
empty catalog skips the project provider and executes directly. No new Host
project question or waiting record is created. Native execution owns business
follow-up questions. `/new` resets the latest actually executed project set's
compatible session, or the default when none exists, and clears follow-up routing
context while retaining memory. `/status` and `/help` provide safe conversation
information. These controls do not invoke the model or memory extraction, and
do not forward native interactive commands. Historical pending requests retain
their original evidence without replay; only their original requester may
explicitly confirm or cancel them. Default execution does not broaden directory,
project-memory or knowledge-source access. Invalid configuration, selected-path
failures and routing-provider errors remain explicit failures.
The original unresolved routing decision remains audit evidence. A default run
delegated from a nonempty catalog's `clarify` result is marked
`MemoryAttribution=unresolved`; automatic memory/experience publication and their
retries are paused. Empty-catalog default execution retains ordinary memory
processing. Default handoff need not rotate a compatible session; trusted per-turn
instructions distinguish the new request from unrelated previous context.

The execution session binding includes the authenticated conversation binding,
sorted project set, actual directories, primary cwd and runtime fingerprint.
The resolved route is stored on the claimed message before native execution, independently of its
receipt binding/scope. Run provenance is derived from the same receipt and the
selected native session. Reply and reaction delivery remain independent.

Changing cwd or runtime selects a new native session, including reverting the
same project to an earlier directory or configuration. Switching between
different projects still resumes each project's compatible session. Legacy sessions lacking
assembly metadata remain inspectable and are not implicitly continued.

For a new channel input, `SessionResumeError` distinguishes terminal execution
states (`failed`, `canceled`, `unknown`) and unfinished memory (`failed` or
`pending` after completed execution) from an active run or storage/configuration
failure. Only the former permits an automatic fresh session. CLI explicit-session
resume retains its existing validation. An active run, invalid directory, unrecognized
state, corrupted record or database error cannot trigger this recovery.

The channel creates an unused session, then atomically rechecks the previous
binding/latest run and saves the replacement binding together with the new
session's `previous_session_id` and `recovery_run_id`. These optional JSON fields
need no schema migration. A later message resumes the replacement; other project
bindings remain intact. The reply notices lost native conversational context and
retained memory, without asking for `/new`. Old run outcomes, pending memory,
receipts and native histories remain unchanged. Synthetic native error model
markers remain in raw evidence but are excluded from the user-facing model label.

Recovery marks already claimed work unknown and never replays it. Queued input
keeps ordinary ordered processing. This mechanism does not retry the failed task
within the same turn or change the provider/model to mask a model API failure.

Automatic recovery acceptance (2026-10-10) covers terminal failures, interrupted
work after restart, unfinished memory, active-run refusal, stale recovery and
corrupt-evidence refusal, duplicate delivery, scoped recall and other projects'
session reuse. The native recovery report (private validation evidence)
records an injected failure followed by two successful real Claude calls: automatic
replacement and continuation. Channel delivery and memory admission use fixtures;
no DingTalk message was sent in this check. The first live test attempt stopped
before native launch because its temporary directory was not canonicalized; the
fixture was corrected before the recorded run. The earlier production API 400
was not reproduced, so upstream error resolution is not claimed.

## Migration and rollout

The project migration follows the separately developed phase-reaction schema.
It adds registry and conversation-selection tables without rewriting old runs,
memories, revisions, candidates, annotations or evidence. Registration adopts
an exact existing directory scope; new projects use `project:<id>`. A project
directory update never changes that namespace.

Upgrade with the service stopped and a complete backup of state/native files.
The forward migration is transactional. Earlier binaries reject the new schema;
rollback requires restoring the pre-upgrade state backup alongside the earlier
binary, rather than dropping tables in a database containing new work.
Production keeps its existing default workspace and native profile. Installation
continues through `make install`, with fresh Stream connection verification.
The earlier project-set and routing migration was schema v8 → v9, after the
separately delivered runtime-phase schema. It retained explicit conversation
choices as manual pins. Current dispatch ignores those pins and selects a
reliable project association or default execution for each new task. It creates
no new pending choices and never replays old ones, without rewriting historical
choices or evidence.
The later [agent partition migration](agent-instances-detail.md) also preserves
those records.

## Validation

The results below describe the earlier single-project explicit-selection
increment, including its now-retired chat project commands. The
[intelligent-routing validation](smart-project-routing-detail.md#validation-and-acceptance)
tracks later automatic, historical clarification and joint-project behavior
separately from the current default-execution policy.

Automated checks cover registration/path validation, duplicates and lock
conflicts; A/B isolation and A → B → A continuation; `/new`; immediate follow-up
after selection; durable selection across restart; duplicate receipt handling;
scope-preserving relocation and old evidence; native cwd, instructions and memory
settings; invalid directories and interrupted work; independent replies/reactions;
and read-only viewer filtering for projects and legacy records.

On 2026-10-10, `make check` passed: formatting, Go vet and all Go tests,
20 installer tests, and 25 viewer tests. The channel race suite passed 58 tests;
both viewer JavaScript files passed `node --check`. Real-report projection was
also checked across 13 retained runs, including the first failed fixture's
empty/null recall and native-session metadata derived from its recorded events.

The final real Claude report (private validation evidence)
passed six turns using Claude Code 2.1.223 and the explicitly configured
`claude-opus-5-5` model. Two isolated projects loaded root `AGENTS.md` and native
`CLAUDE.md`, wrote the expected controlled files, resumed separate native sessions,
then recalled only their own Host knowledge from new sessions and cited its ID.
Native auto-memory files were absent, project rule files stayed unchanged, and
tool-free experience extraction used a separate temporary cwd. The fixture took
about 192 seconds. [Fixture instructions and earlier attempts](../experiments/runtime/README.md)
retain limitations and failure evidence.

The installation record (private validation evidence)
confirms `make install` restored a fresh Stream connection with the same binary
SHA-256 as that native report. Production migrated from schema v6 to v7.
All 12 existing historical tables retained identical row counts and row-content
digests, and all four prior native-profile files stayed unchanged. The stopped
writer's state, native files, configuration and binary were backed up privately at
`~/.agent-host/production/backups/runtime-assembly-20261010T074345Z`.

No production projects were registered automatically; the default production
workspace and its existing memory remain. The installation record above verifies
connection recovery. The subsequent private-chat acceptance is recorded below.

## Live DingTalk acceptance with dws

On 2026-10-10 the owner authorized private-chat validation against
`the configured application bot`. dws uniquely resolved its bot identity, and the returned
single-chat conversation matched the application's existing owner conversation.
All lookups, sends, status checks and event listening used the same explicit
organization/account profile. No group or other recipient received a message.

The first `/project` reached the Host as `richText` and was explicitly rejected
by the text-only adapter. The adapter now accepts ordered, exclusively textual
fragments and retains the original provider message type. Mixed media, link
attributes, unknown fields and duplicate fragment/content keys remain unsupported
as a whole. Earlier empty-text receipts explicitly rejected as unsupported
remain duplicates on redelivery; identity collisions and other content changes
still fail. This compatibility path never repairs old payloads or repeats a run,
reply, project switch or reaction.

Production was stopped during the controlled platform sequence. A private
acceptance runtime used the same application bot and shared execution settings,
but independent Host state, native profile, default workspace and two synthetic
projects. Alpha used root `AGENTS.md`; beta used root `CLAUDE.md`. Fifteen source
messages in this isolated runtime produced six real Claude runs, including one
standalone user declaration admitted as Host memory. One additional `/project`
confirmed the restored production runtime.

Observed assertions:

- A project-selection command immediately followed by a task entered the
  selected real cwd. Native init events and successful `Read` evidence agree.
- A and B loaded different rule markers and used different native sessions.
  A → B → A resumed A; `/new` created a fresh A session; B still resumed B.
- A fresh session recalled only alpha's saved declaration and cited its memory
  ID. B's continuation retained its own codename and received no alpha memory.
- Temporarily removing only beta's synthetic directory produced an explicit
  failed receipt with no run or default-workspace fallback. The error reply and
  failure reaction were separately accepted by DingTalk.
- Project commands produced no model runs or progress reactions. Native auto
  memory files were absent, and synthetic project files remained unchanged.
- `make install` restored production at 08:02:59 UTC (16:02:59 Singapore time),
  PID 6388. A new owner-to-bot `/project` round trip reported its original default
  workspace. Production projects, selections, sessions, runs, event journal,
  memories and revisions were compared with the stopped-state snapshot and were
  unchanged. Production still has no registered projects.

The sender-scoped dws listener observed 50 message/reaction events and was ended
with SIGTERM after the bounded validation. A subsequent dws status reported no
consumers or subscriptions. Reports retain selected event observations, source
send/delivery references, Host receipts, native events and separate reaction
outcomes. They redact credentials and isolated directory paths:

- Successful platform sequence (private validation evidence)
- Initial discovery and runner failure (private validation evidence)

The first runner attempt sampled `reply_status=sending` too early and exited;
its command subsequently settled as accepted. That failed report is retained.
The next attempt used fresh state and idempotency keys, waited for terminal
delivery states, and passed. Compound instruction messages were not admitted
as durable facts; the memory assertion uses the separate attributed declaration.

The isolated sequence used the first richText-support build. Restored production
also includes the legacy-redelivery and duplicate-key regressions; its post-restore
private command used that final build. Binary hashes are recorded separately.
`make check` passed, and 344 race tests passed across channel, DingTalk and state.
Unauthorized senders, live callback redelivery, policy rotation and interrupted
execution were not induced by this platform check; offline tests cover them.
Platform acceptance and an event observation do not establish human read status
or business acceptance.
