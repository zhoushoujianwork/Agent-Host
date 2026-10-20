# Architecture acceptance evidence — 2026-10-20

[Main review](architecture-review-2026-10-20.md) owns the conclusions and scope.
This is bounded acceptance, not a claim that every integration has passed live
platform verification. Implementation inspected through `b2efdf5`; the review
commit adds the two acceptance fixtures and these records.

## Source trace

- `internal/cli/dingtalk.go:composeHost` constructs the common Host, business
  runner, statement decider, independent tool-free experience extractor, judge
  and object extractor. ACP business selection does not change the auxiliary
  extraction transport or silently fall back after failure.
- CLI `runChat` reaches `Host.Run`; Web `configured_launch` validates its
  configured destination and invokes that CLI. IM project resolution uses
  `resolveAgentChannel` and `composeHost`, then `Host.RunChannel`.
- `internal/host/host.go:run` retrieves memory, assembles native Skills and MCP
  mounts, records execution, calls `finalize` on the current user's input, then
  `finalizeExperience` on saved execution evidence.
- `internal/host/experience.go:finishCandidates` automatically compares and
  publishes eligible procedures only on migrated file-memory stores.
- `AutomaticMemorySkipReason` guards unresolved attribution, independent card
  tasks and joint-project runs. Knowledge/history results are excluded from
  execution-experience evidence. Failed publication can be retried without
  replaying business execution.
- History requires a current channel receipt. Owner context, private profiles
  and native sessions retain audience/project boundaries. Equal capabilities
  are asserted only under the same selected Agent and authorization policy.

## Checks

- `make check`: formatting, vet, all Go packages and the repository's Python
  and included Node suites passed. The final review adds the parity regression
  to the same gate.
- `node --test tools/run-viewer/*_test.cjs`: 40 passed, including streaming,
  activity ordering, Agent switching, Markdown safety and UI state.
- `go test -race ./internal/host ./internal/channel ./internal/harness/acp ./internal/assembly`:
  506 tests passed across four packages.
- New `TestCLIAndChannelAgentCapabilityParity`: one selected native Skill,
  one selected knowledge source and a migrated file store; checks both IM
  adapters after project resolution against CLI composition. Three test cases
  passed. This executes no native model or platform API.
- Existing Host tests cover automatic publication, fresh Skill mounting,
  disabled-memory exclusion, rejection of equivalent re-admission, and
  memory-only retry. ACP subprocess tests cover patches, replay, continuation,
  explicit permission policy, cancellation and failure without fallback.

## Real CLI/Web probe

`experiments/runtime/architecture.py` accepts an explicit private connection
and pinned ACP profile. It validates the connection, reuses its provider/model
settings, and creates a new synthetic Agent, native skill, workspace, local
Markdown source, Host state and authoritative file memory under a temporary
root. It does not inherit production datasource credentials or tool mounts.

The CLI receives a plain durable declaration containing a random codename.
A fresh Web conversation must recover it through Host memory, invoke the
selected Skill and use MCP search/read to return a second random marker known
only to the document. Assertions inspect the persisted recalled context,
successful tool results and distinct Host conversation IDs. A subsequent Web
turn must retain both Host and native ACP session IDs.

```sh
make build
python3 -B experiments/runtime/architecture.py \
  --connection /absolute/private/connection.json \
  --execution-config /absolute/private/execution.json \
  --output /absolute/private/fresh-report.json
```

The two-turn observation passed in 27.67 seconds. Observed tools were `Skill`,
`mcp__knowledge_probe_docs__search_local_markdown` and
`mcp__knowledge_probe_docs__read_local_markdown`. CLI memory was `saved`; Web
memory was `unchanged` and experience `empty`, correctly avoiding a procedural
lesson from reference-document retrieval. The final three-turn observation
passed in 35.36 seconds, including explicit successful-result and
native-continuation assertions.

Private evidence is retained in the ignored local
`experiments/runtime/results/agh-architecture-20261020-final.json`.
Earlier reports remain in that directory under the same prefix: two rejected incomplete source
fixtures, then two failed Web starts. The latter exposed a stale executable
during concurrent introduction of a Web-only CLI flag; rebuilding the checkout
resolved it. None of those attempts switched providers or replayed production
tasks. Reports redact provider secrets and temporary workspace roots; temp
state is removed. Original reports also remain in `/tmp`; neither location is
part of the Git commit or a production backup guarantee.

## Real automatic procedure learning

`TestProceduralLearningNativeObservation` passed in 27.54 seconds with the
explicitly configured provider/model. Private report:
`experiments/runtime/results/agh-learning-architecture-20261020.json` (ignored,
owner-private local evidence).

The test replays sanitized historical correction evidence into an isolated
file-memory Host. Real tool-free extraction and Jev admission automatically
publish the supported procedure. A fresh real Claude print session then invokes
the generated Skill and calls a local read-only cloud-CLI surrogate exactly
once, with correct profile, region and JSON arguments. No cloud account is
contacted. The source correction itself is a fixture, not a newly observed
production execution.

Direct lexical recall found zero matches for the short follow-up. The explicit
recent-procedure hint selected one memory; Skill invocation and the first
correct lookup were observed. This proves that bounded hint-based reuse works,
not that arbitrary semantic retrieval works. Channel history supplies these
hints under a conversation binding; standalone CLI/Web do not automatically
gain access to channel history.

## Configuration and remaining gaps

Read-only production inspection found `memory.backend=files`; neither channel's
host profile nor its Agent catalog selected an ACP execution profile. Native
Skills and knowledge selections were unspecified in those catalogs, so existing
inheritance/default policies still apply. This review neither changes that
configuration nor restarts services.

Real platform delivery, mail/Confluence writes, general extraction accuracy,
large-scale recall, proactive conversation learning, second-engine support and
interactive approvals remain outside these observations. Tests and old platform
records do not substitute for current live acceptance of those capabilities.

Scoped source tracing used offload: status `ok`, exit 0, profile `cc`, model
`claude-sonnet-5[1m]`, session `38dc53fd-a8b7-4282-aa97-3ca6ce38745c`, 54 tool
calls, 142.221 seconds, ESTIMATED worker cost USD 2.409699. This excludes the
parent's work and is not verified billing. Key traces were independently checked.
