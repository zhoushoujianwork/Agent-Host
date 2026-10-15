# External agent memory skill: implementation notes

Implemented 2026-10-15. Scope and usage live in the
[main design](external-memory-skill-design.md); command and payload examples live
in the skill's [protocol reference](../skills/agent-host-memory/references/protocol.md).

## Ownership and connections

`make install` builds first, then runs `tools/install.py`. This user-local
installer links the built executable into `~/.local/bin` and the complete source
skill into each detected supported client's skill directory. Detection checks
the configured client directory or its command on PATH. `--agents` explicitly
selects supported clients, including those not detected; it limits the set rather
than adding to autodetection. It never starts a service or edits connections.

| Client | Default skill directory | Supported override |
| --- | --- | --- |
| Codex | `~/.agents/skills` | `$CODEX_HOME/skills` for an explicitly selected local profile |
| Claude Code | `~/.claude/skills` | `$CLAUDE_CONFIG_DIR/skills` |
| OpenCode | `~/.config/opencode/skills` | `$OPENCODE_CONFIG_DIR/skills`, otherwise `$XDG_CONFIG_HOME/opencode/skills` |
| Gemini CLI | `~/.gemini/skills` | None |
| Cursor | `~/.cursor/skills` | None |
| GitHub Copilot | `~/.copilot/skills` | None |

Default discovery directories follow the
[Codex](https://learn.chatgpt.com/docs/build-skills#where-codex-loads-local-skills),
[Claude Code](https://code.claude.com/docs/en/skills#choose-where-skills-load),
[OpenCode](https://opencode.ai/docs/skills/),
[Gemini CLI](https://geminicli.com/docs/cli/skills/),
[Cursor](https://cursor.com/docs/skills) and
[GitHub Copilot](https://docs.github.com/en/copilot/concepts/agents/about-agent-skills)
documentation. Profile overrides retain their local installation conventions.
These links make the skill discoverable; an agent's own settings still control
its availability and invocation permissions. Cloud/remote clients are separate.

Reinstalling matching links is a no-op. Existing files, directories and foreign
or dangling links are preserved. Skill conflicts are reported as `preserved`;
a CLI conflict or filesystem failure produces a nonzero exit after reporting
other outcomes. Source updates are visible through the links; moving the
checkout requires explicit cleanup of old links before reinstalling. The
installer reports a CLI directory missing from PATH, and the skill explains
how to resolve its source-checkout binary from any working directory.

`internal/cli/external_memory.go` is a transport-only client. It connects to the
owning Host's existing `memory-control.sock` at `/external-memory`, using POST
over a private Unix socket. The managed DingTalk and Feishu workers serve this
endpoint through `startManagedMemoryControl`. There is no remote HTTP listener
and no second writable database connection in the client.

`memory connection add` explicitly selects a running Host state home, a configured
Agent and optionally an allowed registered project. The Host derives the private
audience and exact scope. It stores an immutable admitted binding; the client
stores an alias in `~/.config/agent-host/memory-connections.json` with mode 0600.
`--connections FILE` selects an alternate alias file. The default alias is
`AGENT_HOST_MEMORY_CONNECTION`, or `default` when unset.

Every call rechecks the admitted binding against current Agent/project
configuration. Payload contents cannot supply a scope or audience. Revocation
blocks intake, recall, read and receipt lookup. A revoked or changed binding
requires a new alias; it cannot be silently repointed. Several aliases may share
the same admitted memory partition while keeping submission receipts separate.
This is a same-OS-user interface, not isolation against arbitrary local file access.

The first release requires explicit file-memory migration. It never changes
backends implicitly or falls back to another runtime. Existing offline memory
commands remain compatible. A schema-v15 migration adds external connections and
receipts without rewriting native runs, candidates or memory evidence.

## Evidence and selection

`internal/state/external_memory.go` validates versioned, bounded JSON submissions.
Each has a stable key, source entry and observation time, optional real native
conversation/turn IDs, 1–3 proposed items and 1–16 selected evidence excerpts.
An envelope is at most 64 KiB; excerpts are at most 8 KiB and user statements
at most 4 KiB. Unknown JSON fields and oversized submissions fail explicitly.

A statement must equal one cited user excerpt exactly. `internal/host/external_memory.go`
uses the existing memory Decider for category, durability and comparison. Author
identities are retained as external attributions, not authenticated channel senders.
Assistant summaries cannot impersonate user statements.

A procedure supplies a bounded `ExperienceDraft` with conditions, steps and
evidence IDs. References must resolve to supplied tool call/result pairs with
explicit error status. Existing Jev assessment checks support and durability;
the existing Judge compares eligible proposals against current memory. The
assessment includes explicit external provenance. No commands in these excerpts
are executed. Failure lessons remain bounded by what their cited results report.

The Host reuses its current comparison, conflict quarantine and blacklist policy.
The current bounded comparison policy and semantic equivalence remain
model-dependent. Provider errors produce failed items without weaker admission.
Imports cannot mark themselves accepted or expand access.

## Publication and recovery

Intake persists the selected source envelope and a pending receipt before returning
`ingestion_status=received`. One worker per owning Host processes pending receipts
on startup and after intake. `processing_status` is separate from each item's
`pending`, `published`, `excluded` or `failed` result. Partial success remains
visible, and provider failure does not replay the business task.

Within a binding, the same key and canonical payload digest returns the existing
receipt. Reusing a key with a different payload fails. Semantic duplicates with
new keys still go through normal comparison and may refer to an existing memory.
The receipt retains that submission's evidence and publication association.

`Store.FinishExternalItem` checks the comparison fingerprint and source binding,
then uses the existing file-memory transaction. New memory has an
`external_receipt_id`, original evidence references, and no `source_run_id`.
The authoritative manifest journals the external receipt update alongside memory
publication. On restart, receipt recovery runs before further operational writes;
a crash after publication cannot cause duplicate publication. Snapshot changes
fail explicitly instead of silently reassessing under different knowledge.

Graceful worker cancellation leaves unfinished items recoverable. A model failure
is terminal for that item; after fixing its cause, the caller can deliberately
submit a new attempt with a new key. Storage failures preserve unfinished work
and are logged. Revoked bindings suppress queued work. This is a bounded worker,
not a general retry scheduler.

Receipt responses omit source bodies, and receipt/read responses omit raw decision
audits; the durable record retains them. Clients look up by receipt ID or original key after a transport
timeout, and only resend the identical envelope/key when necessary. The skill
polls at most six times over 30 seconds before reporting unfinished work.

## Recall, native use and inspection

`memory recall` returns at most ten scoped navigation items. `memory read`
rechecks current eligibility and optional expected revision, and returns the full
record plus supporting retained evidence. Search snippets do not prove a fact.
Conflicted, disabled, expired and foreign-scope records cannot be read this way.
Recall does not expose arbitrary private conversation history.

`ContextMemoryEvidence` supports both native-run and external-receipt sources.
External reads explicitly report `external_agent_supplied_evidence`, original
source/observation times, receipt time, payload digest and cited excerpts. Links
are retained as source references; the Host does not fetch supplied links or paths.
The same eligible memory can be recalled by an existing Host entry sharing its
Agent, audience and scope. Native packages support receipt-based procedure
provenance while retaining each harness's own execution and permissions.

The workbench renders imported sources without inventing an origin run. Its
evidence projection checks scope and shows only the memory's cited excerpts.
Disabling remains available. Native-run restructuring is not offered for imports.
File-backend rollback preserves imported records and their operational receipts;
new external intake remains unavailable on the historical SQLite backend.

## Validation

Automated coverage includes local CLI/socket intake and key lookup, independent
same-scope recall, foreign-scope and revoked-binding denial, changed-payload key
conflicts, publication recovery after simulated SQL rollback, cache rebuild,
blacklist/conflict handling, partial publication, provider failure, unsupported
evidence, native procedure packaging, workbench projection and rollback.

Run `make check` and the skill-creator validator. Live native-entry acceptance
is recorded separately from these deterministic checks; general model quality,
remote access, group sharing and transcript synchronization remain outside this release.

### Bounded live acceptance, 2026-10-15

The existing local file-memory Host upgraded from schema 14 to 15 and both
DingTalk and Feishu reconnected. A fresh Codex evaluation context followed the
installed skill to submit an exact standing user language preference. Real Jev
selection published it as an active preference with retained external evidence.
A second fresh Codex evaluation context independently searched, read, and answered
from that memory. The existing Host CLI search found the same record, and the
live workbench returned HTTP 200 with its external evidence projection.

The local skill is linked to this source checkout, and the default connection
targets the existing owner-private default Agent scope. Checkpoint collection is
off. This proves one real statement round trip and fresh-context recall; it does
not establish general procedure quality or a separate end-user chat's behavior.
