# Agent-Host

A memory-aware host for AI work, implemented in Go.

Agent-Host connects workspaces and user requests to native agent harnesses.
The host owns cross-session memory and execution evidence; the native harness
owns its model/tool loop and permissions.

Try the [independent beta](docs/beta-release.md) with your own accounts and local
memory. Use the [feedback guide](docs/feedback.md) to generate a reviewed local
diagnostic summary and report runtime or memory problems. Licensed under
[Apache License 2.0](LICENSE); dependency notices remain separate.

## Intro video

https://github.com/user-attachments/assets/aa3b84c9-e689-4231-bff3-a8575509ba76

Play the 80-second introduction above, or [download the MP4](docs/media/agent-host-intro.mp4) (MP4,
1080p, 6.3 MB, Chinese on-screen text, synthesized music, no narration). It
covers the problem, architecture, reviewed memory loop, DingTalk channel,
automatic project routing, on-demand knowledge sources, getting started, and
what remains planned. Example conversations and terminal output in the video
are illustrative and labeled as such.

## Current status

The local CLI implements native Claude execution, live phase/text events,
SQLite evidence and scoped memory, Jev decisions, and independent memory retry.
The experience MVP adds [reviewed execution experience](docs/experience-memory.md):
record native tool results, propose reusable procedures, review them, then recall
accepted records in a fresh task. Attributed user statements remain a separate
source type. Neither source type independently verifies external outcomes.
The [first MVP validation](docs/mvp-validation.md) records the earlier live
user-statement loop. The [experience validation](docs/experience-memory-detail.md#validation-status-and-limits)
records a passing real edit/test, review, fresh-session reuse and invalidation loop.
The [object retrieval extension](docs/object-retrieval.md) adds source-linked
service names, explicit aliases and bounded property matching to both chat and
memory search. The first [DingTalk channel](docs/dingtalk-channel.md) implements
owner private messages through an application bot, durable receipt and
one final reply, and original-message progress reactions. Its adapter and host path are tested offline; the
[production service](docs/service-install.md) has completed its first live owner
DM through platform-accepted reply. Broader live acceptance remains open.
The local read-only [execution and memory viewer](tools/run-viewer/README.md)
renders saved traces, memory provenance, object/topic links and recall observations.
The [project routing and runtime assembly](docs/runtime-assembly.md) select
registered directories, scoped knowledge and separate native sessions.
The [intelligent routing extension](docs/smart-project-routing.md) adds automatic
project recognition and project sets. Reliable associations select registered
projects; other requests run in the agent's default workspace, including with an
empty catalog. The native agent asks for task details when needed.
Joint tasks can access selected project directories and existing scoped memory;
automatic new memory and experience attribution for those tasks is deferred.
Requests without a reliable project match keep normal default-workspace history
and memory processing, whether or not the catalog is empty. Historical unresolved
runs retain their skipped learning records and remain readable in that scope.
Projects within an agent share its Claude configuration; native auto memory is disabled.
The [knowledge-source integration](docs/knowledge-sources.md) mounts selected
Dokki/Confluence read-only MCP tools for runtime search and page reading. The
[local knowledge increment](docs/local-knowledge.md) adds selected Markdown
documents and owner-only retained snapshots through the same tool boundary,
with separate acceptance. No document is fetched before an agent tool call.
The [conversation-history routes](docs/conversation-history.md) add automatic
read-only search/read over saved DingTalk Agent interactions, without DWS, and
an optional separate system DWS collector for selected daily conversations.
DWS topics support local review and owner-private evidence export; acceptance
starts no task and publishes no memory. Conversation-derived learning remains
planned. Native and selected-source acceptance are tracked separately.
Enterprise group mentions and default/group-bound agent instances are implemented.
The [conversation and attachment increment](docs/channel-conversations.md) adds
per-conversation ordering with bounded parallel audiences, picture/file inputs
and callback-provided quotes. Its automated, native and platform validation are
tracked separately from earlier text-only acceptance.
Local/GitHub/GitLab project discovery, per-project execution profiles, Codex, general
entity extraction, vector retrieval and a Web interface for chat or memory changes remain planned.

The [Feishu meeting text channel](docs/feishu-meeting-channel.md) connects one
explicit meeting through a local `lark-cli` profile to the existing host/harness
path. Its two-turn owner acceptance passed on 2026-10-12 with both text replies
accepted by Feishu. The guide tracks those results separately from the installed
DingTalk service. Meeting audio remains future work.

The [evaluation guide](docs/evaluation.md) defines categorized examples and the
workflow for retaining daily incidents as regression cases. `agent-host eval`
checks saved-memory retrieval separately from live Jev decisions. The expanded
42-case suite preserves the original labels and adds retrieval counterexamples.
On the same 26 recall cases, the old path passed 13 and the object-aware path
passed all 26. The historical live Jev run remains 15/16; its withheld negative
lesson is unresolved, and the live decision suite was not rerun for this change.

## Use

Requires Go 1.26+, Make, and Claude Code. Runtime supports macOS/Linux and local
filesystems. Configure `TYPESAFE_API_KEY` securely in the environment; Claude
uses its native authentication and settings.

```sh
make build
./bin/agent-host project add --id api --workspace /absolute/path/api
./bin/agent-host project add --id web --workspace /absolute/path/web
./bin/agent-host project update --id api --description 'Customer billing API' --alias billing
./bin/agent-host project list
./bin/agent-host chat --project api --model sonnet
./bin/agent-host chat --project api --project web -m 'Compare the two registered projects.'
./bin/agent-host chat --model sonnet -m 'This project uses Go.'
./bin/agent-host chat --format json -m 'Which language does this project use?'
./bin/agent-host memory search 'project'
./bin/agent-host run show RUN_ID
./bin/agent-host run retry-memory RUN_ID
./bin/agent-host memory candidates --status pending
./bin/agent-host memory candidate CANDIDATE_ID
./bin/agent-host memory accept --note 'Reviewed the cited tool results' CANDIDATE_ID
./bin/agent-host memory reject --reason 'The evidence does not support this lesson' CANDIDATE_ID
./bin/agent-host memory invalidate --reason 'This procedure is obsolete' MEMORY_ID
./bin/agent-host run retry-experience RUN_ID
./bin/agent-host eval --suite evals/memory-v2.json
./bin/agent-host eval --suite evals/memory-v2.json --mode live
```

Ordinary CLI and channel replies lead with a concise, conversational answer while
keeping essential facts, actions and caveats. Routine replies aim for 2–4 short
sentences or up to three compact bullets; more detail remains available when
requested or needed. Answers that use external sources link to the actual pages
consulted. This guidance also applies with `--no-tools`; structured memory extraction keeps
its separate output requirements.

Jev uses the official TypeSafe API by default with `TYPESAFE_API_KEY`.
`--jev-provider custom --jev-endpoint URL` selects your own System One gateway
and reads `JEV_API_KEY`. There is no automatic provider fallback.
State is stored in `~/.agent-host`; `--home` isolates a test installation.
Registered projects have stable memory scopes independent of their directories.
Use `--project ID`, or an exact registered current directory/`--workspace` match.
Unregistered directories retain their existing directory-based scopes.
Explicit `--project` and `--workspace` are mutually exclusive. Chat accepts
repeated `--project` options; joint work uses `~/workspace` unless `--primary-project ID` selects one of its
projects. The configured default for ordinary work and its memory stay unchanged.
In DingTalk, describe requests directly: every task automatically routes within
the configured agent's project catalog when a reliable association exists, and
otherwise runs in its default workspace. No project setup or Host project
question blocks a new task. Existing waiting requests remain available only for
their original requester's explicit confirmation or cancellation. `/new`, `/status` and `/help`
are the only Host conversation controls; project and agent settings remain local.
These standalone controls share the conversation queue; quoted or attached
commands are material, not controls. They do not forward native interactive
commands. Put CLI options before
positional IDs/queries. `--no-tools` explicitly disables native tools,
MCP, custom hooks and skill commands for controlled tests (managed policy still applies); normal execution retains
native permissions. Configured channel/Agent instances use `bypassPermissions`
with behavioral disclosure rules; this does not provide filesystem isolation. Optional
`--allowed-tools` takes explicit native tool permission patterns, as repeated
options or a comma-separated list. It cannot be combined with `--no-tools`.
Experience extraction always runs separately with tools disabled. Native
Claude 2.1.223 was exercised; older versions may lack the extraction flags.

See [CLI and event details](docs/mvp-validation-detail.md) for resumed sessions,
conflicts, NDJSON output, and event replay. See the
[experience guide](docs/experience-memory.md) for review and expiration.
The [DingTalk guide](docs/dingtalk-channel.md) covers local configuration checks,
foreground serving and status inspection. Use the
[Feishu meeting guide](docs/feishu-meeting-channel.md) for the separate
`feishu-meeting check`, `serve` and `status` workflow. Use the
[production installation guide](docs/service-install.md) for `make install` and
the separate runtime under `~/.agent-host/production`.
The [agent instance guide](docs/agent-instances.md) explains enterprise group
mentions, default and group-bound roles, and separate group conversations and
memory. Local `--agent ID --audience ID` selects the run/memory partition.
The retrieval registry is bounded to 200 eligible memories per scope; native
chat retains its separate 100-memory Jev comparison limit. These bounds are not
production memory capacity claims.

## Development

```sh
make check
make build
./bin/agent-host --help
```

The local module is `agent-host`; choose a canonical import path when a remote
repository is selected. Tests use real temporary SQLite databases, synthetic
native subprocesses, and HTTP fixtures. The [live MVP experiment](experiments/mvp/README.md)
records actual provider observations separately.

## Design and research

- [Independent beta](docs/beta-release.md) and [feedback](docs/feedback.md):
  installation, explicit diagnostic sharing and issue-based feedback.
- [Release record: everyday reliability](docs/release-daily-use.md): delivered
  continuity, audience prompts, failure recovery and public runtime footers.
- [Release follow-up](docs/release-follow-up.md): remaining platform checks,
  daily-use observation, database diagnosis and deferred increments.
- [Agent conversation history and topic discovery](docs/conversation-history.md):
  built-in history tools for Agent interactions and an optional system
  DWS discovery path with local topic review.
- [External knowledge and everyday experience](docs/knowledge-sources.md): implemented
  Dokki/Confluence runtime MCP tools, selected DWS topic discovery and deferred
  conversation learning.
- [Local project knowledge and retained notes](docs/local-knowledge.md): explicit
  Markdown sources, audience grants and provenance-preserving private snapshots.
- [Project routing and runtime assembly](docs/runtime-assembly.md): local project
  registration, automatic DingTalk routing, native cwd and compatible conversations.
- [Intelligent project routing](docs/smart-project-routing.md): registered-project
  inference, project sets, default execution and future discovery boundaries.
- [Production service installation](docs/service-install.md): one-command macOS
  installation/startup, private runtime configuration and development isolation.
- [Agent instances and group mentions](docs/agent-instances.md): configurable
  responsibilities, private/public output and audience-partitioned memory.
- [Conversation execution and attachments](docs/channel-conversations.md):
  ordered conversations, bounded concurrency, picture/file inputs and quotes.
- [DingTalk channel](docs/dingtalk-channel.md): the first external
  channel, offline validation, configuration and live rollout boundaries.
- [Feishu meeting text channel](docs/feishu-meeting-channel.md): named-profile
  foreground polling, explicitly addressed text, meeting context and voice prerequisites.
- [Reviewed execution experience](docs/experience-memory.md): the current experience MVP and
  its review workflow and live validation.
- [Object-aware memory retrieval](docs/object-retrieval.md): shared read/write
  annotations, scoped aliases, property filtering and bounded validation.
- [Memory evaluation and regression](docs/evaluation.md): categorized fixtures,
  incident capture, deterministic checks and separate live Jev observations.
- [First MVP scope and validation](docs/mvp-validation.md): the earlier
  user-statement loop and retained experiment results.
- [Architecture](docs/architecture.md) and [details](docs/architecture-detail.md):
  ownership and component boundaries.
- [CLI memory roadmap](docs/cli-memory-design.md): broader planned entity,
  extraction, retrieval, and recovery work beyond the narrow MVP.
- [Research conclusions](docs/research.md): product positioning and evidence.
- [Jev baseline evaluation](docs/jev-evaluation.md): historical synthetic gateway
  measurements, including classification errors and limitations.
