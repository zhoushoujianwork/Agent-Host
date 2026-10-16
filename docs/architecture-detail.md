# Agent-Host implementation notes

Companion to the [current architecture](architecture.md), reviewed on 2026-10-16.
The main document owns scope; [progress](release-follow-up.md) owns priorities.
Feature-specific details retain historical schemas and bounded acceptance results;
they are not a claim that every current path passed live validation.

## Current implementation map

| Concern | Code / authoritative guide |
| --- | --- |
| Selected local checkout discovery and shared receipts | `internal/projectruntime`, `internal/cli/project_runtime.go`; [project records](development-projects-detail.md) |
| Verified private users and profiles | `internal/cli/users.go`, `internal/assembly/workspace.go`; [workspace contracts](workspace-layout-detail.md#multi-user-recording-and-retrieval) |
| Owner/member execution and one-use approvals | `internal/cli/channel_authorization.go`, `internal/approval`, `internal/harness/claude/restricted.go`; [authorization details](dangerous-operations-design-detail.md) |
| File-memory publication, source-linked objects and native Skills | `internal/state`, `internal/memory`, `internal/experience`, `internal/objectmemory`; [file backend](file-memory-design-detail.md), [procedural learning](procedural-learning-detail.md) |
| External evidence ingestion and installed memory skill | `internal/cli`, `skills/agent-host-memory`, `tools/install.py`; [external memory](external-memory-skill-design-detail.md) |
| Knowledge retrieval, Confluence writes and reply guidance | `internal/knowledge`, `internal/assembly/context.go`, `internal/harness/claude/knowledge_instructions.go`; [source tools](knowledge-sources-detail.md) |
| Selected-source matters and suggestions | `internal/discovery`, `internal/assistance`; [P0 implementation](personal-assistant-detail.md#p0-implementation) |
| Service lifecycle and independent runtime | `internal/cli/service.go`, `tools/service/autostart.py`; [runtime operations](service-install-detail.md) |
| Encrypted backup, restore verification and retention | `internal/backup`; [backup details](workspace-backup-design-detail.md) |

The supervisor uses one worker per enabled channel and a shared local workbench.
Runtime state and native sessions remain channel-local. Memory publication can
share file storage without merging Agent/audience visibility. Shared project
receipts exclude original request/response text and native context.

## Storage and context authority

- `config/`: user-maintained inputs, read-only during execution. Automatic first
  contact initializes runtime `users/<id>/USER.md`; explicit configured overrides win.
- `channels/<name>/state/state.db` and CLI state: operational evidence, source
  receipts, candidates and lifecycle records. These remain durable.
- `memory/`: versioned published knowledge and Skills for explicitly migrated
  runtimes. Source publication manifests own visibility; domain views are derived.
- `cache/`: rebuildable retrieval indexes, scoped task views and native Skill packages.
- `datasource/plugins/`: source snapshots and provider-owned runtime data.
- `projects/`: shared registry and per-run engineering receipts, not durable features.
- `backups/`: encrypted recovery archives; the decryption key is kept outside the root.

Migration is explicit per source and reversible. Startup cannot infer a backend
from an existing directory. A production relocation receipt identifies a separate
runtime: development builds and tests must not update or restart that bundle.
See [layout](workspace-layout-detail.md) and [migration](file-memory-design-detail.md).

## Code boundaries

The CLI composes channel and local execution through selected profiles. Local Git
discovery, automatic enrollment, routing, default-workspace execution and project-set
assembly are implemented. Remote discovery, managed clones and durable worktree/PR
coordination remain proposed. The Codex package remains a scaffold.

```text
Agent-Host/
├── cmd/agent-host/           CLI entry point
├── internal/
│   ├── cli/                 Commands and local ownership
│   ├── event/               Versioned execution events
│   ├── terminal/            Text and NDJSON renderers
│   ├── decision/jev/        Explicit company or official API
│   ├── host/                Task coordination and run supervision
│   ├── channel/             Channel input, identities, and delivery
│   │   └── dingtalk/        Application-bot Stream and session-webhook transport
│   ├── routing/             Explicit and tool-free registered-project selection
│   ├── assembly/            Workspace, instructions, and context preparation
│   ├── knowledge/           Source MCP retrieval and authorized Confluence writes
│   ├── harness/             Shared adapter boundary
│   │   ├── claude/          Claude Code protocol and process integration
│   │   └── codex/           Codex adapter scaffold (not executable)
│   ├── annotation/          Source-linked objects, aliases and topic applicability
│   ├── memory/              User-statement decisions and finalization
│   ├── experience/          Tool-free extraction and selected procedure publication
│   ├── evaluation/          Categorized fixture checks and evaluation reports
│   └── state/               Operational records and raw evidence
├── config/                 Example local channel configuration
├── evals/                  Versioned fixture suites
└── docs/                    Research and architecture documents
```

| Private package | Responsibility |
| --- | --- |
| `internal/host` | Business task lifecycle and execution coordination |
| `internal/harness` | Shared execution contract and capability reporting |
| `internal/harness/claude` | Claude-specific protocol and process integration |
| `internal/harness/codex` | Reserved scaffold; no runtime implementation |
| `internal/annotation` | Versioned source spans, service identities, aliases, topics and applicability checks |
| `internal/memory` | Source-linked user-statement decisions and finalization |
| `internal/experience` | Evidence-linked procedure extraction and Jev screening/comparison |
| `internal/evaluation` | Isolated fixture execution, expected/actual comparisons and structured evaluation reports |
| `internal/channel` | Durable input, source identity, conversation scheduling, attachment preparation and result delivery |
| `internal/channel/dingtalk` | Owner DM and enterprise group-mention admission, supported media/quotes, bounded media downloads, Stream lifecycle, safe ACKs and HTTP replies |
| `internal/routing` | Explicit identities and bounded tool-free project-set classification |
| `internal/assembly` | Task context, workspace, and agent configuration |
| `internal/knowledge` | Private source selection, pinned credentials, source MCP retrieval and Confluence page/blog writes with attributed receipts |
| `internal/state` | Durable state, atomic derived indexes, scoped retrieval and recovery bookkeeping |

Keep APIs private until external consumers justify public contracts. Add
concrete adapters, channels, and storage implementations below the component
that owns their behavior, as those implementations are developed.

`cmd/agent-host` composes the host through `internal/cli`. The host coordinates routing, assembly,
memory, operational state, channels, and the shared harness boundary. Native
adapters implement that boundary without importing host coordination code.
Routing and memory must not depend on Claude- or Codex-specific protocols.

Channel input retains source identity; routing and host policy use it when
selecting an agent and access scope. The first CLI loop uses one local owner and
project-scoped memory. The DingTalk channel accepts owner private messages and
verified enterprise group mentions. It resolves agent/audience identity before
automatic registered-project routing with default execution when no association
is reliable,
deduplication and independent delivery status. Chat exposes only `/new`, `/status`
and `/help` as Host controls; local interfaces own project and agent settings.
Historical manual pins are retained but do not determine new tasks. The empty
catalog path skips classification. No new Host project question or waiting
record is created; native execution asks for business details as needed.
Historical pending requests require their original requester's explicit
confirmation or cancellation and never replay automatically. Configuration,
selected-directory and provider failures remain explicit, and default execution
does not extend project or knowledge permissions.
[Runtime assembly](runtime-assembly-detail.md) separates stable memory scope from
physical cwd and preserves native session compatibility. Joint tasks carry an
explicit project set and additional directories, retrieve only those scopes and
pause automatic new memory/experience publication. New default-workspace handoffs
and continuations keep normal history, memory and experience processing even
with a populated catalog. Historical `MemoryAttribution=unresolved` runs keep
their skipped learning records but remain readable within their saved default
scope. Empty-catalog runs use the same normal memory flow. The
[routing details](smart-project-routing-detail.md) define that boundary.
Each state home retains a SQLite database for operational records and candidates.
The legacy backend also keeps versioned memory there; migrated file runtimes use
the publication and cache boundaries above. `internal/state` owns transactions,
with semantics in `internal/memory`, `internal/experience` and `internal/annotation`.
The schema descriptions below record historical migration steps, not the latest schema.
Schema v3
migration derives annotations and terms from existing memory text without
rewriting statements, immutable revisions, evidence or review decisions. Schema
v4 adds channel receipts, private reply capabilities and native-session bindings;
run provenance is derived atomically from an accepted receipt. Unfinished claims
and attempted replies are marked unknown on recovery instead of replayed.
The SQLite backend is implemented; no public plugin loader is provided.

The channel scheduler serializes each conversation binding and shared
agent/audience memory partition through execution, memory finalization and reply.
Other audiences use a bounded pool; SQLite transactions remain short and
single-writer. This protects memory read/decision/write ordering without
isolating shared checkout files. Queued commands follow the same lane, including
`/status`; no asynchronous memory work or `/stop` is introduced.

Schema v11 adds protected `input_capabilities` alongside existing private reply
capabilities. Attachment and quote metadata remain in source-linked task input,
separate from the current sender's statement and reusable memory. Binary files
live under the private state home's `attachments` directory. Harness-neutral
image bytes cross only the execution boundary; the Claude adapter owns native
stream-JSON encoding. Download codes and image base64 stay outside public run
payloads and observations. Old queued receipts and claimed-task no-replay
semantics remain unchanged.

## Execution and memory

- Keep business task identity separate from an execution attempt and native
  harness session identity. A session can span multiple turns.
- Prefer supported structured lifecycle signals. A process ID or changed file
  is evidence, but is insufficient by itself to establish task success.
- Declare adapter capabilities explicitly, including streaming, cancellation,
  session continuation, and approval interaction. Do not silently emulate an
  unsupported capability or replace the requested execution engine.
- Retain observed terminal outcomes and provenance. Report unknown outcomes
  explicitly when completion cannot be established.
- Native harness permissions remain authoritative. Host policy may constrain
  execution further; retrieved content cannot enlarge execution authority.
- Separate raw evidence, temporary task progress, and durable knowledge.
  Update knowledge only under an explicit finalization policy, retaining links
  to supporting evidence and a distinction between claims and verified facts.
- Recovery must not automatically replay operations with external effects just
  because recording their result was interrupted.
- Pair native tool calls/results by their IDs and journal sequence before using
  them as experience sources. Tool-free extraction runs separately; its output
  cannot serve as evidence of business execution.
- Migrated file-memory runtimes publish Jev-selected experience automatically;
  legacy SQLite retains explicit review. Go
  validates source references, scope, compared revisions and exclusions before
  publication. Jev screening does not certify truth. A scoped blacklist must
  prevent both recall and equivalent re-admission. Expired, invalidated and
  conflicted memories remain excluded while evidence stays inspectable.

## External sources

The [external-source extension](knowledge-sources-detail.md) implements
Host-selected MCP configuration through assembly and harness adapters.
`internal/knowledge` owns private source configuration, credential pinning and
the source MCP process: Dokki/local Markdown reads and authorized Confluence
page/blog writes. Enabled datasource plugins provide a global source catalog for
owner execution; optional users.json applies explicit non-owner/shared grants.
The restricted member profile currently removes business MCP servers entirely.
Legacy standalone source selectors remain a compatibility path. Knowledge
results remain journal evidence but cannot support execution-experience candidates.
The separate [Host history implementation](conversation-history-detail.md) derives
a read-only search/read view from retained bot receipts and exact saved replies.
It binds each MCP invocation to the durable current receipt: administrative
channel inspection stays Host-wide even on a partitioned Store. Native MCP
mounting is reused without making history a remote knowledge provider or DWS cache.

`internal/discovery` owns optional exact-profile selected-source DWS polling and
tool-free topic extraction. Schema 12 stores its independent collection ledgers,
immutable message revisions, proposals and citations. Local review/acceptance
exports bounded owner-private evidence and creates no run or memory. Background
conversation learning remains planned; run-bound memory policies are unchanged.
Knowledge connector reuse does not change SQLite ownership, native permissions
or the distinction between reported outcomes and execution observations.
Source integration validation is recorded in its own details, separately from
the earlier validation below.

The [local-source details](local-knowledge-detail.md) specify canonical roots,
explicit relative file/directory entries, bounded search coverage, source-relative
references and digest-pinned reads. Local roots do not change business cwd or
native additional directories. Selected older notes remain immutable private
snapshots with an outside-repository provenance manifest; no run, user statement
or reviewed memory is manufactured from them. Source grants and learned-memory
audience partitioning are independent boundaries. Neither source
filtering nor native approval bypass provides filesystem isolation.

## First implementation validation

Use a controlled local workspace to verify one real Claude Code execution.
Observe completion, failure, and cancellation, and retain enough evidence to
explain the recorded outcome. Do not count CLI help, mocks, or compilation as
proof of a working native integration.

The [first MVP details](mvp-validation-detail.md) record the earlier persistence
and session validation. The [experience details](experience-memory-detail.md)
retain historical live evidence for file edits/tests, resume, explicit review,
fresh-session reuse and invalidation. File-memory and procedural-learning records
cover bounded automatic publication,
reuse, disabling and retry checks. Equivalent re-admission remains model-dependent
for arbitrary rewrites; broader usefulness and scale remain unmeasured. The
[CLI memory details](cli-memory-design-detail.md) remain the broader roadmap.
Add storage and protocol
dependencies only when the validated path needs them. After execution is
verified, prove memory finalization, independent recovery, and fresh-session
reuse before treating the first local product loop as delivered.

## Continuing regression

Evaluation consumes the real store, context assembly and decision components.
Both chat and recall evaluation use `Store.Retrieve`, then budget its accepted
matches and prefiltered persistent context with `assembly.Select`. Memory search
uses the same applicability rules. Deterministic cases use temporary SQLite state; live cases invoke the selected
Jev provider without native business execution. Keep these modes and their
results distinct. A passing fixture policy check is not evidence of model
accuracy or improved task outcomes.

Everyday failures become minimized, reviewed cases with stable IDs, categories,
expected outcomes and source references. Preserve their failing baseline and
subsequent results. The suite content fingerprint and per-case observations
make reports comparable across changes. Existing Go tests continue to own
native protocol, transaction, expiry, retry and other lifecycle invariants
that the JSON suite does not exercise.

## Project assembly storage

Schema v7 follows the phase-reaction migration and adds project registration and
durable conversation selection. Existing memory scopes and historical payloads
remain unchanged. Sessions/runs carry project, physical workspace and runtime
fingerprint separately from memory scope. Queue configuration identity stays
separate from stable authenticated conversation identity and resolved execution
binding. Host conversation controls are applied in the ordered message worker.

The earlier intelligent-routing migration extends descriptors, manual/automatic selection,
numbered choices and project-set execution snapshots on the latest merged schema.
Historical pending clarification survives restart without being a native execution claim.
Original task text and later source-linked user supplements remain separate;
claimed unknown execution and uncertain delivery retain the no-replay rule.
Current routing ignores old manual pins without rewriting their historical rows.
Unresolved new requests execute in the default environment without creating more
choice records. Existing waiting requests remain owner-confirmed only.
`/new` resets the compatible session for the latest actual project set and retains
memory. `/status` exposes only safe conversation/task state; `/help` lists the
three supported controls. Native interactive commands are not forwarded.
