# Channel progress

Channel business runs with tools enabled receive the built-in
`channel_progress.send_progress(text, idempotency_key?)` MCP tool. It sends a
short update at meaningful milestones or blockers through the current authenticated receipt's transport. The agent
cannot choose a destination. Tool-free runs, restricted reads, extraction and
ordinary CLI runs do not receive this capability.

Text is trimmed and must contain 1–200 Unicode code points. Each run permits at
most five durable reservations, at least 15 seconds apart; the first is
immediate. Failed and uncertain attempts also count. Reusing a nonempty
idempotency key returns the original attempt without another send or charge.

Results distinguish `pending`, `accepted`, `failed` and `unknown`. The MCP waits
up to 25 seconds, then reports `pending` honestly if no terminal result exists. Acceptance
means transport acceptance, independent of task completion, business acceptance,
final reply delivery or memory persistence. Unknown sends are never replayed.
A pending result cannot guarantee ordering relative to the final answer. Do not
automatically replay pending or unknown attempts. The normal final answer
remains required. Routine per-tool chatter, raw tool logs, credentials and
internal reasoning do not belong in progress updates.

Progress authorization ends when its run or receipt becomes stale, completes or
is canceled. An already authorized in-flight send may finish. This is source
behavior validated by isolated tests; production adoption and live provider
acceptance remain separate.

See [implementation details](channel-progress-detail.md).
