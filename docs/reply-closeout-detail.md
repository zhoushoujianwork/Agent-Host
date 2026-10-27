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
