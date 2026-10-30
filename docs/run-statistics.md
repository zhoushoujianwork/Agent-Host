# Run statistics

The source implementation provides a read-only `agent-host stats` command for
retained execution evidence. It does not record new facts or change a runtime.
只读 [Stats 页面](../tools/run-viewer/README.md#stats) 已实现；任务分类与验收记录仍待实现。
[Technical contract](run-statistics-detail.md) defines coverage and window semantics.

```sh
agent-host stats --root /absolute/runtime --since 7d --compare --format json
agent-host stats --home /absolute/runtime/cli-state --home /absolute/runtime/channels/feishu/state
```

An explicit root or one or more state homes is required. A root discovers
`cli-state/state.db` and `channels/*/state/state.db`. There is no working-directory
runtime fallback. Sources with missing, locked or unsupported databases are
reported and excluded; the command fails if none can be read.

Execution completion, business acceptance and memory persistence are independent.
Unverified acceptance is not a success rate. Missing measurements are `null`;
coverage accompanies optional measurements, including token and cost subtotals.
Native costs are estimates, not provider bills. Run IDs are the only drill-down
handles; reports omit prompts, replies, sender identities and payload paths.

Defaults: last seven days, until now, stale running after two hours, ten attention
entries and example run IDs, text output. `--agent` and `--channel` filter runs;
`--since` accepts a duration, RFC3339 timestamp or UTC date, and `--until` accepts
a timestamp or UTC date. `--compare` includes the preceding equal-length window.
