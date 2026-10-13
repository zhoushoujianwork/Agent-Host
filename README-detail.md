# Agent-Host usage and guide index

The [homepage](README.md) introduces the features and everyday examples.
This page keeps additional commands, configuration notes and implementation guides.

## Runtime and credentials

`make run` uses `.agent-host/runtime`, with visible foreground logs and a local
workbench. Use `make run RUN_ARGS=--no-browser` to suppress browser launch, or
`RUNTIME_ROOT=/absolute/private/runtime` to select another configured runtime.
See the [runtime guide](docs/service-install.md).

Ordinary CLI state defaults to `.agent-host/cli-state` under its invocation
directory. `--home /absolute/private/state` chooses explicit state for a trial
or inspection. The channel runtime's state is separate from ordinary CLI state.

Claude uses its configured native authentication. Jev uses TypeSafe by default
with `TYPESAFE_API_KEY`; `--jev-provider custom --jev-endpoint URL` selects an
explicit gateway and reads `JEV_API_KEY`. In the channel runtime, configure
provider values in the private `config/environment.json`. There is no provider
fallback. All accounts, credentials and populated runtime files belong in the
ignored `.agent-host/` tree.

`--no-tools` disables native tools and MCP for controlled tests. It prevents
email access. `--allowed-tools` accepts explicit native permission patterns.
Channel Agent instances retain their configured native execution policy;
runtime directories are not an OS isolation boundary.

## Additional CLI examples

Put options before positional queries and IDs. Replace paths and IDs with your
own; inspection of channel runs needs that channel runtime's `--home`.

```sh
./bin/agent-host project list
./bin/agent-host project update --id api --description 'Customer billing API' --alias billing
./bin/agent-host chat --project api --project web -m 'Compare the registered projects.'
./bin/agent-host memory search 'deployment'
./bin/agent-host memory candidates --status pending
./bin/agent-host memory candidate CANDIDATE_ID
./bin/agent-host memory accept --note 'Reviewed the cited evidence' CANDIDATE_ID
./bin/agent-host memory reject --reason 'Not supported by the evidence' CANDIDATE_ID
./bin/agent-host memory invalidate --reason 'Superseded' MEMORY_ID
./bin/agent-host run show RUN_ID
./bin/agent-host run events RUN_ID
./bin/agent-host run retry-memory RUN_ID
./bin/agent-host run retry-experience RUN_ID
./bin/agent-host eval --suite evals/memory-v2.json
./bin/agent-host email check --config "$PWD/.agent-host/email/accounts.json"
./bin/agent-host assistance --help
```

Execution completion, task acceptance and memory persistence are separate.
User statements are attributed evidence; accepted execution lessons retain the
conditions and observations that support them. Source documents and emails do
not grant permissions or prove that a described procedure was executed.

## Guides

| Area | Documentation |
| --- | --- |
| Setup and observation | [Project-local runtime](docs/service-install.md), [execution and memory viewer](tools/run-viewer/README.md) |
| Channels | [DingTalk](docs/dingtalk-channel.md), [Feishu](docs/feishu-channel.md), [Feishu meeting text](docs/feishu-meeting-channel.md) |
| Project work | [Runtime assembly](docs/runtime-assembly.md), [automatic routing](docs/smart-project-routing.md), [Agent instances](docs/agent-instances.md), [attachments and conversations](docs/channel-conversations.md) |
| Email | [Managed Gmail/163 accounts and tools](docs/email-plugin.md) |
| Knowledge | [Remote sources](docs/knowledge-sources.md), [local Markdown and retained notes](docs/local-knowledge.md) |
| Memory and history | [Execution experience](docs/experience-memory.md), [object retrieval](docs/object-retrieval.md), [conversation history](docs/conversation-history.md) |
| Personal assistance | [Current matters and private suggestions](docs/personal-assistant.md) |
| Validation | [Evaluation workflow](docs/evaluation.md), [first CLI MVP](docs/mvp-validation.md), [experience validation](docs/experience-memory-detail.md#validation-status-and-limits) |
| Architecture and roadmap | [Architecture](docs/architecture.md), [release follow-up](docs/release-follow-up.md), [research](docs/research.md) |
| Source setup and feedback | [Clone and run](docs/beta-release.md), [reviewed diagnostic feedback](docs/feedback.md), [development workflow](docs/development.md) |

Guides distinguish delivered features, bounded live acceptance and remaining work.
Historical experiments do not establish general accuracy or production coverage.
