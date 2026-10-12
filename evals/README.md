# Memory fixture suites

See the [evaluation guide](../docs/evaluation.md) for the workflow and
[implementation details](../docs/evaluation-detail.md) for the command and
report contract. Run commands from the repository root.

```sh
agent-host eval --suite evals/memory-v2.json
agent-host eval --suite evals/memory-v2.json --category recall.scope
agent-host eval --suite evals/memory-v2.json --mode live --category statement.relations
```

## Current set and preserved baselines

[`memory-v2.json`](memory-v2.json) is the default suite: 42 cases, comprising
26 recall checks, 10 user-statement decisions and 6 experience assessments.
It preserves all 26 cases in [`memory-v1.json`](memory-v1.json), including their
labels, and adds 16 recall counterexamples. Most inputs are Chinese. There are
41 synthetic examples and one retained historical incident; these are not a
representative sample of production traffic.

| Category | Cases | Intended check |
| --- | ---: | --- |
| `recall.basic` | 3 | Chinese/Latin lexical matches and an unrelated query |
| `recall.scope` | 2 | Project boundaries for facts and aliases |
| `recall.context` | 2 | Unannotated persistent preferences and constraints |
| `recall.lifecycle` | 3 | Conflicted/invalidated records and alias exclusion |
| `recall.entity` | 5 | Distinct, multiple, unknown, overlapping and generic service subjects |
| `recall.alias` | 5 | Explicit aliases, collisions, withdrawal, substrings and alias-only saved facts |
| `recall.semantic` | 3 | Bounded credential wording with digital subtype and physical-key separation |
| `recall.property` | 3 | Requested property, object overview and rotation versus credential format |
| `statement.durable` | 4 | Project fact, preference, constraint and adopted decision |
| `statement.exclusion` | 3 | Question, work request and temporary progress |
| `statement.provenance` | 1 | Assistant-reported success is not a user declaration |
| `statement.relations` | 2 | Semantic duplicate and incompatible same-subject values |
| `experience.support` | 4 | Observed procedure, failure/success contradiction, production overclaim and negative lesson |
| `experience.durability` | 1 | Temporary status is not a reusable procedure |
| `experience.incident` | 1 | First live extraction's support-gate abstention |

On the same frozen `memory-v2` suite, the
earlier retrieval path (private validation evidence) passed 13/26
recall cases and the object-aware path (private validation evidence)
passed 26/26. All 16 live cases were `not_run` in these regression runs. The
[retrieval design](../docs/object-retrieval.md) defines the bounded rules and
remaining coverage limits.

The historical `memory-v1` regression (private validation evidence)
passed 8/10. Its `recall-wrong-entity-gap` and `recall-synonym-gap` failures are
now passing regressions with unchanged expected IDs. Their original rationale
and reports remain historical evidence, not descriptions of current failure.

The separate historical live report (private validation evidence) remains
15/16: the narrow negative lesson about a missing file was withheld because Jev
chose `supported` at `0.77`, below the unchanged `0.8` policy threshold. It did
not choose `unsupported`. This mismatch remains unresolved; the live suite was
not rerun for the retrieval change.

## Retained incident

`experience-historical-support-abstention` copies the draft and cited tool
evidence unchanged from
`live-2026-10-10-a.json` (private validation evidence),
using the `experience.assessed` event in `commands[0]`. Its rationale calls out
untested general character/case-folding claims. The original Jev answer chose
`supported` with probability `0.74`; the existing `0.8` policy threshold then
abstained. It was not a model choice of `unsupported`. The fixture expects the
candidate to remain ineligible without requiring a future run to reproduce
that exact probability.

The source was already a sanitized, isolated Go experiment. Its report paths
and source IDs are retained as evidence references, not executable paths or
instructions. This case assesses the saved draft; it does not rerun extraction
or the original repair.

## Add a fixture

Give each case a stable ID, title, category, origin, scope and rationale.
`origin.kind` is `synthetic` or `incident`; an incident also needs a meaningful
`origin.reference`. Use a small scope label such as `project-a`, not an actual
user directory. Set only fields applicable to its kind:

- `recall`: query in `input`, saved `memories`, and exact expected `recall_ids`.
- `statement`: exact user text in `input`, compared `memories`, and expected
  `store`, `kind`, `conflict_ids` and `duplicate_ids`.
- `experience`: `draft`, paired `evidence`, and expected `eligible` plus
  `kind: "procedure"`.

Keep positive examples and nearby counterexamples together by category. Each
gold label needs an explanation independent of current output. The suite
stores expected decisions, never canned model answers. Save evaluation reports
separately when reproducing a daily failure.

The recall fixtures currently seed attributed user statements only. Candidate
review, accepted experience persistence, expiry, transaction rollback, retries
and native process failures remain in the owning Go tests. Do not mark those
boundaries covered solely because this JSON file passes.
