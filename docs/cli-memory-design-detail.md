# CLI memory implementation notes

Status: broader implementation roadmap. The [MVP details](mvp-validation-detail.md)
and [experience details](experience-memory-detail.md) identify the delivered
subset and deliberate simplifications; this document
is not a claim that general entity/event understanding is implemented. The
[object retrieval details](object-retrieval-detail.md) define the delivered
service/alias/property subset, shared by writes, chat and memory search. The
[main design](cli-memory-design.md) governs product scope and user experience.
Exact Go interfaces and SQL schemas should be defined when implementing the
corresponding step rather than treated as a public API now.
The [revised publication policy](experience-memory-detail.md#revised-publication-and-blacklist-policy-2026-10-12)
is implemented for explicitly migrated file-memory tasks: automatic experience
publication and bounded admission/recall blacklist checks. Legacy SQLite tasks
retain human acceptance. The [file-memory details](file-memory-design-detail.md)
own authoritative storage, publication recovery and current validation; the
SQLite storage and transaction sections below remain historical compatibility notes.

## Command and session behavior

- `chat` creates a host conversation and accepts one message at a time. It
  displays the session ID, project scope, and configured harness.
- `chat --resume <session-id>` continues that host conversation and its saved
  native session when compatible. A missing native session or changed project,
  identity, or incompatible profile is an explicit error, not a silent new
  conversation or replay. A new `chat` invocation starts a new conversation
  while still permitting applicable durable memory retrieval.
- `chat -m <message>` executes one turn, performs memory finalization, and exits.
  It returns nonzero for execution, persistence, or required finalization
  failure. A valid finalization with no eligible updates is `unchanged`.
- During execution, Ctrl-C requests cancellation and records the observed
  outcome. It does not mark external effects as undone. An interrupted memory
  job remains recoverable. At an idle prompt, EOF exits cleanly.
- `memory search` and `memory show` expose durable content and its sources;
  `run show` exposes execution, memory processing, retrieval decisions, and
  injected memory versions. Both chat and standalone `memory search` use the
  delivered `Store.Retrieve` path: scoped object/topic/alias and FTS candidates
  pass applicability checks before selection. Search does not borrow hidden
  conversation context. The broader flow below still contains planned work.
  `run retry-memory` retries only an eligible memory job with sufficient saved
  evidence. It never restarts the business execution.

The first implementation uses one foreground host process, one configured
Claude profile, and a host-home ownership lock. A second process targeting the
same home for execution or recovery reports that it is in use; read-only
inspection can use a separate database connection. This avoids concurrent
workers and a background service until their behavior is needed and tested.
Local owner, workspace, and explicit personal scope are still retained so
knowledge from one project does not accidentally become universal.

## Execution and context assembly

1. Persist the request before starting the harness. Keep host conversation,
   run, execution attempt, and native session identities distinct. Record the
   engine version and effective execution configuration without credentials.
2. Resolve the request and retrieve applicable memory through the
   [structured flow](#structured-memory-retrieval). Assemble selected records
   within an explicit context budget and retain their exact supplied excerpts
   and revisions. No matches is a valid result; a failed processor, query, or
   required index stops the turn with an error. Do not silently continue with
   weaker recall after a failure.
3. Pass the current request, selected memory, and necessary working context to
   Claude through its programmatic CLI. Memory is attributed context and cannot
   change permissions. Native continuation supplies its own conversation
   history; do not also replay the entire host history into the same session.
   Refresh applicable memory for every turn, including a resumed conversation.
   Check revisions of previously injected memories on resume and explicitly
   supply supersession/conflict notices when needed. Earlier injected text may
   still exist in native history; refreshing recall does not erase it. Include
   this condition in correction acceptance tests.
4. Stream text while retaining execution events and explicit observation
   coverage. The adapter converts Claude's protocol; it does not own memory
   policy. Use a matching final result, error indicators, stream integrity, and
   process exit to classify the attempt. Do not treat an intermediate result,
   background-task notification, PID disappearance, or changed file as proof of
   successful completion. Native success does not establish business acceptance.
5. In a short database transaction, record the terminal outcome and, for an
   eligible completed turn, create its memory finalization job. The first policy
   does not automatically promote interrupted or failed attempts into long-term
   knowledge; their evidence remains available in the run record.

Use supported native permission behavior. If an operation needs interaction
that this CLI integration cannot handle, report the limitation rather than
changing the permission mode. The exact version-specific integration and
session-continuation behavior still require real CLI acceptance.

## Structured memory retrieval

The implemented subset uses versioned Go annotations, schema-v3 derived indexes
and `Store.Retrieve`. Eligible statements are annotated at finalization and
reviewed procedures at acceptance; each question is resolved before context
assembly. Source text remains unchanged. See the
[delivered contract and limits](object-retrieval-detail.md).
Under the revised policy, automatic publication performs the same annotation
work. Disabled identities also participate in admission exclusion checks, while
remaining absent from recall and applicable alias resolution.

The remaining design proposes a broader host-owned entity/event processor and
deterministic retrieval. Separate pre-run model calls for query planning and
selection are no longer the default proposal; that earlier approach remains an evaluation
alternative. The existing post-run memory-extraction model call is independent
of this decision. The bounded retrieval subset has fixed-case regression
measurements; general language accuracy and model-assisted retrieval comparison
remain unmeasured.

The first loop retrieves before native execution. A later information need
discovered during tool execution would require an explicit host retrieval
interface; preprocessing does not promise awareness of every future step.

### Share identities and vocabulary across reading and writing

Maintain stable entity IDs, types, and scope-qualified aliases for a small
initial domain: projects, tasks/runs, files, and configured services. Seed these
from host metadata and observed references, not invented text classifications.
Two projects can have objects with the same name. An alias collision stays
ambiguous; string similarity alone cannot merge their identities.

Represent statements and requests using shared object/action vocabulary, while
preserving their different meanings:

| Representation | Information to retain |
| --- | --- |
| User request | Desired response/action, referenced entities and roles, topic action, time, conditions, negation, and unresolved spans |
| Historical event | Actor/object/target roles, action, related run, observed time, reported or observed outcome, and evidence |
| Reusable knowledge | Fact, preference, decision, constraint, or procedure; linked entities/events, applicability, revision, and sources |

For example, asking why a deployment failed has a lookup/explanation purpose
and a deployment topic. A stored failed deployment has an event outcome, not
an instruction to deploy. "Do not deploy" is a prohibition, not a positive
deployment request. Do not reduce these to an identical `intent=deploy` tag.
A single input may contain several clauses with different subjects and modes.

Preserve source text, offsets or event references, extraction method, and
rule/schema version for derived annotations. Keep a procedure with its
preconditions and a claim with its qualifications. Observation time alone
does not establish validity or expiry. Unknown relations remain unknown.

### Implement bounded extraction in the host

1. Obtain scope, current project, run identity, and typed event fields directly
   from the host/adapter where available. Adapter observation coverage remains
   explicit; a successful tool call is not automatically business acceptance.
2. Match known IDs, paths, and scoped aliases. A dictionary/trie matcher and
   patterns for structured identifiers are reasonable Go components. Handle
   overlapping names and alias ambiguity explicitly; Chinese names must not
   depend on whitespace boundaries. Test tokenization separately for FTS.
3. Apply a small, versioned set of clause/context rules for action vocabulary,
   query purpose, time, conditions, and negation. Restrict mappings such as
   "上线" and "部署" to the relevant domain; "发布" can refer to a service or a
   message. Matching an action word alone does not establish an executable goal.
4. Link pronouns and continuation only when bounded conversation/task context
   provides an unambiguous supported target. Retain alternatives otherwise.
   Ask for clarification if unresolved meaning changes the work or its target.

Unsupported wording is an explicit coverage gap, not a fabricated label or a
processor error. Keep the original request for native Claude, and do not claim
it was fully parsed. Operational failures in a configured processor or index
still stop the turn; they cannot silently activate another implementation.
The initial proposal has no automatic pre-run LLM escalation.

During writing, use the same canonical IDs and action vocabulary to normalize
the existing finalizer's evidence-linked suggestions. Deterministic host facts
and model-extracted claims retain distinct provenance. Source checks cannot
guarantee semantic correctness. Removing all model-assisted writing would be
a separate scope decision, limiting extraction to explicit supported patterns
and structured events until a broader extractor is validated.

Rule-based matching is suitable for known dictionaries and constrained patterns;
arbitrary language requires broader coverage and evidence. See
[spaCy's rules-versus-models guidance](https://spacy.io/usage/rule-based-matching)
as a methodological reference, not a proposed Python dependency.

### Retrieve related information and check applicability

The host enforces owner/project visibility and allowed personal scope before
candidate text reaches the native model. Guessed topics cannot widen access.
Only grounded entity identities and supported conditions become exact filters.
Use ordinary SQLite relations/indexes for entity links and structured event
annotations; a graph database is not required by this design.

Retrieve a bounded union of direct references, entity-linked records, and FTS5
results over original words and supported aliases. Full-text retrieval is a
declared companion path, including for partially annotated memories; it is not
a silent substitute after an index failure. Preserve match routes and limits.

Request purpose selects useful memory kinds rather than requiring identical
intent tags. For example, a supported deployment request can need procedures,
environment constraints, and prior lessons; a request about the latest failed
deployment needs the relevant event and associated diagnosis. Traverse only
explicit, bounded relations. Similarity does not establish causality.

Small applicable preferences use scoped lookup. Current progress and failed or
unfinished attempts stay operational and can be inspected through bounded
recent context or explicit run references. The first finalization policy still
does not automatically turn a failed native attempt into durable knowledge; a
later completed analysis can save a supported lesson linked to that evidence.
Full transcript semantic search is outside the initial milestone.

Use deterministic selection based on grounded references, supported conditions,
memory kind, temporal request, evidence status, and lexical rank. Keep inferred
fields as hints when certainty is insufficient for a filter. Current recall
excludes superseded facts; explicit historical questions can retrieve them as
historical. Unresolved contradictions stay labeled, never become a settled
fact merely because one record ranks higher. A match score is not confidence
in factual truth. A successful empty result is legitimate.

Inject a bounded set of records with sources and qualifications. A remembered
capability or prior successful action does not establish current availability
or authority. Record a concise trace in `run show`: matched entities/rules,
unresolved spans, queries, candidate IDs/revisions, selection reasons, and exact
injected context. This separates extraction, linking, and retrieval failures.

Derived matching annotations and FTS indexes remain rebuildable. Version rule
and alias changes so the same query can be reproduced; reprocessing must not
silently rewrite the underlying claims. Semantic corrections require memory
revisions. A future vector index likewise tracks memory and embedding-model
versions, with scope/revision rechecked at materialization. Define readiness
and consistency before enabling it. Do not silently serve lexical-only results
when a configured hybrid index fails. No vector engine is selected yet.

### Evaluate accuracy and coverage independently

Start with roughly 50 representative labeled cases and expand from observed
misses. Include same-name objects in different projects, alias collisions,
multi-entity roles, Chinese short terms, negation, hypothetical plans, historical
questions, pronouns, unfamiliar wording, corrections, and no-memory cases.
Retain examples outside supported rules to expose coverage limits.

Compare plain scoped FTS, structured preprocessing plus FTS, and the earlier
model-assisted query/selection proposal on the same frozen evidence. Evaluate
vectors later for remaining paraphrase misses. Hold candidate/context budgets
constant, record rule/model versions, and separate tuning cases from held-out
evaluation. Measure:

- Extraction and linking precision/coverage, including false identity merges,
  incorrect entity roles, and intended actions mislabeled as observed facts.
- Candidate recall and injected precision; a higher precision obtained by
  omitting most difficult questions is not sufficient evidence of improvement.
- Correct treatment of unknown wording, empty recall, and clarification.
- Scope/revision correctness: cross-scope disclosure or superseded facts
  presented as current are failures, not acceptable average error rates.
- Fresh-session task results, pre-run latency, annotation/indexing cost, and
  the ongoing effort of maintaining rules.

Preprocessing can improve known-object associations but can also amplify wrong
entity links across many future queries. Re-ranking cannot recover a candidate
excluded by an incorrect hard filter. The actual host retrieval path now has a
[same-suite before/after regression](object-retrieval-detail.md#validation):
13/26 before and 26/26 after on the fixed recall set, with original labels intact.
This is bounded functional evidence, not general accuracy, scale or a paired
with-memory/without-memory task benchmark. The separate synthetic Jev experiment
below evaluates only bounded model decisions.

### Jev evaluation option

Jev is a candidate decision component inside the memory path, not a native
execution harness. It does not replace the entity registry, retrieval indexes,
source records, or generative memory extraction. Keep any client private until
an actual integration needs a broader contract. The deterministic first path
remains the baseline; an explicitly selected experiment may use Jev for:

- Choosing among grounded entity or source-span candidates, including explicit
  none/ambiguous options. A single Choice cannot represent all entities in a
  multi-entity sentence; ask per role or use independent candidate judgments.
- Classifying request purpose or memory kind against a bounded vocabulary.
- Scoring relevance of a previously retrieved, scoped memory shortlist.
- Assessing whether saved evidence supports a proposed annotation, retaining
  this as a fallible assessment rather than proof of truth or completion.

Compute scope, IDs, date ordering, revision eligibility, and commit decisions
in Go. Jev can choose a named temporal relation, but code resolves the date
range and event order. No candidate score can override those constraints or
authorize an action. Filter before sending text to the hosted service; record
which excerpts were sent under the selected execution configuration.

Independent judgments sharing the same state can be batched. If retrieval
depends on an entity decision, finish and validate that decision before
building the next shortlist. Independently chosen fields can conflict; code
checks their relationships. Every question needs explicit instructions and
criteria: API question keys are correlation identifiers, not instructions.

Record model version, question/criteria version, candidate set, distributions,
and selected outcome. Calibrate thresholds on held-out Chinese cases; low
certainty means unresolved/clarify as appropriate, not a guessed entity.
Transport or service failures remain explicit failures in the selected mode,
with no silent switch to rules or another provider.

First compare deterministic selection with Jev reranking on identical candidate
lists to isolate selection quality. Then separately compare entity resolution
and classification, which can change candidate recall. Include reordered
options, omitted correct candidates, negation, ambiguous aliases, misleading
source instructions, and same-name objects in different projects. Measure
wrong associations, recall, coverage, latency from the user's region, and
cost. A [synthetic live experiment](jev-evaluation.md) now covers this gateway's
basic decisions and limited reranking. It found no ranking gain over the simple
lexical baseline and exposed a need for precise evidence-policy criteria. Its
small sample is not production calibration or host integration acceptance.
The [research notes](research-detail.md#jev-official-findings) distinguish vendor
documentation from observed gateway behavior and proposed application design.

## Memory finalization

The host schedules finalization after recording execution, rather than relying
on the main Agent to remember to save a note. A separate, bounded, tool-free
Claude invocation can extract structured update suggestions from the saved
request, observed result, available evidence, and relevant existing memories.
It uses the configured provider with no automatic provider substitution. This
internal job does not itself trigger another memory-finalization job.

The host checks source references, scope, update targets, and expected memory
revisions before applying changes. Record the evidence type: an explicit user
preference, a user-reported fact, an observed tool outcome, and an assistant
claim are not interchangeable. Structural validation alone cannot establish
semantic correctness; supported claims retain their qualifications, and
quality is tested against representative cases. Missing required evidence or
invalid extraction output fails finalization instead of inventing support.

Preserve observation dates and source links. An explicit correction can
supersede the earlier value for the same subject and scope. Ambiguous conflicts
remain unresolved and visible; they are not silently merged or injected as a
settled fact. Identical repeated evidence should not create duplicate memories.
The memory policy may explicitly conclude that no durable update is warranted;
persist that reason as a successful `unchanged` outcome.

Commit memory revisions, evidence links, the search-index update, and job
completion in one SQLite transaction. A stable job identity and uniqueness
constraints make retry idempotent: retrying does not apply the same changes
again. Check expected revisions before writing. A receipt is displayed only
after the commit succeeds and the committed records can be read back. A
read-back failure is reported as an unverified receipt and reconciled from the
database; it must not blindly reapply the mutation.

Do not keep a write transaction open during model execution, file verification,
or network requests. Native Agents do not write the database directly. Markdown
exports are views of committed data; a future import/edit workflow must route
changes through the memory manager rather than introduce a second authority.

## Recovery and completion signals

Track execution and memory processing independently. Execution can be running,
completed, failed, canceled, or unknown. Memory processing can be pending,
running, saved, unchanged, or failed; failed/canceled execution is ineligible
under the first finalization policy. User acceptance is separately recorded
when available and is not inferred from either status.

On startup, acquire host ownership and inspect unfinished work. Verify whether
a recorded native process incarnation is still alive before starting anything
else. If ownership or outcome is uncertain, report it for inspection. Do not
infer success or replay business actions. A recorded process identity must
include more than a reusable PID.

Pending or interrupted memory jobs can resume visibly from persisted evidence
after any process ownership is resolved. Deliberately failed jobs require the
explicit retry command. A crash after execution was recorded but before memory
commit leaves a pending job; a crash after commit leaves a completed job and
must not duplicate its updates. A crash before a terminal execution record is
committed may leave an unknown outcome requiring reconciliation.

`Stop` hooks can request bounded in-session continuation and contribute
evidence. `PostToolUse` can identify observed writes. Neither hook completion
nor `SessionEnd` is the authoritative memory receipt: hooks can fail without
failing the turn and abrupt termination can prevent them from running. The
host owns its finalization job and durable result. Hooks are optional for the
first complete loop; avoid global hook configuration changes.

## Storage ownership

Use one local SQLite database with separate logical groups:

| Group | Contents and purpose |
| --- | --- |
| Conversations and runs | Requests, attempts, native identities, observed events, outcomes, and receipt facts |
| Durable memories | Current records, revisions, subject/scope, evidence type, and source links |
| Memory processing | Finalization jobs, retry identity, failures, and completion receipts |
| Retrieval records and index | Entity references, versioned matching annotations, per-run query/selection trace and supplied memory versions; a rebuildable FTS5 index |

Large artifacts remain files with recorded references and content fingerprints.
Evidence supporting a live memory must remain resolvable or explicitly marked
unavailable; log retention cannot silently destroy provenance. Choose concrete
retention and deletion behavior before enabling automatic cleanup.

`internal/host` coordinates runs and jobs; `internal/assembly` selects and records
context; `internal/annotation` owns bounded matching and applicability;
`internal/memory` owns user-statement update decisions;
`internal/harness/claude` owns the CLI protocol; `internal/state` owns database
access, indexed retrieval and shared transaction boundaries. The CLI calls this
local host path directly. External channel abstractions are not a prerequisite for this loop.

## SQLite and growth

SQLite is the implemented local store, not a measured capacity guarantee. Its
documented limits are much larger than the initial workload, but practical
performance depends on queries, text size, storage, and concurrency. It permits
one writer at a time; a client/server store becomes appropriate when deployment
requires multiple machines or write concurrency that cannot queue acceptably.
See [appropriate uses](https://www.sqlite.org/whentouse.html).

Use indexed scope/status/subject queries and FTS5 for text retrieval instead of
unindexed substring scans over every transcript. Only currently applicable
memories participate in default recall. Keep full operational history outside
that recall corpus. Verify actual query plans and latency for common and rare
terms; a LIMIT does not by itself make a broad search inexpensive.
See [query planning](https://www.sqlite.org/queryplanner.html) and
[FTS5](https://www.sqlite.org/fts5.html).

Chinese recall is an explicit implementation requirement. Default `unicode61`
groups contiguous letters rather than performing Chinese word segmentation.
The built-in trigram tokenizer supports substring matching but does not match
FTS queries shorter than three Unicode characters. Select and test a consistent
index/query tokenization strategy, including two-character Chinese terms; do
not ship raw default tokenization as if it solved Chinese recall. This choice
does not require a vector database. FTS5 availability must be verified in the
selected Go SQLite build; missing support is an error, not a silent scan mode.

Use WAL on local storage and keep transactions short. WAL permits readers and
a writer to proceed concurrently, but still has one writer, and long-lived
read transactions can delay checkpoints. Plan checkpoints and backup behavior
for the actual workload. Do not put a live WAL database on a network filesystem.
See [WAL documentation](https://www.sqlite.org/wal.html).

Benchmark 10,000, 100,000, and 1,000,000 synthetic memories with realistic text
lengths, skewed scope sizes, Chinese/English queries, revisions, and unrelated
operational records. These are test sizes, not promised capacity. Measure p50/
p95 recall latency, memory-commit latency, lock waits, database/index size, and
retrieval quality on the chosen driver and machine. Separate database time
from model extraction time. Use results to decide whether indexing changes,
semantic retrieval, or a different database are actually needed.

## Focused acceptance

- Fresh-session reuse: save a scoped preference or supported outcome, start a
  new host and native conversation, and prove that the recorded host-injected
  memory explains the result. Isolate native memory and ambient instructions
  in the test so they cannot supply the answer independently.
- Correction and scope: explicitly revise a fact, verify the earlier revision
  is historical, and ensure unrelated projects do not receive it as applicable.
- Evidence quality: no-op turns create no unnecessary notes; an unsupported
  assistant assertion is not stored as a verified completed action.
- Failure and recovery: exercise query failure, extraction failure, failed
  memory commit, and crashes before and after that commit. No false saved
  receipt, duplicate update, or business-operation replay is acceptable.
- Native execution: validate normal completion, failure, cancellation, native
  resume, and missing-session errors with the installed Claude version. Fixture
  tests alone do not establish live behavior.
- Retrieval growth: run the multilingual quality and performance plan above.
  Compare candidate recall with injected precision and test ambiguity,
  corrections, unrelated-project distractors, and legitimate empty recall.
  No such growth benchmark has been completed; bounded loop evidence is
  recorded separately in the MVP 1 and reviewed-experience documents.
