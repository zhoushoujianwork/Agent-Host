# Memory evaluation details

Status: implemented and evaluated on 2026-10-10. Object-aware retrieval passes
the expanded fixed recall suite; the original failed baselines remain historical
evidence. The live Jev admission mismatch has not been resolved or rerun. The
[main guide](evaluation.md) governs the user workflow and current scope.

## Ownership and isolation

The evaluation command is a consumer of the existing memory components. It does
not create a second retrieval policy or replace native harness execution.
Regression cases exercise a temporary SQLite store, production
`Store.Retrieve`, and existing context assembly using accepted matches plus
prefiltered persistent memories. Live cases use the current user-statement
decider or experience
assessment against the explicitly selected Jev provider.

Evaluation state is separate from the user's normal host home and memory. The
runner never calls native Claude to carry out a business task. A live quality
run sends the selected fixture text to the chosen provider; published fixtures
must therefore contain minimized synthetic or explicitly de-identified data.

## Command

```text
agent-host eval --suite PATH
  [--mode regression|live]
  [--category CATEGORY]
  [--case CASE_ID]
  [--format text|json]
  [--output PATH]
  [--jev-provider typesafe|custom]
  [--jev-endpoint URL]
  [--jev-model MODEL]
  [--timeout DURATION]
```

The default mode is `regression`, with `evals/memory-v2.json` as the default suite.
Options appear after `eval`; the command has no positional case argument. A missing suite, invalid fixture or expectation,
unknown selection, or a filter matching no cases is an error. An applicable
case that fails its assertions or provider call produces a failing result and
a nonzero command exit status. Reports must remain readable on failure so a
later fix can be compared with the baseline.

`--timeout` applies to each selected case, including fixture setup and any
provider decisions in that case. A labeled mismatch continues to the next
independent case. A provider or execution error stops the run, leaving the
remaining cases explicitly `not_run`. Cancellation also preserves which
selected cases did not execute.

Live mode uses the existing transport and policy: the official TypeSafe API by
default with `TYPESAFE_API_KEY`, or an explicit `custom` gateway endpoint with
`JEV_API_KEY`.
Authentication does not cross providers. There is no automatic model/provider
fallback and no replacement of a failed live call with a fixture answer.

Terminal text shows each case starting and finishing, with category, model and
duration where applicable. `--format json` emits the existing versioned NDJSON
event envelope with `eval.case.started`, `eval.case.finished` and
`eval.finished`. Case events include the case result in `data.case`; the final
event contains the full report in `data.report`. JSON mode is an event stream,
not one standalone report object.

`--output PATH` independently saves a single indented JSON report. The parent
directory must already exist and the file must be new; an existing baseline is
never overwritten. A valid run with failing expectations still saves its
report. The report includes `binary_sha256` for the executing binary in
addition to the suite fingerprint, allowing source inputs and implementation
artifacts to be distinguished.

## Fixture and report contract

The suite is versioned and cases have stable IDs, a category, an origin,
reviewed expectations, and a reason for those expectations. The current set is
[memory-v2.json](../evals/memory-v2.json), containing the unchanged original
[memory-v1.json](../evals/memory-v1.json) cases plus 16 recall counterexamples.

| Field | Contract |
| --- | --- |
| Suite | `version: 1`, slug `id`, nonempty `description`, and `cases` |
| Case identity | Stable slug `id`, `category`, `title`, `rationale`, `kind`, and scope label |
| Origin | `kind: synthetic` or `incident`; an incident requires `reference` |
| Recall input | Query in `input` and saved `memories` with ID, scope, kind, text and status |
| Recall expectation | Exact `recall_ids` set; order is not graded |
| Statement input | Whole user statement in `input`, plus memories to compare in the current scope |
| Statement expectation | Explicit `store`, `kind`, `conflict_ids` and `duplicate_ids` |
| Experience input | Complete `draft` with `evidence_ids`, plus paired tool `evidence` |
| Experience expectation | Explicit `eligible` and `kind: procedure` |

The loader rejects duplicate JSON object keys, unknown fields, trailing objects,
duplicate case or memory IDs, unsupported field combinations, invalid memory/evidence references
and inconsistent expectations. Incident source references are required labels;
the loader does not fetch or verify an external source. Bounds are 4 MiB per suite, 1–200 cases, at most
100 memories per case, 4 KiB per fixture memory and 16 KiB per case input.
Experience drafts also retain the existing extraction validation limits.

Fixtures contain scope labels rather than real user workspace paths. Recall
setup uses normal production state transitions to create attributed user
statements in a temporary database, including conflict or invalidation when
requested. The runner then calls the same `Store.Retrieve` as chat: source-linked
objects/topics, current aliases and FTS candidates pass applicability checks.
`assembly.Select` budgets the accepted matches and prefiltered persistent
memories with the existing 8-record / 12-KiB context limit. Gold expectations do
not influence fixture state or retrieval. The current fixture format does not seed
reviewed experience as already-accepted memory or simulate candidate approval.

Cases distinguish saved-memory retrieval from user-statement decisions and
experience screening. The selected mode controls which of these paths runs.
Unsupported case kinds in that mode are reported as `not_run`, never folded
into successful checks. The suite and filter must select actual applicable
work; an empty result is not a passing evaluation.

Structured results retain suite/source identity, mode and filters, expected and
actual outcomes, per-case errors and duration, and model metadata when a model
was called. Recall reports retain `actual.query` (source spans, rule version,
resolved/unknown/ambiguous identities) and `actual.retrieval` (candidate routes
and acceptance or rejection reasons). They show lexical `matched_ids`, final
`recall_ids`, and `missing_ids`/`unexpected_ids` against the expectation.
They therefore distinguish a search candidate from a memory actually selected
for context. Live results retain the underlying `decision` or `assessment`,
including policy audit and typed model responses.

The `suite_sha256` fingerprint ties a report to the exact JSON file bytes that
were evaluated. A changed fixture must produce a distinguishable report input;
reports are evidence, not mutable expected-answer files.

Each case has `selected` and a final status: `passed`, `failed`, `error` or
`not_run`. Reports include counts for the whole suite and each category; skipped
cases never increase the pass count. The top-level success result requires
every selected case to pass. Dotted categories in the initial catalog separate
recall, statement and experience concerns; the [catalog](../evals/README.md)
lists the categories, historical failing baselines and unresolved live mismatch.

For Jev, retain typed answers and their reported probabilities as observations.
A policy threshold and agreement with a labeled fixture are separate facts.
Neither a high probability nor an aggregate passing fraction is calibrated
confidence in correctness on unseen user work.

## Incident lifecycle

| Step | Required evidence |
| --- | --- |
| Capture | Minimal de-identified input, relevant stored context, and a source reference |
| Label | Correct expected outcome and human-readable rationale independent of current output |
| Classify | Primary failure category and a stable case ID |
| Reproduce | Saved report showing the current mismatch or explicit provider error |
| Repair | Narrow implementation or policy change with the same expectation |
| Recheck | Focused category results and full deterministic regression results; relevant live run when the decision policy changes |
| Retain | Accepted fixture, original failure evidence, and subsequent validation evidence |

A report should distinguish a retrieval miss from an incorrect memory being
injected, a rejected valid declaration from an admitted transient request, and
an unsupported procedural claim from an applicable evidence-backed proposal.
Do not hide these different errors behind one total pass rate.

If an incident needs unsupported features, preserve it as a documented pending
case with a named coverage gap rather than claiming it was tested. Native
process failures, cancellation, memory-only retry, and storage transaction
invariants continue to use their existing Go tests until an evaluation case
can exercise the real owning component.

## Validation status and limits

The current object-retrieval comparison uses the same frozen 42-case
`memory-v2` suite for both binaries. The original 26 cases and their expectations
remain unchanged; 16 recall counterexamples cover alias, object and property
boundaries. No Jev threshold changed, and the 16 live cases were not rerun.

| Same-suite retrieval run | Selected | Passed | Failed | Errors | Not run |
| --- | ---: | ---: | ---: | ---: | ---: |
| Earlier path (private validation evidence) | 26 | 13 | 13 | 0 | 16 |
| Object-aware path (private validation evidence) | 26 | 26 | 0 | 0 | 16 |

Both reports retain identical suite fingerprints and their own binary hashes.
The [object retrieval details](object-retrieval-detail.md#validation) record
current validation and scope limits. This compares deterministic applicability
on a bounded synthetic set, not general entity accuracy or large-memory speed.

The earlier retained runs below used the frozen 26-case `memory-v1` suite on
2026-10-10. Both reports retain full suite and binary SHA-256 values. No label
or decision threshold was changed to make the results pass.

| Run | Selected | Passed | Failed | Errors | Excluded by mode | Duration |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite regression (private validation evidence) | 10 | 8 | 2 | 0 | 16 | 120 ms |
| Live Jev decisions (private validation evidence) | 16 | 15 | 1 | 0 | 10 | 19,379 ms |

Both CLI commands returned exit code 1 because not every selected expectation
passed. The durations are individual observations, not latency percentiles or
large-memory performance measurements. Live execution selected Patsnap with
`jev-latest`; responses identified `typesafe/jev-1.13-20260917`.

| Original mismatch | Expected | Historical observation |
| --- | --- | --- |
| `recall-wrong-entity-gap` | Only `order-db` | Both `order-db` and unrelated `billing-db` were matched and selected through shared lexical tokens |
| `recall-synonym-gap` | `credential-rotation` | No match or injected memory because the Chinese synonyms share no indexed bigram |
| `experience-observed-negative-lesson` | Eligible narrow lesson about an observed missing file | Jev selected `supported` at `0.77`, below the `0.8` threshold, so the policy abstained |

The two recall cases above now pass under object-aware retrieval with the same
labels. The negative-lesson case remains unresolved and received durability
`yes` at `1.0` in the historical live run. Its mismatch is a
withheld eligible proposal relative to the fixture label, not a model choice
of `unsupported`. Preserve the raw choice and probability when diagnosing the
gate; lowering the threshold solely to pass this case would not establish a
better policy.

The historical incident was also reassessed without changing its draft or
cited evidence. It remained ineligible as expected: the new `supported`
probability was `0.75`, below `0.8`. The earlier report recorded `0.74`; neither
run selected `unsupported`. This is another bounded observation, not a promise
that repeated live model decisions will be identical.

For that initial evaluation implementation, `make check`, `make build`, and
`go test -race ./internal/cli ./internal/evaluation` passed. Final report-output
hardening was verified after the retained runs:
the rebuilt binary still reported 8 passes and the same 2 recall mismatches,
and its final NDJSON event matched the saved report. The memory and Jev
policies were unchanged; live observations above retain the original tested
binary fingerprint. Evaluation tests separately check invalid
gold/schema, mode/filter selection, failure reports and component behavior.
Existing native execution and experience reports remain in the
[experience details](experience-memory-detail.md).

This increment does not run paired native tasks with and without memory and
therefore cannot claim improved task completion, reduced tool use or token
savings. It does not measure large-database latency or semantic retrieval
quality beyond the selected fixtures.
