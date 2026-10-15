# Object-aware retrieval implementation

The [main document](object-retrieval.md) defines user-visible scope. This is a
bounded addition to the existing evidence and memory lifecycle.

## Ownership and timing

- `internal/annotation` owns deterministic extraction, scoped identity linking,
  topic normalization and applicability rules. It does not depend on storage,
  native harnesses, Jev or fixture IDs.
- `internal/state` owns schema migration, atomic derived-index maintenance and
  the union of actual SQLite retrieval routes.
- `internal/host` calls retrieval before native dispatch, saves the supplied
  memory snapshot and journals query/selection observations.
- `internal/evaluation` uses this same retrieval and context assembly, including
  normal state transitions when seeding fixtures.

User statements are annotated only when the existing finalizer admits and saves
them after completed execution. Reviewed procedures are annotated when accepted
as durable memory. Pending candidates do not become a lookup registry. Query
annotations are produced on every retrieval before context assembly. No new
model call is introduced on this path.

The existing `Memory.Entities` field remains a reviewed experience's project
and file evidence anchors. Semantic retrieval annotations are separate derived
rows; a user statement cannot claim experience provenance through them.

## Representation and rules

The current rule version is `entity-topic-v3`. V3 adds explicit context objects
with scoped IDs and user-evidence provenance in `Memory.Objects`. New external
items cite a subject excerpt; `memory associate` updates an active record using
an expected revision and normal atomic publication. It cannot change the original
statement, admission evidence, scope or lifecycle status. Exact object-name
presence validates source shape, not the semantic truth of a caller assertion.

Context mentions use negative offsets and `span.source` to reference separate
evidence instead of pretending the name occurred in the memory statement. Derived
indexes include these IDs even when the original statement omits the name. A
single explicit subject can bind otherwise unbound properties; several subjects
do not receive guessed property relationships. Active canonical names also
participate in bounded name resolution; name collisions abstain. This is explicit
context support, not general model-based entity extraction or pronoun resolution.

The P0 native acceptance exposed
that a readiness "success token" was incorrectly filtered as an authentication
credential. V2 excludes specifically named success/readiness/liveness response
tokens from credential classification, while a separate bearer/access token
retains its type. Writable startup rebuilds outdated annotations/indexes by
Agent/audience partition without rewriting source memory or revisions.
The original retrieval evidence below remains historical; P0 checks this new
case and real fresh-session recall separately.

V3 validation covers subject-free statements, object/property isolation, name
collisions, unchanged source text, stale revisions, scope/partition boundaries,
invalidation, file-cache rebuild and external submit/associate/recall/read. Local
runtime read-back verified two existing facts resolve to one user-confirmed
system object. This does not establish general entity-extraction accuracy.

An annotation retains object mentions,
explicit aliases and topics, each with source text, UTF-8 byte offsets and a
method. Object IDs derive from project scope, object kind and canonical spelling.
They do not merge identities based on fuzzy similarity. Topic-to-object links
preserve supported subject/property relationships.

Explicit Chinese service names ending in 服务 and supported Latin service-name
forms establish source mentions. Grammatical prefixes and conjunctions are
handled only by bounded rules. A compound name such as 历史订单服务 must remain
different from 订单服务. A known alias substring inside a longer name does not
identify the shorter alias's object.

Positive parenthetical declarations such as 订单服务（简称交易） add alias evidence.
An alias is resolved only from the current active, unexpired, same-project
registry. Collisions remain ambiguous. Invalidated or conflicted declarations
cannot supply current identities. Stored alias-only statements are associated
through that same registry, without changing their original text or revision.

Supported property topics include database, port, timeout and owner. Credential
rotation and period expressions bridge bounded Chinese/English wording, while
preserving digital credential subtypes and the physical-key domain. These
annotations describe applicability; they do not prove a stored claim true or
turn a question into permission to execute an action.

Unknown explicit service names remain `unknown`; competing aliases remain
`ambiguous`. Unsupported grammar is a coverage limit, not an inferred complete
parse. Negation, arbitrary conditions, all anaphora and general event semantics
are not solved by the finite rules.

Supported subject forms include `订单服务`, `order-service`, `order service`,
and registered whole aliases followed by whitespace, punctuation or 的. Rules
bind a following property to its preceding explicit subject, including a direct
conjunction such as `订单服务和账单服务的数据库`. Property-before-subject
wording and cross-sentence subject inheritance are not supported. Name prefixes
such as 在途 and 和谐 are not automatically removed.

Unregistered bare names have no object marker. After every 交易 declaration is
withdrawn, `交易 数据库` can still select database-topic records because the
query carries no identified object. `交易服务的数据库` instead carries an
unknown explicit service and is filtered. The withdrawn-alias regression uses
the explicit-service form; it does not prove safety for every unknown name.

## SQLite migration and index lifecycle

Schema v3 adds separate `memory_annotations` and `memory_terms` tables. Writable
open upgrades v1/v2 stores and backfills annotations transactionally from saved
text. It does not rewrite memory payloads, immutable revisions, evidence links
or review decisions. Read-only open of an older schema fails with an explicit
migration requirement. Index errors do not silently activate lexical-only mode.

Writing or changing a memory updates its annotation and rebuilds the affected
scope's derived terms in the same transaction. This preserves alias addition
and withdrawal across other records. Reads re-resolve against currently
eligible records so expiry does not keep an alias authoritative. Current alias
resolution is a declared retrieval route alongside indexed objects/topics and
full-text lookup.

The registry remains bounded to 200 eligible memories per scope, with an
explicit error above the limit. Native chat additionally retains the existing
100-memory Jev comparison limit. Rebuilding scope terms on writes is a bounded
MVP tradeoff, not a large-database performance claim.

## Retrieval and context

`Store.Retrieve` uses one database snapshot for eligible annotations and
candidates. It unions object/topic index matches, current alias associations and
FTS results, then checks object, property and supported subject/property roles
before applying the requested candidate limit. Lexical retrieval is a declared
companion route for incompletely annotated text, not an error fallback.

Global preferences and constraints without object/topic annotations can remain
persistent. Annotated preferences and constraints need a positive candidate
route and must pass the same applicability checks; their memory kind cannot
bypass object isolation. The
existing context budget still limits output to eight records and 12 KiB of text.
A retrieval candidate and a memory actually supplied to the native agent remain
separate observations.

Chat journals `memory.query.analyzed` and `memory.retrieval.evaluated` before
`agent.dispatched`. The existing `memory.recalled` event and `Run.Context` retain
the final supplied versions. Evaluation reports add `actual.query` and
`actual.retrieval`; `matched_ids` remains lexical matches, while `recall_ids`
contains final context. Candidate decisions expose routes and rejection reasons.

## Validation

The fixed 42-case `memory-v2` suite preserves the original 26 cases as identical
JSON objects and adds 16 recall cases. No expected result was changed to match
the implementation. All three reports use the same suite SHA-256:

| Run | Selected | Passed | Failed | Errors | Not run | Duration |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Before (private validation evidence) | 26 | 13 | 13 | 0 | 16 | 293 ms |
| First implementation (private validation evidence) | 26 | 26 | 0 | 0 | 16 | 332 ms |
| Final binary (private validation evidence) | 26 | 26 | 0 | 0 | 16 | 241 ms |

The final run follows a deterministic tie-order correction and clearer stage
counts. The original baseline returned exit 1; both implementation runs returned
exit 0. The 16 unselected cases concern live Jev decisions; they were not rerun
by regression mode. The historical negative-lesson admission mismatch remains
unresolved, with its original label and Jev threshold unchanged.

The real [native experiment](../experiments/retrieval/README.md) writes three
statements through actual Claude execution and Jev admission, then asks the two
original problem questions in separate fresh native sessions. Both its first
and final-binary report (private validation evidence)
passed all 32 assertions across five commands: exact source preservation,
successful memory persistence, exact injected memory IDs, native citations,
observable query/filter events, and business acceptance remaining unverified.
The credential interval was classified as a constraint by Jev, exercising the
persistent-rule applicability boundary as well as ordinary project facts.
The final live report's binary SHA-256 matches the final regression report.
Native query turns took 15,452 ms and 14,159 ms in that run, including model
execution and post-run admission processing; these are not retrieval latency
measurements. The experiment performs no native tools or external business work.

`make check` and `go test -race ./...` passed (316 tests). Storage checks cover
transactional v1/v2 backfill with original payload/revision/evidence bytes
preserved, rollback, alias addition/withdrawal/expiry, one-character aliases,
filtering before candidate limits, ambiguous stored subjects and index errors.
Host tests exercise the actual SQLite/assembly/event path with test doubles for
the native harness and decision provider; the separate experiment supplies
live integration evidence. The experiment's later failure-report hardening
passed six synthetic protocol/inspection checks without additional model calls.

This is neither an independent held-out accuracy study nor a large-database
benchmark. No native tasks with and without memory were paired to measure
completion rate, tool use or token savings. Generic entity/action extraction
and the documented grammar gaps remain open.
