---
name: agent-host-platform
description: Read Agent-Host (agh) run status, health, statistics, backlog, stuck or failed runs, and recent work reviews (运行状态、健康检查、统计、积压、卡住或失败的执行、近期工作复盘). Not for executing operations, restarting services, filing issues, or saving knowledge.
---

# Agent-Host platform state

Use the installed `agent-host --help`, then `agent-host status --help` or
`agent-host stats --help` before the corresponding read. Older installed binaries
may lack these commands even when the checkout includes them. If unsupported,
report the missing capability; do not substitute database queries or deploy a
new binary as part of this read.

Select an explicit absolute runtime root from the authorized request or runtime
configuration. Check the checkout's `.agent-host/production-migration.json`:
when present, it identifies a separately deployed production runtime. Keep that
runtime separate from development configuration and channel identities. Never
fall back to cwd or assume the checkout's `.agent-host` is production. If the
intended root is unresolved, report that missing prerequisite before querying.
Use explicit `--home` paths only for intentionally scoped state homes supported
by the installed help; do not combine them with `--root`.

For current health and attention items:

```sh
agent-host status --root /absolute/runtime --format json
```

For statistics and the preceding equal comparison window:

```sh
agent-host stats --root /absolute/runtime --since 7d --format json --compare
```

Replace the example root with the resolved destination. Select supported filters
and windows to match the question. Use `status --no-http` when local HTTP probing
is outside the authorized read, and retain its unverified checks in the report.
The Go commands own collection and metrics; do not add scripts to recompute them.

Cite the command, runtime scope, generation time, time window and filters with
reported figures. Include source diagnostics, omitted/unreadable homes and each
metric's coverage (covered/total); comparison periods need their own coverage.
Keep observed token/cost subtotals distinct from a provider bill. Unknown/null
is not zero, partial coverage is not a platform-wide total, and a stale-running
threshold is an attention signal rather than proof of a stuck process.
Execution completion, business acceptance and memory persistence are separate
outcomes. `unverified` acceptance is not a success rate; never infer acceptance
from completed runs or memory publication.

Stay read-only: never open production stores directly (including SQLite), write
state, or advise restart, replay or deletion. Report failures and uncertainty
without repair attempts. Process ownership and service lifecycle belong to the
[operations guide](../../docs/service-install.md). Filing follow-up work belongs
to [agent-host-work-items](../agent-host-work-items/SKILL.md); executing an
authorized runbook belongs to [agent-host-tasks](../agent-host-tasks/SKILL.md).
