# Shared ACP execution details

Companion to [the user guide](acp-execution.md). Scope follows the
[architecture](architecture.md#shared-acp-execution).

## Configuration

Install the known bridge outside the checkout, for example:

```sh
npm install --prefix "$HOME/.local/share/agent-host-acp" --ignore-scripts \
  --no-audit --no-fund @agentclientprotocol/claude-agent-acp@0.81.2
```

Node.js 22+ and an existing Claude Code executable are required. AGH selects
the configured `--claude` executable using `CLAUDE_CODE_EXECUTABLE`; the bridge
does not choose a different native installation. Provider credentials stay in
the existing server-side environment/native profile, outside this JSON:

```json
{
  "version": 1,
  "transport": "acp",
  "engine": "claude",
  "command": "/Users/you/.local/share/agent-host-acp/node_modules/.bin/claude-agent-acp",
  "adapter_version": "0.81.2"
}
```

Use an actual absolute path; the profile loader does not expand `$HOME` or `~`.
Optional `args` is an argument array for an explicitly configured launcher.
Unknown fields, engines, transport/version combinations and unavailable commands
fail validation. A missing `execution_config` retains the existing print path;
selecting ACP never falls back after failure.

An Agent can reference the profile relative to its own catalog file:

```json
{
  "default_agent": "assistant",
  "agents": [
    {
      "id": "assistant",
      "name": "Assistant",
      "execution_config": "execution.acp.json",
      "projects": []
    }
  ]
}
```

CLI can instead pass `--execution-config /absolute/path/execution.acp.json`.
The Web handoff's saved `args` accepts the same flag. Managed `host.json`
profiles can set an absolute `execution_config` within their `execution` object;
the same field works in the explicit legacy service configuration. Agent
selection overrides the service/CLI default. The existing Claude executable and
Jev settings are still required for their native/auxiliary responsibilities.
Check local service configuration before restarting the intended instance;
this does not probe the remote model or connect a channel.

## Execution boundary and compatibility

`internal/harness/acp` owns bounded newline-delimited JSON-RPC, ACP v1 setup,
session restoration, prompt updates, nested permission requests and cancellation.
`internal/harness/claude/acp.go` validates the native request and supplies the
known bridge's SDK options: Host/project instructions, model, strict MCP set,
task-scoped validated Skill plugins, additional directories and tool settings.
Initialization verifies the reported package identity and pinned version before
session/prompt. Opt-in native initialization evidence passes the existing strict
MCP admission check before business output is accepted; missing/unconnected or
unconfigured servers fail explicitly. Only the sanitized inventory is projected.
Actual model metadata comes from native observations, not the ACP `default`
configuration alias. Unsupported required features fail rather than weaken policy.
The bridge's provider-specific metadata is documented in its
[pinned source](https://github.com/agentclientprotocol/claude-agent-acp/tree/v0.81.2).

The configured profile fingerprint participates in runtime assembly. Switching
print/ACP, command/profile or engine therefore cannot reuse an incompatible Host
conversation. `NativeSessionID` remains the Host's native-binding key in existing
state schemas. Private `native/agent-host-acp/sessions/<hashed-key>.json` files
bind that key to the actual opaque ACP session and execution identity. New
bindings cannot overwrite existing ones; resume requires an exact compatible
binding. Native ACP IDs are also retained in execution event evidence. This
avoids changing existing print transcripts or adding a state schema migration.

Each turn starts and owns its bridge process. It negotiates resume support,
prefers `session/resume`, otherwise uses supported `session/load`. Setup replay
is excluded from fresh answer/tool delivery and learning; failed restoration
does not create another native session automatically. Host/channel scheduling
continues to serialize compatible turns. Long-lived bridge reuse is future work.

## Evidence, cancellation and policy

Host records ACP observations as `harness.acp`, retaining raw session updates
and normalized fields. Message chunks become `text.delta`; tool IDs have one
`call`, zero or more `update` patches, and one observed `result`. Evidence
assembly validates patches and binds final arguments to their journal sequence.
The Web projection and inspector update the same call instead of counting it
again. Native plans update in place. Images/binary output are omitted from public
journal envelopes; incoming images still follow existing byte/type limits.

Cancellation sends `session/cancel`, retains partial text and waits up to three
seconds for the prompt result before stopping the owned process tree. A separate
four-second watchdog also interrupts a blocked protocol write. Shared Unix
supervision freezes and terminates owned descendants, including detached tool
shells. No other conversation shares this bridge process. Prompt stop reasons
determine the task outcome; stopping an idle bridge after a valid result is
resource cleanup, not proof of task failure or success.

The current `bypassPermissions` execution policy may select an `allow_once`
permission option. Default mode cancels and explicitly reports a permission
request because interactive approval is not delivered yet. Decisions are
journaled before allowing the call; a journaling failure cancels it. Unsupported
client filesystem/terminal/elicitation calls fail explicitly, and these client
capabilities are not advertised. This adds no channel approval or Stop command.

Bridge stderr and RPC error messages may contain account/configuration details.
They remain bounded to 64 KiB in owner-private native profile diagnostics;
public errors expose the method/error code without those payloads. Native usage
projection is shared by channel rendering; ACP's missing billing is not inferred
from auxiliary extractor cost. Auxiliary extraction remains a separate print
runner with its existing restrictions, not a runtime fallback for business ACP.

## Validation

- ACP subprocess fixtures cover partial tool patches, result ordering, replay
  exclusion, continuation, permission decisions, cancellation receipts, callback
  failure, version mismatch and process/transport failure.
- CLI/channel composition tests cover the common flag, service arguments,
  Agent-relative profiles, runtime fingerprint rotation and DingTalk/Feishu
  selection of the shared runner without platform side effects.
- Python Web tests cover ACP model metadata, tool upserts, literal results and
  in-place plan updates. Existing Markdown, send/retry and Stop tests remain.
- An isolated real-provider check on 2026-10-20 completed one Bash `printf`,
  observed eight answer chunks, then resumed the native session and recalled its
  output. A separate running `sleep 60` was canceled: tracked owned processes
  exited, execution was `canceled`, acceptance `unverified`, memory `skipped`.
  The receipt's aggregate `failed` status reflects the interrupted request;
  it must not be confused with its separate execution status.

These checks do not establish real DingTalk/Feishu platform acceptance, Codex
support, file-diff completeness or production quality.
