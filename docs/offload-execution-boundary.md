# Offload as the execution component

Agent-Host uses Offload's public Go package for native execution. ACP, direct
Host-managed Claude execution and the Offload CLI transport are removed.
Production adoption remains separate from this source migration.

## Usage

Run `agent-host chat` for interactive CLI chat, or `agent-host chat -m "task"`
for one turn. Without `--offload-config`, Host selects the explicit `default`
profile in the private Offload `config.json`. An optional version 2 selector
chooses another native config/profile; it does not change execution transport.
Missing configuration fails explicitly.

Host retains tasks, identity, authorization, routing, context, MCP, Skills,
memory, business acceptance and delivery. Offload owns native subprocesses,
timeouts, cancellation, execution history and provider credentials. Native
engines retain their model/tool loops and conversation state.

Every package request carries `caller: "agh"`. Business calls use
`caller_run_id: "agh:<Host run ID>"`; internal model calls use a distinct internal
correlation ID. Execution, history persistence, task acceptance and memory remain
separate outcomes.

## Scope and transition

The full Host maps its Claude permissions, tool controls, MCP/Skills, images,
structured extraction, forks and streamed observations through the package.
The lightweight text Host also uses the package and can use ordinary Claude or
Codex profiles. Full native controls currently require Claude; unsupported
backend capabilities fail before dispatch.

Existing state and historical ACP evidence remain readable. Native bindings
are not relabelled or automatically replayed. Start a new compatible conversation
when changing backend/profile. Existing production configuration must be migrated
explicitly before deploying the replacement binary.

Consolidating the remaining lightweight and full Host orchestration is a separate
step; both now share the package execution boundary. No generic plugin loader,
second model/tool loop or automatic fallback is introduced.

[Implementation and checks](offload-execution-boundary-detail.md).
[Architecture](architecture.md).
