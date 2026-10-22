# Procedural learning: evidence and implementation detail

Status: lightweight MVP implemented on 2026-10-15; broader mechanisms below
remain a proposal unless explicitly listed as delivered. The
[main design](procedural-learning.md) defines scope and delivery order.
The original analysis was read-only. Initial MVP observations use isolated
memory and a local command surrogate. The follow-up below explicitly backfills
one original case and recovers its quarantined records; raw source evidence is
preserved.

## MVP implementation and observation

The MVP adds no schema migration, configuration mode or new service. It reuses
native extraction, the unchanged 0.80 evidence threshold, candidate publication,
scoped retrieval, native Skills and the existing event journal.

Comparison policy v6 splits pairwise memory relations into batches whose JSON
decision states each fit the existing 48 KiB limit. Every eligible record and
its complete claims remain included; stored decision audits are not nested in
new model inputs. All batches are planned before evaluation and retain their
own request/response audit. Publication requires all batches to succeed; later
failure cannot accept earlier partial relations. An individual record that
cannot fit with the candidate still reports an explicit limit failure without
truncation. This fixes catalog growth exceeding one combined comparison state.

- `experience.NativeExtractor.Refine` narrows one withheld draft using only its
  cited observations, in a tool-free fresh invocation with no assistant/request
  context. At most one refinement is attempted per run. A second failed gate
  remains withheld; a provider failure becomes an explicit experience failure.
- Extraction policy v4 requests a complete observed command template, including
  target selectors and quoting. It must not reduce a CLI correction to just the
  changed flag or rely on a previous native conversation for remaining arguments.
- `experience.LearningEvidence` derives a view with known credential fields
  redacted before extraction and assessment. Raw evidence remains unchanged.
  JSON number precision is preserved; this is bounded pattern-based redaction,
  not a general detector of every possible secret.
- Channel assembly takes up to three eligible procedure references from the
  three most recent routed tasks in the same authenticated conversation and
  scope. Explicit native resets stop these history hints. Multi-project tasks
  and CLI conversations do not gain this channel-specific fallback. Current
  memory is rechecked before ordinary indexed retrieval. Only a short request
  with no direct matches uses the titles; no extra retrieval model is invoked.
- CLI Skill preselection and Host dispatch call the same
  `RetrieveWithProcedureHints` path, preserving native runtime compatibility.
- `experience.learning.summary`, `experience.refinement.completed`,
  `experience.refinement.assessed` and `memory.procedure_hints.applied` expose
  narrowing and recall outcomes. Existing publication/manifest/tool events
  provide subsequent evidence without a separate usage table.

`experiments/experience/observe.py` reads a bounded operational sample with a
read-only SQLite connection. It reports learning counts, active/conflicted
publication events, hint matches, recalled procedures, paired native Skill
invocations with exact memory revisions, observed tool errors, and the specific
`tccli --cli-profile` / `Unknown options` recurrence. It does not export task
text, command bodies, results or credentials. Historical publication status is
not a statement of current memory eligibility; process error flags can miss
API errors hidden by a shell pipeline. Explicit Skill invocation is stronger
than mounting alone but does not prove the lesson caused a successful outcome.

Validation uses `internal/host/procedural_learning_live_test.go` with the real
configured native model/Jev and an isolated file-memory Host. Its teaching
runner replays two sanitized historical command/result shapes. Its fresh native
session executes only a local CBS read surrogate, with fixture profile, region
and resource ID. No cloud business operation or channel message is replayed.
The reports live in the ignored `.agent-host/validation/` directory.

| Probe | Observation |
| --- | --- |
| `procedural-learning-mvp-native-20261015-01.json` | Learning published, but native dispatch failed because the temporary fixture path was not canonical on macOS. The fixture was corrected; no provider substitution. |
| `...-02.json` | Real-model learning and Skill invocation passed; direct recall 0, hinted recall 1, one lookup. This initial surrogate checked the profile flag; later validation tightened all target parameters. |
| `...-03.json` | Stricter validation failed: three lookup/discovery calls. The Skill avoided the old profile flag but omitted a full command, and the first lookup lacked `DiskIds`. This prompted extraction policy v4. |
| `...-04.json` | Policy v4 passed the stricter probe in 42.1 s: direct recall 0, hinted recall 1, a successful native Skill invocation, one correctly parameterized lookup and the observed `UNATTACHED` response. The surrogate validates the action, profile, region and parsed `DiskIds` JSON. |

Controlled Go regressions additionally cover withheld → refined → reassessed →
automatically published → fresh-session Skill assembly; one-attempt bounds;
provider failure visibility; conversation/partition boundaries; invalidation;
preselection compatibility; source/reference limits; and credential redaction.
The live probes above admitted their initial lesson, so they do not independently
establish live refinement accuracy. These are development observations on a
known case, not held-out general accuracy or a causal productivity experiment.

Opt in with explicit provider settings and a new report filename:

```sh
AGENT_HOST_LIVE_LEARNING=1 \
AGENT_HOST_LIVE_LEARNING_ENV=/absolute/provider-environment.json \
AGENT_HOST_LIVE_MODEL=MODEL \
AGENT_HOST_LIVE_JEV_ENDPOINT=https://configured-gateway/v1/systemone \
AGENT_HOST_LIVE_REPORT=/absolute/new-report.json \
go test ./internal/host -run '^TestProceduralLearningNativeObservation$' -count=1 -v
```

The environment file supplies only the configured provider values to child
processes. Reports refuse overwrite. Observe natural tasks after deployment
before claiming reduced business error rates or adding automatic revision.

The local DingTalk/Feishu runtime was updated at 11:30 Asia/Singapore on
2026-10-15 from source commit `19fd85c`. Fresh channel connection events and
workbench HTTP 200 were verified after restart. Neither channel had an active
task at restart. The rollout receipt and initial natural-task sample are retained
under `.agent-host/validation/procedural-learning-mvp-rollout-20261015.json`
and the corresponding `*-post-rollout.json` files. No natural post-rollout task
had completed at this initial check; isolated probes are not counted as business
samples.

`make check` passed before parallel unrelated development made the shared
working copy temporarily unbuildable. The exact feature tree was then exported
to a temporary snapshot. Its initial parallel check hit existing three-second
Feishu subprocess fixture deadlines; the unchanged full check passed with
`GOFLAGS=-p=1`, followed by a successful build. The deployed binary's SHA-256 is
`56af237ceede8b28a4faafecd1bbf016c129e75bd72e6d8c66846e53901186cb`.
Unrelated working-copy edits were preserved. No test assertion or model-provider
fallback was used to obtain a passing result.

### Original tccli case checked against the running Host

On 2026-10-15 at 13:37–13:39 Asia/Singapore, the user's request to check the real
case was exercised against the running local Host through the installed
`agent-host-memory` skill and its default connection. That connection targets
the DingTalk default Agent's owner-private workspace scope; this result does not
establish recall in another channel or project.

- Normal recall/read returned active revision 1 of
  `2f9a0f9a-1607-433d-a900-225eb770ed3d`, the older invalid-profile recovery
  procedure. Its source is run `8b92358b-34bd-4f98-84eb-a0ca5bb37df4`.
  It does not contain the later `--cli-profile` correction.
- Read-only journal inspection paired the actual failed call/result at events
  931/935 and corrected successful call/result at 941/945 of run
  `85c265b3-f57d-4212-b5c1-4ebca9f82190`. The failed invocation reported
  `Unknown options`; the corrected invocation used `--profile` and returned
  `TotalCount`, `DiskSet` and `RequestId`. No cloud command was repeated.
- Selected original excerpts were explicitly attributed as journal evidence read
  and supplied by external Codex, then submitted through the normal external
  memory interface. Receipt `998756fd-7027-4b07-bbcb-e7b58568696c` completed with
  `excluded/procedure_not_supported_or_durable`: durability 1.00, support 0.65.
- One deliberately narrower proposal retained only the complete parameterized
  command, returned `DiskSet`, and observed failed flag. Receipt
  `14255f90-e610-44a8-9ec2-a0b307e5ca53` was also excluded: durability 0.98,
  support 0.56. Both assessments chose `supported` but abstained because its
  probability was below 0.80. No threshold, receipt, database or publication
  manifest was overridden. This manual historical backfill is distinct from the
  automatic extraction/refinement path for newly completed native runs.
- A subsequent normal recall still returned only the older recovery procedure.
  The exact correction has **not** been published. Saved raw evidence,
  completed receipt processing and active reusable memory remain distinct.
- Independently, the existing opt-in native learning/reuse probe passed again
  in 35.3 seconds on the configured model/provider: direct recall 0, hinted
  recall 1, actual Skill invocation, one correctly parameterized local surrogate
  lookup and `UNATTACHED`. Report:
  `.agent-host/validation/tccli-native-observation-20261015-05.json`.
  It uses sanitized replay shapes and isolated memory, not the rejected
  production proposal. Its passing result cannot stand in for production
  admission or prove reduced cloud-operation errors.

Private submission files and a concise outcome report are retained under
`.agent-host/validation/tccli-real-case-20261015-*`. They are not committed.
The check identifies a real admission gap without establishing its cause; do
not infer a generally calibrated error probability from Jev's scores. Automatic
historical backfill and external-proposal refinement are not delivered by this
MVP. Further changes should use this failed case as evidence and preserve the
existing admission boundary.

### Admission fix and audited production recovery

The follow-up reproduced the original assessment failure using the unchanged
v2 judge and exactly the previously withheld submission: support 0.62. The
extraction policy required complete parameterized commands, but assessment did
not explicitly distinguish faithful syntax abstraction from unsupported claims
about future values. Clarifying this contract changed observed support to 0.81
in the first focused probe and 0.82 in the running Host. The threshold stayed
0.80. This is evidence for a policy mismatch, not an explanation of the model's
internal reasoning or a general reliability estimate.

That production attempt, receipt `7e0c0081-7493-4e07-8190-958fdfd4d6f6`, passed
assessment but quarantined both the new lesson and the old profile-recovery
lesson: comparison chose independent with probability 0.77. A focused unchanged
comparison reproduced the false quarantine at 0.65. The revised comparison
instructions distinguish compatible syntax and conditional recovery guidance
from incompatible advice under overlapping conditions. A real-provider probe
then admitted the original template at 0.83 and judged the compatible pair
independent at 0.94. An invented option, a universal success guarantee and a
failed command presented as successful remained ineligible. A contradictory
procedure also remained quarantined, although its chosen relation was weakly
independent (0.54); this does not prove correct semantic conflict classification.

After these checks, existing `memory restore` commands explicitly recovered the
old lesson to active revision 3 and the new lesson
`10abf12f-e0f7-49b2-b8e1-9f2efd9421a7` to active revision 2. The reason records
the live report's digest and the successful reassessment/comparison. This was
audited local repair under the user's fix request, not automatic publication or
an edit to database/manifests. Failed receipts and previous revisions remain.
Both channels reconnected; ordinary recall/read returned the correction with
its full `--profile`, `--region`, quoted `--DiskIds` template and external
evidence attribution. The console's `/api/memories` returned both active rows.

A final normal resubmission exposed a separate exact-duplicate bug: receipt
`19175bb1-932f-4eb2-8da7-ec1b547063c1` failed with a uniqueness error instead
of reusing the active record. Judgment policy v5 now recognizes literal identity
of the complete reviewed procedure text, conditions, steps and avoidance notes
before asking the model about other relations. The audit identifies this as a
Host identity check without inventing a model response or probability. It
preserves source type, checks other potential conflicts, and never promotes a
conflicted exact match. Publication still checks eligibility, source identity
and the current generation.

The v5 normal repeat, receipt `579fc65c-3303-405d-90f5-c94fc8d80f73`, was
withheld during fresh evidence assessment despite the identical procedure being
active. Exact comparison alone therefore did not make repeat submissions
reliable. The external Host now recognizes an identical active reviewed
procedure before reassessing it and returns an existing-memory reference.
The audit policy `reuse-active-reviewed-procedure-v1` explicitly states that
incoming evidence was not reassessed or merged. Source, scope, lifecycle,
expiration, object context and publication-generation checks remain. New or
changed content still takes the ordinary assessment path. A regression proves
reuse under an abstaining judge, unchanged original evidence/revision, rejection
of changed claims and no revival of a withdrawn memory. One older test expected
fresh rejection for identical already-admitted content; its expectation now
checks reuse, while the new changed-content case retains the rejection check.

At 14:13 Asia/Singapore the final local restart used
`make run RUN_ARGS=--no-browser`; both channels emitted fresh connection events.
Normal submission receipt `0206b799-74ca-4e11-9967-2d032283b6d7` completed as
`published/duplicate_existing_memory`, referencing the existing correction at
revision 2. Its stored audit identifies the reuse policy above. Ordinary recall
and revision-specific read succeeded. `/api/memories` returned exactly one
active copy of that ID, and its original external evidence still pointed to
receipt `7e0c0081-7493-4e07-8190-958fdfd4d6f6`. No source command was replayed.
The final combined-checkout `make check` passed. Reports are retained as
`tccli-active-reuse-rollout-20261015.json` and
`tccli-active-reuse-outcome-20261015.json` in the ignored validation directory.
This verifies known-record reuse and the recovered case, not reliable automatic
admission of every new lesson; the earlier probabilistic abstentions remain
evidence of that limitation.

Focused live checks live in `internal/experience/judge_live_test.go`. Opt in
with `AGENT_HOST_LIVE_ASSESSMENT=1`, the selected submission in
`AGENT_HOST_LIVE_ASSESSMENT_INPUT`, configured environment/endpoint/model via
`AGENT_HOST_LIVE_ASSESSMENT_ENV`, `AGENT_HOST_LIVE_ASSESSMENT_ENDPOINT` and
`AGENT_HOST_LIVE_ASSESSMENT_MODEL`, and a new output filename in
`AGENT_HOST_LIVE_ASSESSMENT_REPORT`. Optional
`AGENT_HOST_LIVE_COMPARISON_INPUT` supplies the selected existing memory for
compatible/contradictory comparison checks. No command is executed or memory
published by these tests; exported results omit private evidence bodies.
Deterministic regressions cover exact active/conflicted records, source type,
other conflicts and truthful audit attribution. Exact staged-tree checks and
runtime receipts remain in the ignored validation directory; unrelated object
retrieval work in the shared checkout was excluded from the build.

The console lookup offload worker failed at its explicit budget limit. Metadata:
exit 1, profile `cc`, model `claude-sonnet-5[1m]`, 8 turns, estimated worker cost
USD 0.293325 (not verified billing or parent cost), measured elapsed 75.393 s,
session `662a7bb8-8f80-4b99-bcef-39f4f958d674`. Its incomplete output was not
used as proof. Local source inspection and the actual console HTTP projection
established visibility and external-source evidence handling.

## Evidence scope and limits

Read-only inspection covered the current DingTalk and Feishu channel state
databases, file publication manifests and selected current memory records.
The focused cases below are DingTalk records. The broader string search found
20 DingTalk runs whose events mention `tccli` and none in Feishu; this is not
20 actual CLI executions because recall, extraction and context also contain
the word. No cloud commands were executed for this inspection.

All times below use Asia/Singapore (UTC+08:00). Private originals remain under
`.agent-host/channels/dingtalk/state/state.db`, primarily `runs`, `events` and
`candidates`. Identify events by `(run_id, seq)`. Do not export whole event or
assessment payloads: nested evidence can contain credentials and private data.
Resource IDs, recipient names and credential values are deliberately omitted.

| Run (start time) | Source and finding |
| --- | --- |
| `63b12eb7-19b8-4f9d-ba6d-c66455d5d259` (Oct 14, 16:34) | Accepted candidate `401463dd-b0e9-4557-a17a-e7a8b6504c45`; active memory `3122ee69-1363-4cb2-9c5c-e7d516552fa9`, CLB lookup by VIP with file-based CLI input. This belongs to a different memory domain from the following CBS cases; do not assume it should cross that boundary. |
| `8b92358b-34bd-4f98-84eb-a0ca5bb37df4` (Oct 15, 10:18) | Request: “看下这个资源情况”. Events 1052/1055/1064: one draft, eligible, then active memory `2f9a0f9a-1607-433d-a900-225eb770ed3d`. The profile-related lesson cites six tool pairs. Its broad recommendation to fall back to the default profile merits narrower conditions; publication is not proof of general correctness. |
| `ddeceb57-0727-4efb-aa04-b4a186fa58da` (10:21) | Request: “对的看下是哪个机器掉下来的 谁创建的”. Events 4/5 show zero initial recall. Event 3641 contains three drafts. Assessments 3644–3646 reject the time-window and JSON-output lessons as `not_supported`; the profile-visibility lesson abstains at support probability 0.54. Run experience status is `empty`, despite substantial attempted learning. |
| `5629927b-c709-4761-9bd9-a8c95be8e465` (10:29) | A notification follow-up, with zero actual `tccli`-bearing execution calls in this run. Events 4/5 show zero initial recall. Events 1660/1666 publish DingTalk candidates as `conflicted`; corresponding memories are `2b0613bf-713c-41f0-8455-9214588b9b8c` and `e687f665-c74c-42cf-b442-b148c7ad304b`. |
| `85c265b3-f57d-4212-b5c1-4ebca9f82190` (10:33) | Request: “回答了 操作吧”. Events 4/5 show zero initial recall. Calls/results 931/935 use `--cli-profile` and report `Unknown options`; 941/945 use `--profile` and return `DiskSet`. Assessment 2741 abstains at support probability 0.72; 2742 withholds a profile-switching deletion lesson at 0.77. Event 2752 publishes a DingTalk discovery lesson as conflicted memory `aed486dd-7174-4ef8-853a-bac5edc4a93f`. |

The syntax comparison held the observed action, region, resource and profile
constant. The larger draft additionally described configuration discovery, so
its score does not isolate the two-command correction. The audit provides
choices/probabilities, not a clause-by-clause explanation. Treat a narrower
draft's likely improvement as a hypothesis to test, not a diagnosed cause.

The file publication manifest under
`.agent-host/memory/sources/67ace226d55bf20bc69de199/current.json` references ten
entries at inspection time. Current records confirm the two tccli procedures
above are active and the three later DingTalk records are conflicted. Manifests
are the publication authority; legacy SQLite memory rows are not a complete
inventory of file-backed memory. Candidate acceptance alone is insufficient.

Initial recall of zero does not prove the native session forgot everything, or
that an on-demand read never occurred. This analysis establishes the Host recall
gap; it does not attribute every repeated error to that gap. Scope differences,
conflict eligibility and lexical coverage must be evaluated separately.

## Delivered implementation to extend

| Component | Verified behavior and boundary |
| --- | --- |
| `internal/state/experience.go`, `EvidenceFromEvents` | Correlates execution-phase calls/results using native tool IDs and sequence. Rejects missing pairs. `ExecutionExperienceEvidence` excludes reference-tool families, Skill calls and discovery tool search from experience publication. |
| `internal/experience/extractor.go` | Completed scoped runs only; full serialized source limit 128 KiB; at most three drafts. Steps and avoidance notes are prose with draft-level evidence IDs. Prompt already asks for successful paths and separate pitfalls. There is no structured command-correction episode or per-claim evidence contract. |
| `internal/experience/judge.go` | Evaluates durability and support, with probability admission checks. Preserves assessment audit. A probability is a model judgment, not measured real-world reliability. |
| `internal/host/experience.go` | Ineligible drafts are skipped; eligible candidates are saved and automatically compared/published for the file backend. No automatic smaller-draft repair after a withheld assessment. Memory retries use saved evidence and do not replay business execution. |
| `internal/host/host.go`, recall phase | Builds the retrieval query from current input and supplements, within eligible projects. Short follow-ups do not necessarily contain tool/action vocabulary. |
| `internal/state/retrieval.go` and file-backed retrieval | Scoped indexed retrieval and applicability filtering already exist. Retain scope, active version, expiry and blacklist checks. |
| `internal/assembly/skills.go` | Builds instruction-only packages from selected active procedures with provenance and conditions. Conflicted records cannot become these Skills. Mounting is not proof of invocation or benefit. |
| `internal/assistance/mcp.go` | Existing `host_context` offers scoped, read-only `search_context`, `read_context` and evidence access. Extend/reuse this entry point instead of inventing another memory service. Verify availability for the intended agent/audience; do not widen owner-private access to groups. |

Failed or canceled overall runs currently cannot enter normal extraction even
if they contain useful complete failure observations. The first increment can
ship with completed runs; bounded learning from terminal failures is a separate
follow-up, restricted to observed negative lessons unless success is present.
The 128 KiB cap should remain explicit; it was not established as the cause of
the focused cases. Episode selection should later bound input without silently
discarding the failure, correction or verification.

## First increment: correction episodes and supported lessons

Derive a small episode from persisted native observations: tool family and
operation, attempted input, observed failure, changed input, resulting output,
and verification when available. Use execution order plus explicit target and
tool correlation. Adjacent calls alone do not establish a correction; ambiguous
pairs remain separate observations. Never execute source text during extraction.

Preserve three outcome dimensions: process/tool wrapper status, application/API
result, and task verification. An observed shell pipeline can return a successful
wrapper status while its text contains an API error; the retained audit case
contains exactly this pattern. Unknown inner exit status remains unknown.
Infer application failure only from recognized result evidence, retaining the
original report and parser version. `RequestId` alone does not prove completion;
an empty result alone does not prove deletion or missing permissions.

Sanitize before any extraction/judgment prompt, then validate again before
publication. Raw historical evidence includes credential-bearing configuration
output, even though the published lesson must not contain secrets. Produce a
bounded sanitized derivative with source IDs; keep original evidence under its
existing local controls. Allowlisted parsers should retain parameter names,
error codes and selected result keys, excluding credential values and unrelated
business fields. Do not copy raw nested audits into fixtures or reports.

Proposed minimum lesson fields, to refine during implementation:

- Tool/service/action and demonstrated environment/version, explicitly unknown
  when not observed; never substitute the current installed version for history.
- Parameterized successful command shape and expected result check.
- Error signature and the demonstrated correction, separate from working steps.
- Preconditions, evidence IDs per claim, source scope, observation time and
  evidence strength (`observed_once`, later repeated evidence).
- Effect category and runtime placeholders; these describe operations and do
  not grant permission to perform them.

For this case, the sanitized command shape is:

```text
tccli cbs DescribeDisks --profile <authorized-profile> --region <region> --DiskIds '["<disk-id>"]'
```

Historical evidence supports `--profile` for that invocation and rejection of
`--cli-profile`. It does not establish every installed version's behavior or
authorize choosing a different account. This document is a historical finding,
not a newly verified reference for the current upstream CLI.

Keep separate records for stable tool syntax, dated environment observations
(profile/region visibility), and task-specific resource state. Reusable command
templates must bind placeholders from the current authorized task. Do not turn
one successful default profile into a general fallback policy. Avoid displaying
credential contents to discover profile names.

After an unsupported or low-probability assessment, allow one smaller-draft
repair using the same sanitized evidence. Remove unsupported optional steps;
split independent claims when useful; do not invent new evidence or lower the
threshold to force acceptance. Re-run the normal judge, scope, duplicate,
conflict and blacklist checks. Limit total drafts and model cost; preserve both
assessments and stop explicitly when still unsupported. Idempotency should key
off source episode, extraction policy and candidate lineage.

Retain `empty` compatibility if needed, but add reason counts that distinguish
no eligible tool evidence, no proposed lesson, unsupported lesson, abstention,
duplicate, conflict and publication error. A conflict needs its competing
records and applicability difference exposed; do not equate it with a usable
successful save. Whether the observed conflicts are valid remains unassessed.

## Second increment: retrieve at the decision point

Keep initial retrieval bounded. Enrich short follow-ups using attributable,
same-scope task references with source IDs, not unconstrained old assistant
prose. Add optional structured filters for tool family, service, action and
error signature to the existing memory lookup. A memory keyed only to the
request's exact wording will miss ordinary business requests.

For the first version, native instructions request an on-demand lookup before
an unfamiliar tool/action and after a recognized failure. The Host provides
eligible lessons and records the lookup; the native harness chooses commands.
This is guidance, not guaranteed interception. A stronger pre-tool check requires
an adapter-specific capability and acceptance test; streaming events alone do
not imply the Host can pause or rewrite an impending command. Do not build a
second model/tool loop in the Host.

Reuse the existing scoped access checks. Cross-project generic tool knowledge
can be added only through explicit sharing policy and sanitized promotion;
neither matching tool names nor a shared filesystem can expand memory grants.
For the first increment, preserve current partitions and diagnose misses.

## Third increment: measured reuse and revision

Persist operational usage receipts in state.db linked to memory ID/revision,
run, source scope and tool evidence. Distinguish offered, explicitly read or
invoked, observed matching application, success/failure and unresolved outcome.
Matching a command alone does not prove memory caused it; record inferred
application separately from explicit native Skill/read evidence.

Track recurrence of the same error under comparable conditions, successful
verification and newer conflicting evidence. A network error should not
invalidate CLI syntax. Version or environment changes should narrow or suspend
the affected condition. Corrections create a new published revision; they do not
erase history or automatically revive blacklisted knowledge. Conflict repair
must compare opposing claims rather than disabling all similar procedures.

Measure first-attempt correctness, known-error recurrence, extra discovery
calls, recall coverage, learning abstentions and end-to-end latency/cost. Use
matched fresh-session fixtures with and without memory to estimate benefit;
do not interpret model confidence, call-count reduction alone or publication
volume as proof of learning. Report small samples as such.

## Acceptance cases and implementation boundaries

1. Reconstruct the sanitized `--cli-profile` failure and `--profile` result from
   saved evidence; the final working steps never prescribe the failed command.
2. Reject unsupported profile-permission claims while preserving a supported
   syntax lesson if the bounded repair passes all normal admission checks.
3. Replay the recorded short follow-ups through recall in an isolated copy;
   record why each lesson was selected or excluded. Check continued and fresh
   native sessions separately.
4. In a stubbed tool execution, invoke the correct syntax on the first attempt
   and retain revision-specific usage and outcome evidence. No destructive
   cloud action is needed to prove syntax learning.
5. Keep API errors distinct from a successful shell wrapper; missing tool results
   and truncated outputs cannot become successful verification.
6. Prove no credential values reach model prompts, persisted learned content or
   evaluation fixtures; preserve only protected source references as appropriate.
7. Test wrong audience/project, blacklist, conflict, expired environment state,
   interrupted publication and repeated processing. No duplicate active lesson
   or implicit business replay may result.

Start in `internal/state/experience.go`, `internal/experience`, and
`internal/host/experience.go`; reuse the file memory publication path. Extend
retrieval annotations/access in the second increment, then usage receipts and
workbench explanations. Add sanitized historical regressions under the existing
experience evaluations. Run `make check` for each implementation increment.

## Investigation delegation record

The two initial analysis offload attempts failed to return a completed report;
the later bounded MVP fixture lookup completed. Important findings were checked
independently through targeted local reads.
Costs below are ESTIMATED worker USD cost, not verified billing or total task
cost; they exclude the supervising agent. A missing cost is not zero.

| Attempt | Status / exit | Profile / model | Estimated USD | Measured elapsed | Session / turns |
| --- | --- | --- | --- | --- | --- |
| Code pipeline | Budget exceeded / 1 | `cc` / `claude-sonnet-5[1m]` | 0.738951 | 109.653 s | `ad2d55a5-d33f-48bd-b7bb-7edeee34fc37` / 20 |
| Runtime history | Timeout / 124 | `cc` / `claude-sonnet-5[1m]` | Unavailable | 240.260 s | Session unavailable / CLI reported 0 turns; completed work unavailable |
| MVP fixture lookup | Completed / 0 | `cc` / `claude-sonnet-5[1m]` | 0.133182 | 59.817 s | `01433a59-05c8-48d2-a99b-6a8f3db2da66` / 10 |
