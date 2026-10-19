# Memory consolidation and execution experience details

Status: automatic experience publication and bounded blacklist admission checks
are implemented for migrated file-memory runtimes on 2026-10-13. Legacy SQLite
runtimes retain manual acceptance. The 2026-10-10 reviewed loop remains historical
validation. File publication, recovery and current receipts are described in the
[file-memory details](file-memory-design-detail.md). The
[main design](experience-memory.md) governs scope and the user workflow. The
[earlier MVP details](mvp-validation-detail.md) describe the underlying event,
Claude execution, Jev transport, and user-statement contracts. This document
extends those contracts for execution experience.

## Revised publication and blacklist policy, 2026-10-12

The required human acceptance gate is removed from the target architecture.
Jev admission, relation decisions and Host validation determine publication.
Human inspection remains optional; an explicit disable controls subsequent use.
The remaining legacy transaction/protocol sections document the manual-review
baseline and historical validation. File-memory tasks now reuse those validation
boundaries with automatic post-run comparison/publication and explicit recovery.

The smallest transition reuses `finalizeExperience`, `Judge.Assess`,
`Judge.Compare` and the transactional publication rules currently reached through
`AcceptExperience`. After assessment, finalization compares a source-linked
proposal against the current permitted memory snapshot and publishes it without
waiting for a CLI review. The existing probability and evidence policies remain
in force; removing human acceptance does not lower those gates. User statements
already use automatic `FinalizeMemory` and adopt the same exclusion policy.

| Decision | Target effect |
| --- | --- |
| Useful, supported, independent proposal | Publish effective memory automatically. |
| Equivalent effective experience with compatible provenance | Add evidence to the canonical record without creating another recalled copy. |
| Unsupported, low-confidence or unresolved proposal | Retain the decision/evidence and report exclusion; no human approval prerequisite. |
| Conflict or uncertain relation | Preserve the existing quarantine behavior and explain exclusion; do not infer a newer winner. |
| Equivalent blacklisted lesson | Preserve the exclusion, including when the proposal has another ID or wording. |
| Model, validation or transaction failure | Report failed processing; retry only saved evidence, never business execution. |

Treat the blacklist as durable scoped exclusion, reusing the existing
`invalidated` memory lifecycle where suitable rather than introducing a second
memory authority. Its canonical identity, reason, actor and revision history
must survive index rebuilds. Jev relation selection cannot clear an explicit
disable. Duplicate evidence, later extraction, retries and migrations cannot
reactivate it implicitly. A distinct corrected assertion may become effective
with its own evidence and an explicit relation to the disabled record; a mere
paraphrase is not a correction.

Current `Store.Memories` and comparison snapshots exclude invalidated records.
That suffices to remove them from recall but does not provide semantic
re-admission protection. Admission must also consult scoped exclusion anchors
and canonical duplicate identities. These anchors are governance input, never
recalled knowledge or authority supplied by source text. Keep project, Agent,
audience, subject and applicability boundaries: disabling one member's preference
must not disable an independent preference from another member. Do not use a
global keyword blacklist. Detailed schema and command choices belong to implementation.

Persist the publication outcome, decision audit, evidence links, canonical
identity, memory revision, indexes and processing receipt atomically. Validate
the compared snapshot and current exclusions at commit; stale snapshots require
reassessment, not a successful save. Interrupted finalization must be retryable
without duplicate publication. Distinguish machine selection from historical
human review in audit metadata while preserving existing source provenance.
Do not rewrite old `reviewed_experience` as independently verified truth.

Search, applicability resolution and context commit enforce exclusions even if
a derived index is stale. Disabling removes derived recall entries while keeping
history inspectable. Previously injected text may remain in resumed native
history; the existing withdrawal notices must cover newly disabled versions.
Expiration, conflict quarantine and detected source withdrawal remain distinct
exclusion reasons, independent of the explicit blacklist. No source may change
execution authority through either publication or restoration.

The next acceptance loop must demonstrate automatic publication with no
`memory accept`, fresh-task reuse, disable/exclusion, equivalent re-extraction
remaining disabled, conflict handling and interrupted-finalization retry without
task replay. The retained 2026-10-10 reports validate only the old reviewed loop.

## Component responsibilities

| Component | Responsibility |
| --- | --- |
| `internal/harness/claude` | Normalize native tool calls/results; preserve native completion, permissions, streaming, and structured extraction output |
| `internal/experience` | Tool-free generative extraction, shape/source validation, bounded Jev screening and relation decisions |
| `internal/host` | Sequence evidence, extraction, screening, review and receipts; independently retry from recorded evidence |
| `internal/state` | Journal linkage, atomic candidate review, memory revisions, source isolation, expiration, invalidation and schema migration |
| `internal/assembly` | Inject current eligible memory with conditions and provenance; identify withdrawn previous context |
| `internal/cli`, `internal/terminal` | Review commands and live text/NDJSON views of the same event structure |

The native task invocation and extractor invocation have distinct session IDs.
Extraction is nonpersistent and uses Claude safe mode with business tools, MCP
and skill commands disabled. The native `StructuredOutput` formatter is allowed
only to return the requested schema; its raw protocol data is not business tool
evidence. Managed policy remains authoritative, and managed hooks may still
apply. This is not a guarantee that every native hook is suppressed. Extraction
never resumes the business task or recursively creates another extraction job.
Its task source is the recorded request, assistant context and normalized tool
observations; assistant prose cannot establish tool execution.

## CLI additions

Options precede positional IDs. The existing `--home`, `--workspace`, model,
provider and output options continue to apply.

```text
agent-host memory candidates [--run RUN_ID] [--status pending|accepted|rejected|all]
agent-host memory candidate CANDIDATE_ID
agent-host memory accept [--note TEXT] [--expires-at RFC3339] CANDIDATE_ID
agent-host memory reject --reason TEXT CANDIDATE_ID
agent-host memory invalidate --reason TEXT MEMORY_ID
agent-host run retry-experience RUN_ID
agent-host chat [--allowed-tools PATTERNS] [-m MESSAGE]
```

`--allowed-tools` accepts repeated options or comma-separated native tool
permission patterns. This is an explicit user configuration passed to Claude;
retrieved memory never modifies permissions. It is incompatible with
`--no-tools`. It does not replace native permission checks or enable bypass mode.

Candidate inspection includes the draft and cited tool evidence. Listing and
inspection require no model credentials; acceptance calls Jev to compare the
stored candidate with current memory. Reject and invalidate are local review
operations. Candidate listing defaults to `pending`; `--status all` includes
completed reviews. An acceptance expiration must be a future timestamp; it
comes from the reviewer, not a model-generated field. Rejection and invalidation
require a reason.

`run retry-experience` handles completed executions whose extraction is pending
or failed and whose user-memory result is already `saved` or `unchanged`.
Unresolved user-memory processing must first use `run retry-memory`, which
completes that finalization and then pending/failed experience processing.
Neither replays the business operation. An extraction failure remains an
explicit error even when the earlier streamed task answer has already been
displayed.

## Evidence and identity

Each normalized `ToolObservation` retains the native ID and name, a `call` or
`result` stage, JSON input/output when present, and the native `is_error` value.
`ToolEvidence` pairs a unique call ID with its following result and retains
`call_seq` and `result_seq` references into the durable run journal. Missing
result content remains absent; it is never replaced by invented successful
output. Error results remain error evidence.

Only events in the native `execute` phase qualify. Extraction observations use
a separate phase and cannot support their own candidates. Unknown/duplicate
IDs, missing result observations, name mismatches, nonincreasing sequences and
malformed JSON cause errors. No process ID, file timestamp, assistant claim,
tool name or input command alone proves success.

Entity derivation is deliberately narrow: canonical project scope plus
`file_path`, `filePath` or `path` string values in cited tool input. Existing
symlinks and existing ancestors are resolved before checking containment.
Outside-project paths are excluded. Generated prose and shell command strings
do not create file identities. These `Memory.Entities` values remain evidence
anchors. The later [object retrieval extension](object-retrieval.md) adds separate
derived annotations from accepted memory text; it does not change tool-evidence
identity, source type or the review requirement.

## Candidate and decision contract

Experience comparison retains each selected memory's claims, identity, revision,
provenance and applicability. Prior `revision_decision` and `object_audit`
envelopes stay on the original record rather than being recursively embedded in
the new model input. The 48 KiB comparison-state limit still applies to the
remaining complete records; oversized claims fail explicitly without truncation.

A draft contains `title`, `summary`, `conditions`, ordered `steps`, and exact
`evidence_ids`. Extractor output is decoded strictly; missing/unknown fields,
malformed JSON, out-of-bound fields, and unknown/repeated evidence references
are rejected without repair or prose fallback.

| Limit | Value |
| --- | --- |
| Proposed experiences per run | 3 |
| Serialized extraction source | 128 KiB |
| Title / summary / conditions | 256 / 4096 / 2048 bytes |
| Steps | 1–12, each at most 2048 bytes |
| Evidence references per draft | 1–32 distinct observed tool IDs |
| Encoded Jev decision state | 48 KiB |
| Existing eligible records in comparison | 100 |
| Recall context | Existing limit of 8 records and 12 KiB text |

Exceeding a processing limit fails explicitly; source/comparison data is not
silently truncated. Recall has a bounded context budget and is not exhaustive
retrieval. These application limits are not SQLite capacity measurements.

User-statement policy `user-statement-v4:attributed-actors` compares declarations with both source
types. Equivalent text across source types is kept distinct; a conflict can
quarantine either source without changing its provenance. The original v2
measurements remain historical evidence, not a v3 quality evaluation.

Extraction policy `reviewed-experience-extraction-v2` favors a short observed
read/check/change/test sequence. It excludes assistant-supplied library
guarantees and untested boundary claims even when phrased as limitations.
This narrowing followed the retained first live attempt; the judgment policy
and `0.8` thresholds did not change.

Jev independently screens durability and support against the cited results.
Both must select the eligible answer at probability at least `0.8`. Unsupported,
uncertain and low-probability proposals are retained in decision audit data but
not promoted to pending review candidates. The threshold is a policy choice,
not measured accuracy. A useful negative lesson may cite observed failures;
failed operations cannot support claims of successful completion.

Human acceptance compares the complete stored procedure and conditions with
all eligible active/conflicted memory in the same scope. Relations are `same`,
`conflict`, `independent` or `uncertain`. Uncertainty or low probability
quarantines the implicated records and new record. No decision chooses a newer
winner or infers temporal ordering. Semantic deduplication links evidence only
to another active reviewed experience; a user statement cannot acquire tool
provenance through a duplicate decision. Conflicts between user statements and
reviewed experience are quarantined in either direction while preserving each
record's original source type.

Jev transport remains explicitly selected. The default is the official
`https://api.typesafe.ai/v1/systemone` with `TYPESAFE_API_KEY`.
`--jev-provider custom --jev-endpoint URL` is an explicit alternative requiring
`JEV_API_KEY`; no credential is forwarded across providers. There are no
automatic retries, redirects or provider fallback. The retained audit includes
policy, prompt/schema, model, typed model output, assessments and comparisons.
The client does not add authentication headers or configured API keys to audit
records. Raw source text and tool output are persisted, however, and may contain
secrets supplied by users or tools; this iteration does not provide general
source-secret detection or redaction.

## Persistence and lifecycle

Schema version 2 adds review candidates and a memory expiration column.
The existing scope/status index remains; the expiration column itself is not
indexed. Writable open migrates version 1 in a transaction while preserving
existing records. Read-only version 1 inspection reports a migration requirement
instead of modifying the database. Legacy memories with absent `source_type`
remain attributed user statements; they are not relabeled as experience.

| Independent state | Values / meaning |
| --- | --- |
| Run execution | `running`, `completed`, `failed`, `canceled`, `unknown`; native task outcome |
| Business acceptance | Remains `unverified`; experience review does not certify the task |
| User memory | Existing `pending_execution`, `pending`, `saved`, `unchanged`, `failed`, `skipped` |
| Experience processing | `pending`, `failed`, `empty`, `review_pending`, `reviewed`, `skipped` |
| Candidate review | `pending`, `accepted`, `rejected` |
| Durable memory | `active`, `conflicted`, `invalidated`, with optional expiration |

Native completion atomically sets both user-memory and experience processing
to `pending`. User-memory finalization happens afterward in a separate
transaction. Failed/canceled/unknown native executions do not
automatically promote experience. No-tool or no-eligible-proposal runs finish
as `empty`. Extraction failure is independently retryable; pending review or
failed extraction does not block native session continuation once the existing
user-memory finalization requirement is satisfied.

Candidate save validates the completed run, scope, source references and shape,
then commits all candidates and extraction audit/status together. Acceptance
revalidates references and the exact compared memory ID/revision snapshot in
one transaction. Changes to that snapshot require review again. It records the
review, source candidate, evidence IDs, conditions, steps, entities, revisions,
FTS changes, derived annotations/terms and outcome atomically. Schema v3 adds
those derived retrieval rows without rewriting accepted experience or its
review evidence. Accept/reject operations cannot replay a
finished review. All candidates being reviewed changes the run to `reviewed`.

Pending/rejected candidates never enter the memory index. Active reviewed
memory uses `source_type=reviewed_experience`, distinct from
`user_statement`. A duplicate preserves its canonical source while adding the
new run as evidence. Exact text already belonging to a revoked, expired or
differently sourced record cannot silently revive that record.

Search, model comparisons and context saving exclude expired and invalidated
records; conflict records are excluded from recall. Invalidation appends a
revision, removes the FTS entry and rebuilds the affected scope's derived terms.
Recall resolves aliases from currently eligible records and filters both object
and property applicability through `Store.Retrieve`. History remains readable
by ID. Context
commit validates the complete stored memory snapshot, including provenance and
expiration, not just its text. Native resumed history may still contain older
injections; revision/withdrawal notices do not claim to erase that history.

## Event additions

The versioned event envelope is unchanged. Additional phases are `evidence`,
`extract`, `assess`, `candidates`, `review` and `publish`. Events include paired
tool evidence, extraction output, Jev assessment, pending candidate IDs,
comparison decisions and accepted memory. Extractor observations identify
`memory-extractor` and its resolved model separately from the executing agent.
Phase start/progress/finish events and phase duration retain existing semantics.
The text renderer limits each tool preview to 320 characters; full tool data
remains available in the structured JSON event and durable journal.
Acceptance and retries append to the same run sequence without replaying the
task. Local rejection and invalidation return the persisted review or memory
record; their reasons remain in that record's history.

## Validation status and limits

The passing live report (private validation evidence)
used native Claude Code `2.1.223`, `claude-opus-5-5` for execution/extraction,
and company Jev `jev-latest`, returned as `typesafe/jev-1.13-20260917`.
The test used a temporary Go project and isolated host state. Resume reused the
original native configuration directory; recall checks used fresh native state.
The literal, separately authorized `llm` alias environment was parsed without
executing the alias or adopting its bypass flags.

| Live check | Observed result |
| --- | --- |
| Native execution | Read implementation/tests, observed failed `go test`, applied an edit, then recorded passing `go test` |
| Independent verification | Original test contents stayed unchanged; external `go test ./... -count=1` passed after each of two repairs |
| Native resume | Same native session recovered a conversation-only random marker, absent from host memory |
| Experience review | Jev durability `0.96`, support `0.86`; explicit CLI acceptance produced active reviewed experience |
| Fresh task | New native/host session received and cited the accepted memory while fixing a similar second function |
| Invalidation | Another fresh session received neither the invalidated memory nor the second run's pending candidate |
| Observability | Text streamed before process completion, contiguous journal sequences, per-phase timings and named model roles retained |

The first repair took 83.03 s overall: native execution 51.09 s, extraction
26.85 s, Jev assessment 2.57 s. The similar task took 117.79 s overall:
execution 100.96 s, extraction 13.67 s, assessment 0.89 s. These are individual
observations, not latency percentiles or a memory productivity comparison.
The initial acceptance had no existing memories to compare, so its relationship
check completed locally; nonempty conflict/deduplication comparisons have
controlled automated coverage, not a new live accuracy benchmark here.

Review was a scripted synthetic reviewer: inspect saved source evidence, choose
a candidate citing successful edit/test observations, and explicitly invoke
`memory accept`. It does not prove human review quality. Business acceptance
remained `unverified`. Rejection, expiry, conflict, interrupted execution and
memory-only retry are tested separately with controlled fixtures.

The retained first attempt (private validation evidence)
passed actual repair and native resume but failed the overall loop because no
candidate was admitted. Its generated procedure added technical claims beyond
the observations; Jev chose support with probability `0.74`, below `0.8`.
Extraction v2 was narrowed afterward, while Jev judgment and thresholds stayed
unchanged. The passing second run is a post-hoc integration validation, not a
held-out accuracy improvement. No failed report was overwritten and neither
runner performs automatic retries or provider substitution.

`make check`, `go test -race ./...`, the focused extraction race recheck after
the prompt change, `make build`, Python syntax, Markdown links and credential
checks passed. Reports retain tested binary hashes, original evidence,
assessments, receipts, timings, and independent test output.

Automated tests use SQLite databases, native protocol fixtures and bounded
decision responses to check pairing, source isolation, transaction rollback,
review gates, stale snapshots, conflict quarantine, migration and recovery.
The automated coverage includes:

| Boundary | Proof |
| --- | --- |
| False execution success | Native nonzero/missing/malformed result, cancellation and descendant termination fixtures |
| Source isolation | Paired native IDs/sequences; missing content preserved; extractor evidence cannot cite itself |
| Review and recall | Pending/rejected/conflicted/expired/invalidated records excluded; accepted procedure recalled in a fresh session |
| Retry | Failed extraction and user-memory finalization recover without another business dispatch |
| Atomic storage | v1 migration preservation/rollback; candidate transaction rollback; exact revision/scope checks |
| Review CLI | Inspection, rejection and invalidation without credentials; cross-workspace disclosure/mutation denied |
| Provenance | Declarations and procedures stay distinct through deduplication and bidirectional conflict quarantine |

These validate implementation behavior, not model semantic accuracy. The
[first live experiment](../experiments/mvp/README.md) remains historical evidence
for user statements only; it disabled tools and does not validate this new loop.

The delivered manual-review version does not claim autonomous promotion,
general entity extraction, vector retrieval, an automatic conflict winner,
broad correctness or large-memory performance. Its policy requires explicit
review. The revised policy above replaces that gate with automatic selection
and later disabling; generated procedures and Jev decisions can still be wrong.

## Successful procedures and avoidance notes

Extraction policy `reviewed-experience-extraction-v3` emits recommended successful
`steps` and optional `avoid` notes. The latter is bounded to eight nonempty items
of at most 2048 bytes each. Failed attempts must not become instructions to
reproduce the failure; their observed warning and narrow integration conditions
belong in `avoid`. Judgment policy v2 screens this distinction and includes
avoidance notes when comparing complete procedures.

Both candidates and published memories preserve these fields; `ExperienceText`
includes an explicit `Avoid:` section for context delivery. Existing payloads
without `avoid` remain readable without a schema migration. New extraction does
not silently rewrite older adopted records.

The local owner can explicitly restructure an active reviewed procedure.
`Host.RestructureExperience` uses only that memory's existing evidence IDs from
its source run, invokes tool-free extraction, requires one lesson, and reassesses
its usefulness and support. Observed tool failures require an avoidance note.
Extraction and assessment outcomes remain in the source journal. A revision
transaction verifies current status/revision and exact allowed source references,
persists the new text, steps, warnings and decision audit, and rebuilds derived
retrieval indexes. Identity, source run, original candidate and evidence IDs stay
unchanged; old revisions remain inspectable. Disabled, expired, conflicted or
stale records are not reactivated by this operation. The original business task
is never rerun. This restructures content; relevance filtering is unchanged.

### Live procedure restructuring (2026-10-13)

The adopted owner-private DingTalk lesson was reorganized through the running
Host from revision 1 to active revision 2. The new version contains four
successful steps (resolve the recipient, send with the verified openDingTalkId,
retain the pending task ID, query final send status) and a separate warning
against the observed rejected userId destination. The original accepted draft,
revision 1, memory identity and five evidence references remain preserved.
Tool-free extraction and the v2 usefulness/support assessment passed. An earlier
attempt that split the lesson into two drafts was rejected without changing
memory; restructuring now explicitly retains one complete adopted goal.

Revision audits remain available in the store and workbench, but context
assembly excludes those audit payloads from the native task prompt. Successful
steps and avoidance notes remain in context. This validation does not establish
improved unrelated-query exclusion; retrieval relevance remains separate.

A fresh, isolated native session recalled revision 2 with its avoidance note and
explained the four successful steps separately from mistakes to avoid. Native
tools were disabled and the recorded run contained no tool calls. The live
workbench displayed the successful revision and separate avoidance section;
`make check` passed. Private validation reports and screenshots remain outside
tracked documentation.
