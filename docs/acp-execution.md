# Shared ACP execution

AGH can run Claude through ACP from CLI, Web, DingTalk and Feishu using the
same Host adapter. Routing, authorized context, memory and delivery continue
through Host. The existing print backend remains available when no ACP profile
is selected. [Configuration and implementation details](acp-execution-detail.md).

## Getting started

1. Install the pinned Claude ACP bridge, version **0.81.2**, with Node.js 22 or
   newer. Keep your existing Claude executable, account/provider and model.
2. Create an execution profile from
   [the example](../config/execution.acp.example.json), using the bridge's actual
   absolute executable path.
3. Select that profile for an Agent, or as the managed service default. CLI and
   the Web CLI destination can also select it directly. Channel bindings keep
   selecting their existing Agent.
4. Start a new conversation after changing the execution profile. Verify one
   reply before using it for work. Changing a production service still requires
   applying its configuration and restarting that service deliberately.

The [setup examples](acp-execution-detail.md#configuration) show the exact commands
and configuration fields. Most users only choose the Agent and model; bridge
paths and advanced options stay in configuration.

## Available now

- Streamed Markdown replies, real tool calls/results and parameter updates.
- Compatible native continuation across separate Host CLI invocations.
- Host timeout/cancellation propagated through ACP, followed by owned-process
  cleanup. Web's Stop button uses this path.
- Native plan updates and permission-decision evidence in the Web projection.
- Explicit capability, version, authentication and execution failures. No
  automatic transport, provider or engine fallback.

## Current limits

This first backend supports **Claude ACP 0.81.2**. Codex and other ACP engines,
interactive approval UI, channel Stop controls and file-change cards remain
pending. DingTalk/Feishu use their existing delivery format; selecting ACP does
not add platform streaming edits or interaction buttons automatically.

Each turn owns a bridge process and reconnects to its compatible native session.
There is no warm bridge pool yet. Memory extraction stays on the separately
configured tool-free Claude path. Restricted-member profiles and structured
business output are rejected by the ACP runner until their constraints are
implemented. Existing authorization policy is unchanged.

Host records the native prompt outcome separately from delivery, acceptance
and memory results. Missing ACP cost/usage stays unavailable. This feature does
not switch an already-running production service or share state across channels.
