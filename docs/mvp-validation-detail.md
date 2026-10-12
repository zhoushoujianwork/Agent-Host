# CLI memory MVP details

Historical MVP 1 baseline: synthetic live acceptance and automated checks
passed on 2026-10-10. The current [experience details](experience-memory-detail.md)
extend this baseline with schema version 2, user-statement policy v3, native
tool evidence, extraction and review. The later
[object retrieval extension](object-retrieval.md) adds schema v3 and shared
service/alias/property retrieval. The [DingTalk owner DM extension](dingtalk-channel-detail.md),
implemented on 2026-10-10, adds schema v4 receipt/delivery state and the first
external channel, tested offline with live platform acceptance still open.
Contracts and exclusions below describe the original version. The
[main MVP document](mvp-validation.md) defines scope and the user-facing loop.

## CLI surface

```text
agent-host chat [-m MESSAGE] [--resume SESSION_ID] [options]
agent-host memory search [options] QUERY
agent-host memory show [options] MEMORY_ID
agent-host run show [options] RUN_ID
agent-host run events [options] RUN_ID
agent-host run retry-memory [options] RUN_ID
```

Place options after the command and before positional queries/IDs. Interactive
chat reads one line per turn and waits for memory finalization before reading
the next turn. EOF exits; Ctrl-C cancels and exits. One-shot `-m` / `--message`
uses the same execution path. Input is limited to 16 KiB.

| Option | Meaning and default |
| --- | --- |
| `--home PATH` | Host state directory; `~/.agent-host` |
| `--workspace PATH` | Canonical project scope and native working directory; current directory |
| `--format text\|json` | Terminal renderer or one JSON event per line; `text` |
| `--model MODEL` | Claude model; `AGENT_HOST_MODEL`, otherwise `sonnet` |
| `--claude PATH` | Native executable; `claude` |
| `--resume SESSION_ID` | Continue an eligible host/native session in the same workspace and model |
| `--no-tools` | Explicit tool-free native execution for controlled validation |
| `--jev-provider typesafe\|custom` | Explicit decision provider; `typesafe` |
| `--jev-endpoint URL` | Third-party gateway; required only for `custom` |
| `--jev-model MODEL` | Pinned `jev-1.13.0` |
| `--timeout DURATION` | Deadline per turn including finalization; `10m` |

`memory search`, `memory show`, and `run show` output inspection JSON.
`run show` includes the retained context, decision, and event history.
`run events` replays the persisted journal using the same renderer as live
execution. Read-only inspection does not require model credentials.

The host opens one native conversation per new host session. Resume is rejected
when the workspace/model differs, the previous native outcome is not completed,
or its memory finalization is unresolved. A native resumed conversation may
retain older injected statements; revision notices identify memories that have
since changed or become conflicted. These notices do not erase native history.

## Shared event protocol

`internal/event.Event` is the boundary between runtime and presentation. The
host persists each event before rendering it. Terminal text and NDJSON consume
the same event objects; a future Web transport can replay them without parsing
terminal text. No Web server or browser UI is implemented.

| Field | Meaning |
| --- | --- |
| `version` | Protocol version, currently integer `1` |
| `seq` | Increasing sequence within one run, assigned by SQLite |
| `at` | UTC event timestamp |
| `session_id`, `run_id` | Host identities; separate from native session identity |
| `type` | Observation type, such as `phase.started` or `text.delta` |
| `phase` | Current work stage: request, recall, execute, remember, persist, receipt |
| `component` | Owning component, such as `memory.sqlite`, `harness.claude`, or `memory.jev` |
| `status` | Observed activity/result status |
| `agent`, `model` | Dispatched agent and model when known; resolved values are recorded when observed |
| `message` | Progress explanation, separate from assistant output |
| `delta` | Incremental assistant text for `text.delta` |
| `elapsed_ms` | Time since this execution or explicit memory-retry invocation began |
| `data` | Typed-event payload: native evidence, context, decisions, result fields, or phase duration |

`phase.finished.data.duration_ms` measures that phase. Long phases emit
`phase.progress` every five seconds. `agent.dispatched` retains the requested
Claude model and native session identity; `agent.started`, streaming
observations, and results report observed model information. Jev decisions
record requested and returned model names separately. Retry appends to the same
run sequence; its elapsed timer starts again for the retry invocation.

Illustrative output event, with preceding events omitted:

```json
{"version":1,"seq":8,"at":"2026-10-10T03:00:01Z","session_id":"SESSION_ID","run_id":"RUN_ID","type":"text.delta","phase":"execute","component":"harness.claude","status":"running","agent":"local-claude","model":"OBSERVED_MODEL","delta":"The release codename is Cedar.","elapsed_ms":1042}
```

The journal retains native protocol evidence that the human terminal renderer
may omit. It distinguishes text deltas from tool activity and lifecycle events.
Rendering errors and journal failures are surfaced rather than ignored. A
rendering failure after a valid native result does not rewrite native success;
completed execution and unresolved memory remain independently inspectable.
The final receipt reports `memory_disposition`, including
`quarantined_excluded_from_recall`, so persistence is not confused with active
knowledge or a model's verbal promise to remember.

## Memory policy and evidence

The tested MVP 1 policy was `user-statement-v2`:

1. Only the current exact user input is a candidate. Native assistant output is
   operational evidence, not an independently verified memory source.
2. One request asks two independent questions over the same
   `{source_role: "user", statement: input}` state: `eligibility` selects
   `remember` or `skip`; `memory_kind` selects `preference`, `project_fact`,
   `decision`, `constraint`, or `none`. Neither question depends on the other's
   answer. No arbitrary string extraction or generated summary is requested.
3. Storage requires `eligibility=remember`, probability of `remember` at least
   `0.8`, and a kind other than `none`. A lower eligibility probability or the
   contradictory pair `remember` + `none` produces a recorded abstention. The
   category's own probability does not gate eligibility: overlapping labels can
   split probability even when a statement clearly qualifies for memory. The
   threshold is a policy choice, not a measured guarantee of 80% accuracy. A
   statement longer than 4 KiB also receives an explicit storage-policy
   abstention.
4. A qualifying candidate is compared with every supplied active/conflicted
   statement in the current scope. Independent questions classify each relation
   as `same`, `conflict`, `independent`, or `uncertain`.
5. A duplicate links the new run as evidence without inventing another fact.
   Conflict, uncertainty, or a relation below its `0.8` probability threshold
   quarantines the candidate and implicated records. A duplicate cannot bypass
   a contradiction with another record. Quarantined duplicates stay quarantined.
6. The host validates scope and source, then commits memory revisions, evidence,
   FTS changes, the decision, and finalization status in one transaction.

The policy refuses incomplete comparison caused by more than 100 records or
more than 48 KiB of aggregate source/encoded decision state; it never truncates
the comparison set silently. These limits make exhaustive comparison feasible
for this MVP. There is no automatic correction winner, temporal ordering by
Jev, or conflict-resolution command yet.

Decision audit data records provider, fixed endpoint, requested model, policy
version, separate eligibility/relation thresholds, request state/questions,
both eligibility and category answers, typed relation responses, returned model,
gateway provider metadata when available, and the resulting reason. Model
credentials are not part of this record. Untrusted source text cannot confer
execution or disclosure authority.

## Jev transport

| Explicit selection | Endpoint | Credential environment variable |
| --- | --- | --- |
| `typesafe` (default) | `https://api.typesafe.ai/v1/systemone` | `TYPESAFE_API_KEY` |
| `custom` | `--jev-endpoint URL` | `JEV_API_KEY` |

A custom endpoint must be HTTPS, or HTTP on a loopback host, without user
credentials, query or fragment. It is an explicit alternative, not a fallback. Production code reads the selected
environment variable; it does not execute or parse shell aliases. Earlier
experiments used the separately authorized `llm` alias credential, parsed
without executing its command. That does not authorize sending it to TypeSafe.

Requests have a 30-second HTTP timeout, a 1 MiB encoded request limit, and a
4 MiB response limit. The client follows no redirects, retries no request, and
never switches providers. HTTP status and transport failures are reported
without reflecting response bodies or authorization values into error logs.

The official API allows structured instructions. A third-party gateway rejected
that shape during prior testing, so `custom` clients explicitly encode non-string
instructions as JSON strings. The memory policy uses string instructions for
both providers. Answers must have the requested IDs, matching types, legal
choices/levels, valid usage and probability ranges, and valid numeric fields.
The client preserves original values while allowing 0.02 aggregate probability
rounding and the corresponding bounded rounding of weighted scores.

The 43 successful saved gateway responses are replayed in a protocol regression
test. This is compatibility evidence for those responses, not a new live test.
The [earlier evaluation details](jev-evaluation-detail.md) retain the full
45-request experiment, including its failure and post-hoc diagnostics.

## SQLite and recovery

`--home/state.db` holds sessions, runs, an append-only event journal, current
memories, immutable revisions, source-run evidence, and FTS5 data. SQLite uses
WAL, foreign keys, short transactions, and one writer connection. A process
ownership lock prevents concurrent hosts writing the same home; read-only
inspection remains available. This implementation targets macOS/Linux local
filesystems. Model/network calls occur outside database transactions.

The original MVP recall described below has been extended by
[object-aware retrieval](object-retrieval-detail.md): current chat and memory
search use derived object/topic indexes, active alias associations, FTS and
applicability filtering. The original lexical baseline is retained here as
historical implementation evidence.

Original MVP recall filters by canonical workspace scope and active status. Indexing and
querying share normalization: lowercase alphanumeric words and adjacent CJK
character pairs. This supports two-character Chinese terms without pretending
that SQLite's default tokenizer performs Chinese word segmentation. A lone CJK
character is not a supported FTS query unit. Recall combines persistent
preferences/constraints with lexical matches and injects at most eight records
and 12 KiB of statement text. The exact selected revisions are saved on the run;
the limit is a context budget, not proof of exhaustive relevant recall.

Execution status, acceptance status, and memory status are separate. Eligible
completed runs become memory `pending`; successful finalization is `saved` or
`unchanged`, while a required-step failure becomes `failed`. An interrupted
unrecorded native outcome becomes `unknown` during recovery and is never
automatically replayed. Explicit `run retry-memory` handles pending/failed
finalization of completed executions without dispatching Claude again. A hard
host kill does not prove that native descendants or external effects stopped;
orphan attachment and process-incarnation recovery are not implemented. Unknown
outcomes require inspection, not an automatic replay.

There is no benchmark establishing latency at 10k, 100k, or one million records.
The present application policy reaches its comparison limit long before those
sizes. Bounded object/topic candidate selection is now implemented, but a
large-data query-plan benchmark remains future work, not a delivered scale
guarantee.

## Validation record

The focused Jev/client policy checks currently cover HTTP shapes, provider
selection, no redirects/retries, error redaction, malformed responses, recorded
gateway-response compatibility, low-eligibility abstention, category ambiguity,
contradictory eligibility/category answers, bounded comparison,
duplicate handling, and conflict quarantine. Simulated decision tests validate
host policy handling; they do not establish Jev's semantic accuracy.

The final live report (private validation evidence)
records Claude Code `2.1.223`, resolved Claude model `claude-opus-5-5`, and Jev
`typesafe/jev-1.13-20260917` through the explicitly selected company gateway.
Each invocation used fresh native state, an empty test workspace, and disabled
tools/MCP/hooks. Only the Host SQLite database carried information across runs.

| Live case | Result | Total / Jev phase / SQLite commit |
| --- | --- | --- |
| Save exact user statement | Saved active source-linked memory | 25.15 s / 863 ms / 2 ms |
| Ask from fresh native state | Correct title and injected memory ID cited | 25.29 s / 1525 ms / 1 ms |
| Introduce incompatible title | Both statements quarantined; receipt explicitly excludes them from recall | 20.38 s / 1909 ms / 2 ms |
| Ask after quarantine | No memory injected; answer explicitly unknown | 22.71 s / 887 ms / under 1 ms |

All four runs emitted text before process exit and contiguous event sequences.
Phase timing separates native execution, Jev judgment, and SQLite commit. These
four small local cases do not establish latency percentiles or large-dataset
SQLite performance.

The retained [experiment history](../experiments/mvp/README.md) includes two
failed v1 runs and an intermediate v2 pass. The failures exposed overlapping
category probabilities being mistaken for ineligible memory; v2 independently
asks eligibility and category without lowering the eligibility threshold. The
initial native settings configuration also produced irrelevant text despite
protocol success. The later explicit alias configuration produced useful
answers. These are observations under different configurations, not a controlled
model-quality comparison, and there is no runtime provider fallback.

| Automated validation | Evidence |
| --- | --- |
| Native protocol, completion, errors and cancellation | Real synthetic subprocess fixtures; false-success fields, nonzero exit, missing/malformed/mismatched/duplicate result, callback errors, descendant cancellation |
| Memory failure and independent retry | Host integration with real SQLite and injected decision failure; retry completes without a second native dispatch |
| Execution versus display/persistence | Completed native result remains completed if rendering fails; committed memory is not relabeled failed after output failure |
| Journal and terminal | Contiguous persistence and typed-event replay equality, incremental text, phase durations, control-character stripping, writer failures |
| SQLite and scope | Atomic provenance checks, immutable revisions, dedup evidence, conflict exclusion, fresh recall, Chinese bigrams, ownership lock and recovery |
| CLI | Idle Ctrl-C cancellation, explicit empty-message rejection, configuration errors |
| Jev | HTTP/schema boundaries, two independent classification questions, category ambiguity, eligibility abstention, duplicate/conflict handling, 43 saved gateway responses replayed |

`make check` (formatting, vet, Go tests), `go test -race ./...`, the focused race
recheck of changed host/CLI/rendering code, `make build`, Python syntax checks,
local Markdown links, and credential-leak checks passed. The final report records
the tested binary SHA-256. No secrets are checked into the repository.

Limitations: business acceptance remains `unverified`; live tests disabled tools
and did not exercise approval interaction, real business mutations, or native
session resume. Resume and fault paths have automated protocol/host coverage.
No vector engine, general entity extraction, automatic conflict resolution,
production accuracy study, or 10k/100k/1m-record benchmark is claimed.
