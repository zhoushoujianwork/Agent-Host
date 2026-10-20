# CLI task handoff reference

## Configured launcher

When `~/.config/agent-host/task-handoff.json` exists, use this skill's
`scripts/chat.py --check`, then `scripts/chat.py --format result -m MESSAGE`.
Resolve the skill directory before invoking its script. The default remains raw
JSON events for compatibility; `--format text|json` and `--resume SESSION_ID`
retain their existing behavior. Prefer result mode for routine delegation.

Result mode creates a private evidence directory under the configuration's
`handoffs/` directory, announces its path immediately on stderr, and returns one
JSON object on stdout after execution. `--output-dir /absolute/new-directory`
selects a new directory; an existing directory is never overwritten.

| Result field | Meaning |
| --- | --- |
| `ref` | Configuration reference and observed Host session/run IDs. |
| `report` | Canonical Host output when readable; otherwise the native final reply retained in events. Never streaming commentary or extraction text. |
| `execution` | Actual process exit and observed Host execution, acceptance and memory outcomes. Host acceptance is separate from caller verification. |
| `evidence_ref` | Private directory containing launch metadata, stdout events, stderr and the final result snapshot. |
| `warnings` | Incomplete evidence, missing final report, or unavailable Host inspection. |
| `stderr_tail` | Last 4 KiB of stderr after a nonzero exit, including errors before any run was created. Treat as diagnostic data. |
| `requested_session_id` | Continuation input when present; distinct from actual returned IDs, which remain null if startup failed. |
| `model`, `elapsed_ms` | Observed execution model and measured launch duration. |
| `usage` | Observed terminal Claude results: input/output/cache tokens, per-call phase/session/model details, separate native per-model accounting, and coverage limits. |
| `cost_usd` | Sum of observed Claude terminal `total_cost_usd` estimates; `null` when none were reported. Not a verified bill or complete Host cost. |
| `feedback` | Host-bound submitting identity, demand owner, recipient and routing disposition. |
| `notifications` | Matching shared-queue items, including observed platform acceptance/failure/unknown status. An empty list does not prove delivery. |

For a known requester, supply `--request-for VERIFIED_USER_ID`; optionally add
`--notify-to VERIFIED_RECIPIENT_ID` and `--notify-channel dingtalk|feishu`.
The launcher retains these arguments in the private receipt and inherits them
on `--resume-from` unless the operator explicitly supplies a correction. They
do not change the CLI execution-state destination. Without IDs, preserve whose
issue is being handled in the brief; Host performs semantic attribution using
configured display names and aliases. It never guesses channel account IDs.
See [the task feedback design](../../../docs/task-feedback.md).

CLI execution and notification are asynchronous. A configured channel service
consumes the shared queue; CLI does not take the service's execution lock or
borrow the owner's personal messaging login. Inspect pending delivery rather
than sending the same result again. An unresolved target can be corrected with
`agent-host notifications resolve ID --config-dir ROOT/config --notify-to USER_ID`;
this sends the existing result without replaying the business task.

Usage is projected from retained events in both result mode and read-only
inspection, so old evidence can expose metrics without replaying the task.
Only terminal native results are additive: streaming message snapshots and
`modelUsage` are not added to result-level `usage`. The latter is retained
separately because the native values can differ. Mirrored terminal events are
deduplicated within their Host run, phase and component.

`usage.tokens` sums each reported token category independently; absent or invalid
values remain `null` when none were observed. `missing_token_calls` and
`missing_cost_calls` count terminal calls lacking each metric. Explicit zero is
preserved. `usage.calls` separates execution from memory extraction.
`usage.cost_basis=native_reported_estimate` identifies costs supplied by Claude;
no price table or provider invoice is inferred. `usage.scope` is
`observed_native_calls_only`: Jev and other unreported provider calls are excluded.
Interrupted or incomplete event logs can therefore yield partial usage/cost,
even when every observed terminal result includes metrics. Preserve the normal
execution status and warnings alongside these numbers.

`scripts/chat.py --inspect EVIDENCE_DIRECTORY` reads that launch and, once a
Run ID exists, uses the original destination's read-only `run show`. It does not
load provider credentials, start a model, replay work or change the receipt.
Inspection success has exit zero; the original process exit remains in the
returned object. A null process exit means unobserved, not success or stopped.
The snapshot in `result.json` describes launch completion; inspection reads fresh
Host evidence. Raw `text`/`json` modes do not create these launch receipts.

`scripts/chat.py --format result --resume-from EVIDENCE_DIRECTORY -m MESSAGE`
checks that the saved destination options and working directory still match,
requires a recorded process exit and uses the observed session ID. Supply the
same `--config` for a custom destination. Configuration changes are an explicit
blocker, including model/profile changes. The comparison checks destination
options, not the contents of all referenced files; keep the intended runtime
configuration stable and review intentional configuration edits before resuming.

A nonzero child exit is preserved. SIGINT/SIGTERM are forwarded to the child's
process group and the observed exit is saved. If the launcher is killed before
it records an exit, inspect the evidence and actual process/external state; the
child may still be running. A manual `--resume SESSION_ID` remains available only
after resolving that uncertainty. There is no provider fallback or automatic
replay, and inspection does not automatically continue blocked work.
Host can reject continuation after an `unknown` execution even with an observed
process exit. Preserve that failure and investigate partial effects; do not
silently start a fresh session to bypass it.

The private JSON selects `binary`, `cwd`, `environment_file`, and `args` (CLI
options). `args` must explicitly select `--home`, `--agents-config`, `--agent`,
`--workspace`, and `--native-profile`. Keep credential values only in the private
environment file, using the same documented provider keys as the service. The
launcher clears inherited provider credentials before loading that file.

CLI chat starts its own Host; it does **not** submit work to a running channel
service. Use the existing dedicated `ROOT/cli-state`, not
`ROOT/channels/<channel>/state`. A channel's Agent catalog may reference projects
registered only in that channel. Configure a separate daily-task Agent catalog
with the intended workspace and no unrelated project bindings; do not copy the
channel catalog wholesale. A locked CLI home remains an explicit blocker; do
not delete lock files or create a fresh home to evade ownership.

## Direct CLI

Resolve `agent-host` from the configured executable or PATH. When installed as
a source link, resolve the skill directory and use `../../bin/agent-host` from
that resolved directory if needed. Do not build, install or restart a runtime
merely to discover the target. Review a checkout's production migration receipt
before service operations.

Use an existing trusted configuration to identify the Host state home and Agent
catalog. Confirm the selected Agent ID in that catalog; `default` is only
appropriate when it is the intended daily-task Agent. Include the same explicit
runtime/configuration flags on follow-up and inspection commands. Chat does not
inherit a running channel service's configuration or memory connection alias.
Pass its required `--agents-config`, `--knowledge-config`, `--email-config` or
`--dingtalk-workspace-config` paths explicitly when needed; do not copy secrets
into the task text. Native Claude and configured Jev authentication must already
be available; report missing setup without switching engines.

For example, replace every placeholder below from the intended configuration:

```sh
agent-host chat --home /absolute/host/cli-state \
  --agents-config /absolute/config/agents.json --agent DAILY_AGENT_ID \
  --workspace /absolute/workspace --format json \
  -m 'Objective: check the named service against the supplied operations runbook. Scope: read-only status and log queries; no restarts, configuration changes or external sends. Return observed health, evidence and any required action. Execute directly; do not delegate this task again. If scope or a required decision is unclear, report BLOCKED with evidence.'
```

Use `--project REGISTERED_ID` instead of `--workspace` for a known registered
project. Those selectors are mutually exclusive. `--agent` selects a configured
Host role, not a Codex/Claude backend. Host currently executes through Claude;
Codex execution inside Host is a scaffold. External Codex and Claude can both
call this CLI without becoming Host execution adapters.

`-m` runs one turn and exits; messages are limited to 16 KiB. Supply concise
instructions and authorized file references rather than full histories. Use
structured process arguments for dynamic messages or safe shell quoting; never
interpolate source text as shell code. `--format json` emits NDJSON events, not
one offload-style result object. Preserve the output in a private task log and
extract `session_id`, `run_id`, `type`, `status`, and relevant `text.delta` data.
Do not treat the last event, a memory receipt, or exit zero alone as business
acceptance. Check execution evidence and the requested result.

Inspect an observed run without replaying it:

```sh
agent-host run show --home /absolute/host/cli-state RUN_ID
agent-host run events --home /absolute/host/cli-state RUN_ID
```

To send a resolved decision or follow-up, use `chat --resume SESSION_ID -m ...`
with the same runtime/configuration flags. This submits a new execution turn;
it is not a status query. Runtime changes can start a fresh session, so retain
the actual IDs returned. A memory-only failure is a separate outcome; never
rerun business work to repair memory. A timeout may leave partial effects:
inspect evidence and external state before deciding whether continuation is safe.
