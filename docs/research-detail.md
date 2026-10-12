# Research sources and validation notes

Reviewed: 2026-10-10. The [research conclusions](research.md) contain the product
summary. Upstream documentation and `master`-branch package behavior may change;
recheck them when implementing an adapter.

Jev findings below were also reviewed on this date against TypeSafe's own
documentation, not independently named Jev community sites.

## Official findings

| Topic | Documented behavior | Implication for the proposed host |
| --- | --- | --- |
| [Claude Code programmatic execution](https://code.claude.com/docs/en/headless) | Non-interactive CLI and Agent SDK access; JSON/streamed results and session continuation | A Go adapter can supervise the CLI; runtime behavior still needs validation |
| [Claude hooks](https://code.claude.com/docs/en/hooks) | Lifecycle events such as `Stop`, `SessionEnd`, and tool events | Hooks can add evidence; the host must correlate it with the actual execution |
| [Codex app-server](https://developers.openai.com/codex/app-server/) | Threads, turns, streaming notifications, continuation, and interruption; `turn/completed` carries `completed`, `interrupted`, or `failed` | Use protocol-level outcomes and retain native thread/turn identity |
| [DSH architecture](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/architecture.md) | Cordis plugins can provide model access, tools, sessions, and the agent loop | Useful reference for extension boundaries; adopting it would change the runtime stack |
| [DSH Claude provider](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/subagent/subagent-claude-code/README.md) | Fresh execution per task; no continuation, resume, or progress stream; intermediate tool activity stays outside the parent session | Its existing provider does not cover the intended persistent-session and evidence interface |
| [DSH Codex provider](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/subagent/subagent-codex/README.md) | Fresh app-server/thread/turn per task; the same continuation and progress limitations | The underlying Codex API supports more than this provider currently exposes |
| [DSH repository](https://github.com/deepseek-ai/deepseek-harness) | Node.js/pnpm ecosystem; developer preview with breaking-change warning | Account for stack and upgrade costs if DSH is evaluated as a foundation |

## Design interpretation

The following are our proposed rules, not guarantees made by the upstream
products:

- PID and process exit help supervise a run, but a long-lived session may
  complete a turn without exiting. A changed file does not establish success.
- A hook marks its own lifecycle event. Correlate it with native result events
  and process/transport outcomes before finalizing an execution attempt.
- Native conversation histories remain native. Share source-linked knowledge
  and accepted artifacts rather than assuming histories are portable.
- One owner must coordinate business task recovery and memory finalization.
  Avoid independent retry loops that can duplicate external operations.
- Stable adapters are necessary; correctness also depends on host lifecycle,
  durable state, and memory finalization behavior.

## Jev official findings

Status of this section: official documentation findings. A later
[live gateway experiment](jev-evaluation.md) is reported separately. Jev is now
a bounded memory decision dependency in the [MVP](mvp-validation.md). Its broader
proposed use is described in the
[memory implementation notes](cli-memory-design-detail.md#jev-evaluation-option).

| Official source | Finding relevant to Agent-Host |
| --- | --- |
| [Introduction](https://docs.typesafe.ai/introduction) | Choice selects an enumerated option; Score evaluates an ordered rubric; Noul returns a yes probability. Questions can share a state and run independently. |
| [HTTP API](https://docs.typesafe.ai/api) | Bearer-authenticated `POST https://api.typesafe.ai/v1/systemone`; requests contain `model`, `state`, and `questions`. Choice supports up to 255 options, Score 2–10 levels. Question IDs only correlate answers and do not supply model instructions. Go can call this documented HTTP interface directly. |
| [Models](https://docs.typesafe.ai/models) | Current documented version: `jev-1.13.0`. Input costs $0.042/M tokens; output is free. Limits: 64k total request tokens, and 32k for state plus the longest question. English is strongest; CJK requires evaluation. Version aliases can change, so pin evaluation versions. |
| [Pre-parsed value extraction](https://docs.typesafe.ai/cookbooks/pre_parsed_value_extraction_cookbook) | Code locates candidate spans, the model selects a candidate or none, and code copies/normalizes the source value. Candidate generation is still required. |
| [Entity alignment](https://docs.typesafe.ai/cookbooks/entity_alignment) | Candidate entity pairs are produced first, then judged for likely identity. A wrong merge can contaminate downstream facts; this does not justify automatic registry merges. |
| [Reranking](https://docs.typesafe.ai/cookbooks/rerank_typesafe) | An example retrieves a BM25 shortlist and scores query/candidate pairs. Its published dataset results are not evidence of Chinese memory quality. |
| [Confidence](https://docs.typesafe.ai/confidence) | Choice/Score confidence is derived from their answer distributions, not an independent correctness measurement. Noul has no separate confidence field. Thresholds require application-specific evaluation. |
| [Known limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13) | Numeric/date comparisons, multi-hop reasoning, distracting context, adversarial input, and option order can affect answers. It does not generate free-form values or memory prose. |

The public integration verified in this review is a hosted API. Local deployment
and weight availability have not been established. The model page says requests
are not used for training and enterprise ZDR is available; this is not proof of
zero retention for every account. Check the selected service configuration
before transmitting private memory. No credentials or private records were
submitted during the documentation review. The separately authorized live
experiment used the user's gateway credential and only synthetic records.

TypeSafe's [launch article](https://typesafe.ai/blog/introducing-system-one-models-and-jev)
reports 70–500 ms response times and describes the measurement context as the
US West Coast. Treat those as vendor figures, not latency promises for this
host. Measure actual end-to-end cost and time, including candidate preparation
and any dependent second request. Output shape constraints do not establish
semantic correctness, joint consistency across questions, or injection immunity.

Recommended experiment: keep our entity/FTS candidates fixed and compare the
current deterministic ranking with Jev relevance judgments. Evaluate entity
selection and annotation separately afterward. This isolates a possible benefit
without attributing changes in candidate recall to the ranking model. Keep
none/ambiguous outcomes and test Chinese wording and option-order sensitivity.

## Remaining validation

1. The [MVP](mvp-validation.md) records real native success/streaming and
   automated failure/cancellation checks. Live permission interaction and real
   tool side-effect validation remain outstanding.
2. Native resume has automated adapter/host coverage; verify real continuation
   without losing event identity or confusing turn and process completion.
3. Fresh-session source-linked reuse passed the narrow MVP live cases. Broader
   useful knowledge quality and cross-engine reuse remain unverified.
4. If revisiting DSH, test those same requirements before selecting it as the
   foundation. No DSH runtime integration test has been performed here.
5. Extend the completed synthetic Jev experiment to independently labeled,
   representative Chinese cases and the actual host retrieval baseline. The
   gateway returned a dated model ID, but alias pinning, production calibration,
   gateway billing, and representative end-to-end memory quality remain unverified.

See [architecture implementation notes](architecture-detail.md) for component
ownership. The current [MVP](mvp-validation.md) implements a narrow subset of these runtime
behaviors.
