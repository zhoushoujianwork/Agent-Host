# Answer delivery and closeout

Agent-Host makes a completed Agent answer available as soon as native execution
and its output are saved. Built-in memory decisions, experience extraction,
judging and publication follow the answer rather than delaying it.

Channels deliver the answer through their existing adapters. Owner-answering
keeps its coordinator's identity, freshness and outcome checks. CLI and Web
can show the saved answer even when the native engine did not stream text.
Verified result notifications can also proceed before closeout finishes.

Execution, delivery, business acceptance and closeout remain separate outcomes.
A later extraction failure stays in the run record; it does not replace an
already delivered answer or cause another send. Failed or canceled native
execution still uses the normal failure response.

Closeout remains owned by the same Host invocation. Its timeout, cancellation
and session leases remain active until the invocation finishes. If the process
exits during closeout, pending work may require the existing manual memory or
experience retry; retrying closeout does not rerun the business task.

Oversized experience payloads are explicitly blocked before model processing.
The audit identifies the limit and the entire unprocessed payload; original
evidence remains retained. Execution and delivery results remain independent.

AI acceptance and task classification now accompany execution completion; see
[the closeout contract](reply-closeout-detail.md#ai-task-classification-and-acceptance).
Humans only hand over tasks; no manual labeling or confirmation is required.
This change does not add detached background jobs or
automatic task replay. Source validation and production rollout are separate.
See the [implementation details](reply-closeout-detail.md) and the
[architecture](architecture.md).

Newly recovered claimed channel messages receive a labeled interruption notice
through the original destination when no reply has been attempted. It says the
outcome is unknown and work was not replayed; it asks the recipient to verify
possible effects before submitting new work. Delivery acceptance never resolves
execution uncertainty. Existing historical unknown messages are not backfilled.
Operators can inspect `reply_kind=recovery_notice` and the independent
`reply_status`, `reply_text`, and `reply_error` on the channel receipt.
