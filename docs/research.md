# Research conclusions

Reviewed: 2026-10-10. These are documentation-based findings and product design
decisions. Agent-Host now has a narrow integrated CLI memory runtime; its
[MVP validation](mvp-validation.md) records the original user-statement results and limitations; the
[reviewed-experience extension](experience-memory.md) adds native tool evidence,
separate extraction and explicit review. DSH
integration remains unvalidated. The separate Jev gateway experiment is a
historical model baseline, not an end-to-end product benchmark.

## Positioning

Agent-Host is a memory-aware outer harness: it coordinates business tasks and
preserves reusable knowledge while existing harnesses execute model/tool loops.
There can be product overlap with those harnesses, but their documented
programmatic interfaces make technical composition possible.
[Claude Code](https://code.claude.com/docs/en/headless) and
[Codex](https://developers.openai.com/codex/app-server/) both expose integration
interfaces.

The intended value is continuity: a later task can use trustworthy knowledge
from earlier work, including when the executing harness changes. This is a
broader product target. A bounded Claude-only reviewed-experience loop is now
demonstrated; cross-harness reuse and general memory quality remain unverified.

## Execution evidence

Prefer supported structured execution signals over PID polling or file-change
inference. Hooks can contribute lifecycle evidence. The proposed host must
distinguish execution completion, task acceptance, and memory persistence;
none alone establishes the other two. The signal choices and their sources are
recorded in the [research notes](research-detail.md).

## Framework choice

DeepSeek Harness (DSH) is a relevant reference and potential integration target.
Its plugin architecture is extensible, but its existing Claude Code and Codex
providers document one-shot execution without continuation or progress
streaming. That leaves work for our session and evidence requirements.
[DSH Claude provider](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/subagent/subagent-claude-code/README.md),
[DSH Codex provider](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/subagent/subagent-codex/README.md).

The current design decision is to build an independent Go host with explicit
adapter boundaries. Add extension points when a working integration needs
them. DSH remains a candidate for future evaluation rather than a dependency.

The [2026-10-23 runtime plugin research](dsh-runtime-plugin-research.md) examines
a separate direction: a main Agent authors and activates a new plugin during a
task. It distinguishes documented DSH mechanisms from unvalidated task benefits,
and compares candidate use cases with ordinary generated scripts and tools.

## Jev as a decision component

Jev is worth evaluating for bounded choices inside our structured memory design:
select a known entity, classify a record, or score a retrieved candidate. Its
official examples combine code-generated candidates with model judgment. See
[candidate extraction](https://docs.typesafe.ai/cookbooks/pre_parsed_value_extraction_cookbook)
and [reranking](https://docs.typesafe.ai/cookbooks/rerank_typesafe).

The host still owns identities, evidence, retrieval, and durable writes. Typed
answers can still be wrong, and the current model documentation says English
performs best. Test Chinese memory tasks before selecting this dependency.
The [research notes](research-detail.md#jev-official-findings) record its API
and limitations. A [live Patsnap gateway evaluation](jev-evaluation.md) found
promising bounded classification results and evidence-policy sensitivity.
Its six simple ranking cases did not outperform the lexical baseline. Broader
Chinese quality and production calibration remain unverified.

See the [architecture](architecture.md) for scope and implementation order, and
the [research notes](research-detail.md) for sources and remaining validation.
