# CLI task handoff reference

## Configured launcher

When `~/.config/agent-host/task-handoff.json` exists, use this skill's
`scripts/chat.py --check`, then `scripts/chat.py -m MESSAGE`. Resolve the skill
directory before invoking its script. It defaults to JSON events and accepts
`--resume SESSION_ID` and `--format text|json`. A nonzero child exit is preserved;
there is no provider fallback or automatic replay.

The private JSON selects `binary`, `cwd`, `environment_file`, and `args` (CLI
options). `args` must explicitly select `--home`, `--agents-config`, `--agent`,
`--workspace`, and `--native-profile`. Keep credential values only in the private
environment file, using the same documented provider keys as the service. The
launcher clears inherited provider credentials before loading that file.

CLI chat starts its own Host; it does **not** submit work to a running channel
service. Use the existing dedicated `ROOT/cli-state`, not
`ROOT/channels/<channel>/state`. A channel's Agent catalog may reference projects
registered only in that channel. Configure a separate daily-task Agent catalog
with the intended workspace and no unrelated project bindings; do not copy the
channel catalog wholesale. A locked CLI home remains an explicit blocker; do
not delete lock files or create a fresh home to evade ownership.

## Direct CLI

Resolve `agent-host` from the configured executable or PATH. When installed as
a source link, resolve the skill directory and use `../../bin/agent-host` from
that resolved directory if needed. Do not build, install or restart a runtime
merely to discover the target. Review a checkout's production migration receipt
before service operations.

Use an existing trusted configuration to identify the Host state home and Agent
catalog. Confirm the selected Agent ID in that catalog; `default` is only
appropriate when it is the intended daily-task Agent. Include the same explicit
runtime/configuration flags on follow-up and inspection commands. Chat does not
inherit a running channel service's configuration or memory connection alias.
Pass its required `--agents-config`, `--knowledge-config`, `--email-config` or
`--dingtalk-workspace-config` paths explicitly when needed; do not copy secrets
into the task text. Native Claude and configured Jev authentication must already
be available; report missing setup without switching engines.

For example, replace every placeholder below from the intended configuration:

```sh
agent-host chat --home /absolute/host/cli-state \
  --agents-config /absolute/config/agents.json --agent DAILY_AGENT_ID \
  --workspace /absolute/workspace --format json \
  -m 'Objective: summarize the named source. Scope: read-only; no external sends or file changes. Return cited findings and unresolved questions. Execute directly; do not delegate this task again. If scope or design is unclear, report BLOCKED with evidence.'
```

Use `--project REGISTERED_ID` instead of `--workspace` for a known registered
project. Those selectors are mutually exclusive. `--agent` selects a configured
Host role, not a Codex/Claude backend. Host currently executes through Claude;
Codex execution inside Host is a scaffold. External Codex and Claude can both
call this CLI without becoming Host execution adapters.

`-m` runs one turn and exits; messages are limited to 16 KiB. Supply concise
instructions and authorized file references rather than full histories. Use
structured process arguments for dynamic messages or safe shell quoting; never
interpolate source text as shell code. `--format json` emits NDJSON events, not
one offload-style result object. Preserve the output in a private task log and
extract `session_id`, `run_id`, `type`, `status`, and relevant `text.delta` data.
Do not treat the last event, a memory receipt, or exit zero alone as business
acceptance. Check execution evidence and the requested result.

Inspect an observed run without replaying it:

```sh
agent-host run show --home /absolute/host/cli-state RUN_ID
agent-host run events --home /absolute/host/cli-state RUN_ID
```

To send a resolved decision or follow-up, use `chat --resume SESSION_ID -m ...`
with the same runtime/configuration flags. This submits a new execution turn;
it is not a status query. Runtime changes can start a fresh session, so retain
the actual IDs returned. A memory-only failure is a separate outcome; never
rerun business work to repair memory. A timeout may leave partial effects:
inspect evidence and external state before deciding whether continuation is safe.
