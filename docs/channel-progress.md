# Channel progress

Channel business runs with tools enabled receive the built-in
`channel_progress.send_progress(text, idempotency_key?)` MCP tool for meaningful
milestones or blockers. Progress uses attached reactions on the original question.
It never sends an ordinary text or webhook message, including on failure. Each
question retains its ordinary final answer through the existing final reply path.
The agent cannot choose a destination. Tool-free runs, restricted reads,
extraction and ordinary CLI runs do not receive this capability.

Labels are trimmed, valid UTF-8, and contain 1–12 Unicode code points without
controls or multiline content. Twelve is Host policy, not a provider maximum.
DingTalk uses a custom text reaction; Feishu uses its native Typing emoji for
progress and native status emoji for lifecycle markers. Unsupported feedback
fails explicitly. DingTalk custom bot text acceptance still needs live verification.

Each run permits five durable reservations, at least 15 seconds apart; the first
is immediate. Failed and uncertain attempts count. Reusing a nonempty idempotency
key returns the original attempt without another send or charge.

Results distinguish `pending`, `accepted`, `failed` and `unknown`. The tool waits
up to 25 seconds; acceptance means confirmed attachment, independently of task
completion, business acceptance, final reply delivery and memory persistence.
Pending cannot guarantee ordering relative to the final answer. Never
automatically replay pending or unknown attempts, including with a new key.
Routine per-tool chatter, raw logs, credentials and internal reasoning do not
belong in progress labels.

Lifecycle and requested progress share one feedback queue per receipt. Terminal
feedback supersedes queued updates. Stale or canceled runs cannot authorize new
mutations; already authorized operations may finish. Uncertain additions or
removals block later replacement rather than risk duplicate attachments.

See [implementation details](channel-progress-detail.md).
