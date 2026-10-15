# Live execution experience validation

The lightweight [procedural learning MVP](../../docs/procedural-learning.md)
adds an opt-in Go native probe and a read-only operational summary. Its exact
commands, failed probes and limits are in the
[validation record](../../docs/procedural-learning-detail.md#mvp-implementation-and-observation).

```sh
python3 -B experiments/experience/observe.py --home .agent-host/channels/dingtalk/state --limit 30
```

The older Python runner described below is historical documentation; `run.py`
is not present in the current source checkout.

This runner exercises the real built Host CLI, native Claude tool execution,
and the explicitly selected company Jev endpoint inside a temporary Go project.
It does not change a real repository or contact another user. A successful run
proves one observed integration path, not general extraction accuracy or a
causal productivity improvement from memory.

```sh
make build
python3 experiments/experience/run.py --credentials env --output /tmp/experience-live.json
```

Jev uses `PATSNAP_API_KEY`. Claude provider credentials come from the existing
settings environment and are passed only in child-process memory. `--model`
explicitly chooses a model. For the separately authorized local company setup,
`--credentials llm-alias --claude-config llm-alias` parses the literal alias
credential and native provider environment. The alias is never executed and its
permission-bypass arguments are never used. The parser checks the company host.
There is no credential/provider substitution or automatic retry.

## What the runner checks

1. Independent `go test` confirms the synthetic fixture fails. Claude captures
   that failure, changes the implementation, and reruns tests. The runner then
   independently runs the unchanged tests and checks their exact contents.
2. A second turn uses the same native configuration directory and host session
   with `--resume`. It must recall a one-time marker from native conversation
   history, without finding that marker in injected host memories.
3. The runner lists and inspects pending candidates, selecting one that cites
   both a successful edit and passing test. It invokes the explicit CLI
   `memory accept` command with a review note and rejects other candidates.
   This is a scripted reviewer for synthetic data, not evidence of a human
   reviewing arbitrary production content or of automatic semantic accuracy.
4. A new host session and empty native configuration receive the accepted
   procedure. Claude must cite it while fixing a second similar fixture.
   Independent tests validate the resulting files. This does not establish
   that Claude would have failed without the memory.
5. Explicit invalidation persists. Another fresh native configuration must not
   receive the invalidated memory or any unaccepted second-run candidates.

Native permissions are explicit: `Read`, `Edit`, `Write`, and
`Bash(go test *)`. No permission bypass is enabled. Empty native configuration
directories disable custom hooks and auto memory; the continuation reuses its
original directory, while recall checks use new ones. The host database is
shared only within the temporary fixture. Extraction has its own separate
tool-free, nonpersistent native invocation.

The report retains fixtures, native version, binary hash, request arguments,
events, tool evidence, timings, review receipts, independent test output, and
failures. Credentials are redacted before writing or printing. Temporary native
state, generated files, and SQLite databases are deleted afterward. The report
is the retained evidence, so use a new output path for each explicitly chosen
rerun; the script refuses to overwrite an earlier result.

## Status and limits

The first attempt (private validation evidence) retained real failed-test,
edit and passing-test evidence and successfully resumed a native conversation.
Its generated experience added technical claims beyond the observed tests;
Jev selected support with probability `0.74`, below the unchanged `0.8` gate,
so no candidate was admitted and the overall experiment failed. Extraction
policy v2 narrows proposals to observed steps and test scope. This is a post-hoc
policy adjustment, not a held-out accuracy result. The second attempt (private validation evidence) passed the complete loop:
real edit/test, resume, explicit candidate acceptance, fresh-session cited reuse,
independent tests, and invalidation exclusion. Jev support was `0.86` for the
accepted candidate. See [validation details](../../docs/experience-memory-detail.md#validation-status-and-limits)
for timings and what remains covered only by controlled tests. Live model behavior may produce no eligible
candidate or fail to cite the procedure; these are failed assertions, not a
reason to silently approve another result. The runner performs no automatic
retry, changes no provider, and does not replay failed business operations.

Protocol cancellation and independent experience retry are covered by focused
Go tests; this script does not deliberately interrupt a live business process.
It also does not measure recall quality at scale, adversarial robustness, or
the quality of human acceptance decisions.
