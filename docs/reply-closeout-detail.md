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
