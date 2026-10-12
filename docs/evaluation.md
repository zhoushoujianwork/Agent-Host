# Memory evaluation and regression

Status: implemented and evaluated on 2026-10-10. The expanded fixed suite passes
26/26 recall cases with [object-aware retrieval](object-retrieval.md), compared
with 13/26 using the earlier path. The historical live Jev run remains 15/16;
its admission mismatch is unresolved, and this retrieval change did not rerun
the live suite. See [validation details](evaluation-detail.md#validation-status-and-limits).

## Goal

Turn examples and everyday failures into reusable regression cases. Each case
records an input, relevant memories or evidence, an expected outcome and its
rationale. Expectations describe the intended behavior; do not copy the current
model's answer merely to make a test pass.

## Categories

The current [fixture catalog](../evals/README.md) has 42 cases in three groups:

| Group | Cases | Examples |
| --- | ---: | --- |
| Recall | 26 | Project isolation, lifecycle, explicit objects, aliases, property roles and bounded credential wording |
| User statements | 10 | Durable facts versus questions or progress; source attribution, duplicates and conflicts |
| Experience | 6 | Evidence-supported steps, failed operations, overclaims and one historical incident |

Each case has a primary category, a stable ID, and an origin identifying a
synthetic example or a captured incident. `memory-v2` preserves all 26 original
cases and labels, adding 16 recall counterexamples.

## Two kinds of checks

| Mode | What it checks | What a pass means |
| --- | --- | --- |
| `regression` | Scoped SQLite retrieval and context assembly from saved fixtures | The implementation still satisfies the selected expectations without a model call |
| `live` | Current Jev decisions for user statements and proposed experience | The selected provider's observed answers agree with this suite's labels in this run |

Keep the results separate. Deterministic passes do not measure model accuracy;
small live fixtures do not establish accuracy on daily work.

## Use

```sh
agent-host eval --suite evals/memory-v2.json
agent-host eval --suite evals/memory-v2.json --mode live
agent-host eval --suite evals/memory-v2.json --format json --output evaluation.json
```

Filter with `--category CATEGORY` or `--case CASE_ID`. Reports retain expected
and observed results, errors, timing and model information. Text and NDJSON
show progress; `--output` saves one JSON report to a new file. Future Web views
can use the same structure.

Regression uses temporary state. Live mode requires Jev credentials and uses
the official TypeSafe API by default, without Claude business execution. Excluded
cases are `not_run`, never passes. Provider failures remain explicit failures.

The original regression's wrong-service match and missed credential wording
are now passing cases with unchanged labels; the failing baseline is retained.
The latest retained live run withheld a narrowly supported negative lesson
below the unchanged probability threshold. That historical mismatch remains
visible and requires separate policy evaluation.

## Add a case after an incident

1. Minimize and de-identify the input and evidence; keep a source reference.
2. Specify the correct outcome, including what must be recalled or excluded.
3. Classify the case, assign a stable ID, and save the failing baseline.
4. Fix the behavior; rerun the category and full regression with the same label.
5. Retain the case and reports. Review and explain any later label changes.

Add a nearby counterexample when it clarifies the rule. Raw daily conversations
and tool logs are not automatically uploaded into the evaluation set.

## Current scope

This suite covers retrieval, context inclusion and bounded Jev decisions.
Existing Go tests separately cover native execution, retries and storage
lifecycle guarantees. Comparing complete tasks with and without memory,
large-database performance, general entity understanding and vector retrieval
remain later work. Passing bounded rules does not imply arbitrary Chinese
paraphrases or unresolved references are understood.
