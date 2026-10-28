# Run statistics implementation contract

See [the user guide](run-statistics.md). This increment reads existing evidence;
new acceptance/category recording and the Web page are separate work.

## Reading and privacy

`internal/stats` opens SQLite with `mode=ro`, `query_only=1` and a 200 ms busy
timeout. It never opens a Host, acquires a Host lock, migrates, repairs, checkpoints
or starts a service. Only the current state schema is supported. Each store uses
one read transaction for both windows. A failed store contributes no partial
metrics; its source run count is null. Errors are fixed diagnostics, never raw SQL
or filesystem errors.
Repeated canonical homes are deduplicated. Explicit homes use logical channel
names for the established layout, otherwise `store-N`; no absolute paths appear.

Run selection uses creation time and half-open windows `[since, until)`. A widened
SQLite time prefilter is followed by exact Go timestamp comparisons to retain
nanosecond boundaries. Events are queried by the selected run's indexed ID and
only tool activity, native result usage, and ACP results are projected. Run
payload input/output fields are not transferred. Required native result evidence
is consumed by the usage summarizer and never emitted. Comparisons use the same
observation time for stale detection. Daily dates are UTC and only populated days
are returned. Each source's run count is after window and agent/channel filters.

## Schema version 1

Reports contain `schema_version`, `generated_at`, `window`, `sources`, `totals`,
`outcomes`, `acceptance`, `memory`, `daily`, `by_agent`, `by_channel`, `by_model`,
`duration_seconds`, `error_classes`, `tools`, `usage`, `attention`, `coverage`,
and `previous` (null unless comparison was requested).

Groups are arrays of `{name,runs}`, sorted by descending runs then name. Missing
agent/model is `unrecorded`. Channel falls back to the logical source. Unknown
execution maps to `unrecorded`; running older than the stale threshold maps to
`stale_running` exclusively. Missing acceptance is unverified; unknown memory
status goes into `other`. These are counts of evidence categories, not inferred
business results. Attention contains failed, stale or unrecorded executions and
memory failures, with run IDs and fixed reasons only.

Durations require a nonnegative completed-at minus created-at. Median averages
the two middle values for even samples; p90 uses nearest rank. Empty duration
measurements are null. `coverage.duration` is valid durations / selected runs.

Tool names come only from `data.raw._meta.claudeCode.toolName`. Each name counts
once per run. A run with any unnamed activity also counts in `unnamed_runs`.
`coverage.tools_named` counts fully named active runs / tool-active runs.

Usage reuses `usage.Summarize`, including its deduplication and extractor/native
result rules. `runs_with_usage` counts runs with at least one observed input-token,
output-token or cost measurement. `coverage.usage` is those runs / all runs. ACP-only calls
with no measurements are uncovered. Additional coverage pairs `input_tokens`,
`output_tokens` and
`cost_usd` count runs whose recognized calls all supply that measurement / all
runs. Values are observed subtotals, null if nothing was observed, never estimates
for missing calls. `runs_missing_cost` includes absent results and incomplete
result costs. ACP-only results therefore retain null tokens/cost and zero covered
measurement runs. An observed numeric zero remains zero.

Error classes contain counts and bounded example run IDs, never error messages.
The specific `caller_lost` phrase takes precedence over an appended `context
canceled` cause. Remaining first-match order is canceled context, canceled
deadline, malformed event, process wait delay, process exit, event persistence,
then other. This resolves the overlapping strings in issue #21 explicitly.

`--top` bounds attention and each class's example IDs; it does not truncate metric
groups. Text output preserves all measurements and coverage. JSON is the stable
machine contract. No task category or acceptance rate is inferred.

## Verification

Fixture tests include golden JSON, privacy, exact boundaries, comparison, source
failure isolation, cwd independence and unchanged database/WAL bytes with a live
fixture connection. `STATS_SCALE=1 go test ./internal/stats -run TestScale -v`
creates approximately 250 MB of synthetic evidence and enforces a 30-second query
context. It never uses production data. These checks do not claim production
acceptance or deploy the command into an existing runtime.
