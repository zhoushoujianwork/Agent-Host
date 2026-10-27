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

This change does not add automatic acceptance, detached background jobs or
automatic task replay. Source validation and production rollout are separate.
See the [implementation details](reply-closeout-detail.md) and the
[architecture](architecture.md).
