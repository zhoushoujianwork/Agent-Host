# Answer delivery and closeout: implementation details

The [main guide](reply-closeout.md) defines the user-visible behavior.

## Ordering and ownership

1. The native harness finishes. Host persists its status and complete output
   with `CompleteRun`.
2. For successful native execution, Host emits `execution.ready` with the saved
   output and independent execution, acceptance, memory and experience states.
3. A request-scoped `runcompletion.Handler`, when supplied, attempts delivery
   synchronously. It receives the persisted run and does not modify shared Host
   configuration. Handler failure has its own journal event.
4. Host performs the existing memory decision and experience closeout phases.
   `run.finished` continues to mean the whole invocation has finished.

Closeout remains synchronous after delivery, within the existing owned process.
There is no detached goroutine or new queue. Existing session exclusion remains
held until the invocation returns; room requests retain independent forks.
The existing turn timeout also covers closeout. A crash can leave a completed
run with pending closeout; existing manual retry reads retained evidence and
never repeats native work.

## Entry points and failures

The channel service records the completed message and exact reply text before
attempting delivery. Its request-local attempt state prevents the post-run tail
from sending again, including after failed or unknown delivery. Routing and
session-renewal notices are carried into the early reply. Alternate Hosts that
do not invoke the handler retain the post-return fallback.

Closeout errors stay in the stored run and `channel.closeout` observation.
They do not rewrite the already recorded message or append another user reply.
Native execution failures do not invoke the early success handler.

Owner-answering parses the native outcome before handing it to the existing
coordinator save/validate/deliver tail. All identity, freshness, blocked-outcome
and delivery-reservation checks remain there. Raw Host output is never sent to
the peer by the hook.

Verified `notify` feedback may use `execution.ready` before `run.finished`.
Origin feedback keeps its existing ownership/recovery gate so it cannot race
the channel's early attempt. Platform acceptance is not a read receipt.

The terminal and Web chat project `execution.ready` output when text was not
already streamed. They keep the invocation active until closeout ends. Attached
sessions use the same chat projection and retain their lease/control rules.

## Validation

Headless tests exercise answer delivery while memory processing is blocked,
post-delivery closeout failure, native failure/cancellation, delivery deduplication,
owner-answering checks, notification readiness and streamed/nonstreamed output.
The repository `make check` remains required before commit. These checks do not
establish production rollout or live platform acceptance.

## Oversized experience evidence

Source extraction (131072 encoded JSON bytes) and decision input (49152 bytes)
retain their existing caps. The delivered policy rejects the entire oversized
payload before the affected model call; it does not select excerpts, truncate,
raise caps, publish partial candidates, or label the result as no useful experience.
Extraction and judgment audits record a structured limit with stage, actual bytes,
limit bytes and `unprocessed=entire_payload`. A Host `experience.blocked` event
links the failure to the retained run and tool evidence. Decision rejection refers
to that decision payload; earlier extraction or comparison batches may have run.

The run retains completed execution, its answer, independent memory status and
failed experience status/error. Channel delivery keeps its separately recorded
outcome. Original run/tool evidence is not rewritten or deleted. Explicit
experience retry reads that evidence and repeats only post-processing; unchanged
oversized input will fail the same check again, without another business run or
answer delivery. Operators must review the retained source before choosing any
future bounded selection policy. Chunking, summarization and automatic retries
are not implemented. Historical records are not rewritten; production replay
and deployment require separate action.

## Interrupted channel intake

`RecoverChannels` enrolls a recovery notice only while changing a claimed
message to unknown with an untouched pending reply. Service startup drains these
notices before normal feedback starts, filtering by original channel, bot account
and profile. The existing persisted API target or webhook capability supplies the
original destination; configuration changes cannot select a replacement. An
expired webhook produces a recorded failure without a send. Historical unknown
records are never enrolled by this change.

`reply_kind=recovery_notice` distinguishes this communication from an Agent
answer. The exact transport body is committed in `reply_text` with `sending`
before transport; `reply_status` and `reply_error` independently record acceptance,
failure or uncertainty. Interrupted sends become unknown on restart. Neither
failed nor ambiguous notices are automatically retried, and generic task feedback
cannot retry these receipts. Execution status, run evidence and memory outcomes
are unchanged. Operators inspect channel receipts and `channel.recovery_notice`
events; this is not a new execution or evidence-resolution API.

Isolated tests cover restart before run creation, after a recorded partial tool
event, repeated recovery, delivery failure/uncertainty and a crash during delivery.
No production restart or historical notification is part of this implementation.

## AI task classification and acceptance

The executing Agent receives trusted per-run instructions to append an
`<agh-closeout>` JSON block containing `run_id`, `task_kind`, `outcome`, `reason`
and `evidence_quote`. This is a Host contract shared by native adapters, not a
second model/tool loop. The Agent judges the requested work and cites a concrete
check/result or delivered answer in its reason. Prefer `ops`, `query`, `report`,
`development`, `other`; new kinds must match `[a-z0-9-]{1,32}`. No text-keyword
classifier, manual accept command, confirmation UI or reaction is involved.

Host validates the current run ID, slug, outcome and a nonempty verbatim citation
present in both the delivered answer and reason. It removes the terminal block
from the persisted answer before delivery, then atomically persists classification
and judgment with execution completion. Raw native events (including streamed
metadata) remain evidence; live streaming consumers may see the metadata before
terminal normalization. Citation validation establishes provenance, not independent
verification of a model's claims. Semantic acceptance remains an AI judgment.
Missing/malformed closeout or unsupported evidence is `unresolved`, with an
explicit diagnostic reason attributed to `host.closeout`; failed/canceled
execution cannot become `resolved`.
A run ending before valid classification remains `unlabeled`. Pre-execution
failures and historical records can remain `unverified`; no historical backfill.

Optional run payload fields are `task_kind` and `judgment` (outcome, reason, actor,
judged_at); `acceptance_status` mirrors the outcome. There is no schema migration.
Reason text inherits run-record retention and detail access rules. Run details
show actor, time and reason. Stats selects only the kind and outcome, never the
reason, and emits `by_task_kind`, `success_rate` and `coverage.acceptance`.
Success rate is `resolved / (resolved + partial + unresolved)`, or null without
judged runs; coverage is `[judged, total]`. Historical accepted/rejected values
remain visible but do not become AI judgments. CLI `stats --task-kind ops` and
the Stats page filter use the same read-only aggregation, including comparisons.
