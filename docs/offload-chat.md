# Chat through Offload

The [execution-component direction](offload-execution-boundary.md) defines the
next migration: retain one Agent-Host task flow and use Offload behind its shared
harness interface. This guide describes the current prototype, whose execution
now uses that adapter; common Host orchestration is not yet consolidated.

Use one execution host for Web/Mac Chat, local CLI and DingTalk/Feishu plugins.
Offload owns native execution and conversation history; AGH owns scoped memory,
request receipts and durable outcomes. The Mac app presents the same workspace.

Select a private Offload profile in the workspace's `config/web-chat.json`, or
use `chat --offload-config PATH` for an interactive terminal conversation, or
add `-m MESSAGE` for a single turn. Optional memory uses that same
profile. Answers appear before memory closeout, whose failure remains separate. User
messages display only their original text; internal context uses a separate
instruction channel, and empty memory adds no snapshot.

Version 2 profiles call the pinned public Offload Go package directly. Version 1
retains explicit CLI compatibility. Use a new conversation for the package path;
existing sessions and installed services are not switched automatically.

Web room/request IDs are now supported. Accepted retries return recorded results
without repeating work. Each room runs one request at a time; different rooms
can run concurrently. A busy room reports a conflict for an explicit later retry.
Only the latest compatible, completed receipt can supply native continuation.
Overlapping or interrupted intake uses a fresh session with attributed history.
This keeps Offload's linear native history consistent with the saved receipts.

Existing ACP conversations and running services are not migrated automatically.
Offload room forks, channel takeover, runtime renewal and managed
Host tool injection remain unsupported. Desktop/live acceptance is separate from
headless implementation checks.

Interactive CLI accepts one message per line and continues the same native
session. Blank lines do nothing; EOF exits after the accepted turn finishes.
Ctrl-C cancels execution and exits. A failed turn stops the loop without running
queued input. Use `--resume HOST_SESSION_ID` to reopen an existing conversation;
its runtime and workspace must still match.

[Configuration, continuity and validation details](offload-chat-detail.md).
