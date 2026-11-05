# Channel progress

Channel business runs with tools enabled receive the built-in
`channel_progress.send_progress(stage, idempotency_key?)` MCP tool for meaningful
milestones or blockers. Progress uses attached reactions on the original question.
It never sends an ordinary text or webhook message, including on failure. Each
question retains its ordinary final answer through the existing final reply path.
The agent cannot choose a destination. Tool-free runs, restricted reads,
extraction and ordinary CLI runs do not receive this capability.

Select a stage such as `checking_permissions`, `connecting`,
`checking_resources` or `blocked`. Host renders the short label (for example,
“检查权限” or “检查资源”); the model does not compose a progress sentence. For a
multistep task, report the first concrete stage before starting tool work.
Legacy short `text` labels remain compatible, with a 12-code-point Host limit.
Rejected arguments reserve no attempt: correct the stage and retry. Pending or
unknown delivery is different and must not be replayed.
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
