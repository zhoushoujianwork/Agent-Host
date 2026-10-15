# External agent memory skill: implementation notes

Status: proposed interfaces, not implemented commands. Scope decisions live in
the [main design](external-memory-skill-design.md).

## Current foundation and gaps

Inspected on 2026-10-15, including an existing dirty working tree. Implementation
references describe current source, not a new delivery or validation claim.

| Foundation | Reuse and required change |
| --- | --- |
| `internal/cli/cli.go` | Existing memory search/show and candidate controls; no external submission command. |
| `internal/assistance/mcp.go` | Scoped search/read/evidence patterns; binding requires an active owner Host run and one project. An external entry needs its own admitted binding. |
| `internal/cli/memory_control.go` | Owner-private Unix socket and single Host writer; currently accepts actions on existing candidates/memories only. Add ingestion operations through the owning runtime. |
| `internal/host/experience.go` | Extraction, Jev assessment, and publication are coupled to run processing. Factor reusable admission logic without fabricating a native run. |
| `internal/host/file_memory.go` | Existing comparison and blacklist admission behavior should apply to imports too. |
| `internal/state/file_memory.go` | Reuse authoritative publication and derived retrieval, extending evidence readers for external receipts. |

Do not invoke `chat` as a storage workaround, fabricate a successful Host run,
or call an existing run-bound MCP with an invented run ID. A receipt needs a
first-class external source that survives through candidates, memory records,
evidence reads, and the workbench. Existing run evidence must remain readable.

## Connection and proposed CLI surface

Start with a local Unix socket served by the Host that owns the target state.
The CLI is a transport client and does not open a second writable Store. Reuse
the service's locking and private socket permissions. A stored connection alias
resolves the endpoint and a Host-configured binding to Agent, owner audience,
and one project/default scope. Validate the binding on every operation; request
content cannot override it. Do not infer project identity solely from cwd.

Illustrative command names, to finalize during implementation:

```text
agent-host memory connection show --connection NAME --json
agent-host memory submit --connection NAME --input FILE --json
agent-host memory receipt --connection NAME RECEIPT_ID --json
agent-host memory recall --connection NAME --query TEXT --json
agent-host memory read --connection NAME MEMORY_ID --json
```

Keep existing offline memory commands compatible. Connection setup explicitly
selects a runtime and existing scope; unsupported backend/version fails with a
clear message, without automatic migration or fallback to a different runtime.
Initial recall returns memory, not arbitrary private conversation history.
Read rechecks eligibility and revision, and returns supporting retained evidence
or bounded evidence references. Revocation applies to receipt lookup as well.

## Submission and source semantics

Use a versioned JSON envelope read from a file or stdin, not text interpolated
into shell commands. Suggested fields:

| Field | Meaning |
| --- | --- |
| `schema_version`, `idempotency_key` | Protocol version and stable identity of one logical submission. |
| `source` | Entry kind, external conversation/turn reference when available, and observation time. Missing native IDs stay missing; do not invent them. |
| `items` | Proposed statement or procedure, applicability, and references into supplied evidence. |
| `evidence` | Bounded excerpts, source-local IDs, author/role, timestamp, and optional tool command/result/exit status. |

The Host adds authenticated local binding, receipt time, immutable evidence IDs,
and content digest. Caller-supplied authorship and tool results are attributed
claims. A digest proves the retained payload's identity, not its truth. Label
external evidence explicitly in recall and the viewer. Retain the submitted,
minimized excerpts; do not fetch arbitrary links or local paths from the payload.
Any omitted/redacted source content must remain distinguishable from a complete
transcript. Recalled Host memory is not fresh evidence for re-importing itself.

External agents may propose concise memories, but cannot mark them accepted,
grant themselves scope, bypass a blacklist, or promote evidence strength. User
statements remain attributed declarations. Host extraction/assessment handles
procedures and checks evidence sufficiency, applicability, conflicts and existing
memory. Unsupported claims are excluded with reasons rather than invented proof.

## Receipt and recovery

Persist evidence and a processing job before acknowledging receipt. A receipt
contains separate ingestion and processing results plus per-item publication
outcomes and memory IDs/revisions. For example, `received` means durable intake;
`processing` means selection is unfinished; terminal items can be `published`,
`excluded`, or `failed`. Partial publication must remain visible. Publication
success requires authoritative memory visibility, not merely an extracted draft.

Deduplicate within the admitted binding using the idempotency key and digest.
Same key/same payload returns the original receipt; same key/different payload
returns a conflict. Persist publication associations so a crash after publication
cannot duplicate it during recovery. Semantic duplicates with different keys
still go through normal comparison. Disable/blacklist checks apply on retries.

On timeout, the client queries by the stable submission key before resending
the same payload. It does not generate another key, silently queue uploads, or
replay the user's business task. Host failures remain failed/pending receipts;
bounded polling ends with an honest status and a receipt for later inspection.
Implementation should define concrete payload, item, evidence and polling limits
and return explicit size errors without silent truncation.

## Skill packaging

Proposed source location: `skills/agent-host-memory/SKILL.md` with one linked
`references/protocol.md` for actual command examples, receipt semantics and
payload shape. Prefer the Go CLI for deterministic transport and validation;
add no wrapper script until it removes a demonstrated need for repeated logic.

The description should cover saving durable knowledge to Agent-Host and recalling
it for current work. The body should give only the connection check, recall/read,
selection/evidence, submit, and receipt workflow. Preserve the original task and
permissions. Do not require a transcript scan, task execution by Host, or human
review of every selected memory. Missing connection/CLI should produce a concrete
setup requirement without altering native configuration automatically.

Installation copies or links the source skill into the chosen entry's skill
directory; never overwrite an unrelated existing skill. Publishing learned
procedures as native executable Skills remains Host's existing separate path.

## Acceptance before claiming delivery

- Save an explicit decision from real Codex; inspect its receipt, memory, and
  retained source; retrieve it in a fresh conversation with the same binding.
- Recall it through an existing Host entry sharing that exact scope. Confirm
  a different Agent/project/audience cannot retrieve it through this interface.
- Replay a submission and recover a simulated crash without duplicate evidence
  or publication; reject key reuse with changed content.
- Verify missing evidence does not become verified procedural success; conflicting
  and disabled memories retain the existing admission behavior.
- Exercise partial publication, provider failure, unavailable Host, revoked
  binding, unsupported backend, and oversized evidence without false success.
- Run `make check` and skill validation once implementation and packaging exist.
  Keep automated checks separate from real native-entry acceptance.
