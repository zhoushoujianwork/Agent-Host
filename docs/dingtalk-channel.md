# DingTalk private and group-mention channel

Status: implemented, with offline adapter and host tests. Production installation
now uses [`make install`](service-install.md) and an isolated user-directory
runtime. Current connection and message-delivery evidence is recorded in the
[deployment record](service-install-detail.md#deployment-evidence). The
[implementation details](dingtalk-channel-detail.md) explain channel boundaries.

## Goal and usage

Send a private message to an application bot in DingTalk, run it through
the existing Claude and memory loop, and receive the final answer in the same
conversation. Text, pictures, files and supported mixed `richText` messages are
accepted. Each message may carry up to four attachments, with a 5 MiB limit per
image and 20 MiB per file. Quoted text or a chat-record summary comes only from
the callback; missing history or quoted media is not fetched automatically.
Unsupported fragment types or attributes reject the whole message. The channel
accepts the configured owner's private messages and verified group mentions
from members of the configured enterprise. Ordinary group traffic is ignored.
Commands also work when a client retains a leading single-token `@name` in a
verified group message. The original message remains available as evidence.
The [instance guide](agent-instances.md) describes the default assistant and
explicit group bindings. Same-group members share context; different groups
and private conversations retain separate learned memory and native sessions.

Messages in each conversation run in order; different audiences can run
concurrently, up to four by default. Conversations sharing an agent/audience
memory partition also stay serial. A turn holds its place through memory
processing and reply delivery. Shared checkouts are not isolated and concurrent
file edits can conflict. See the [conversation and attachment guide](channel-conversations.md)
for configuration, input handling and the separate validation boundary.

The original question shows generic receipt, processing, completion or failure
markers. Detailed execution, knowledge and memory phases remain in the local
event journal and viewer. Fast steps may pass too quickly to appear; the worker
skips obsolete unsent steps to keep feedback current.

A completed marker requires successful processing and a platform-accepted
answer; failures or uncertain outcomes show 打叉. Host controls and unsupported inputs
receive their system reply without Agent progress markers. Markers run
independently, so a slow reaction API does not delay the answer.

The local terminal continues to show live progress and streamed text. DingTalk
gets one final Markdown reply with the answer's headings/lists/code.
The footer shows the actual runtime model and measured execution time as public
information in private chats and groups. Missing measurements are omitted.
Group replies @ the current requester and include a short quoted question
preview, including command, clarification and failure replies. The quote is a
Markdown excerpt; it does not create native reply navigation. Private replies
keep their existing presentation.
Automatic debug information stays in local inspection, including for system
commands. The owner can request diagnostic detail in private chat; group
answers follow the shared-audience prompt.
Failures distinguish startup problems, provider/process interruptions, timeouts,
cancellation and uncertain outcomes, with a supported next step. After execution
starts, the reply explains that partial effects may remain and asks the user to
check the result before resending. Canceled work is described as canceled.
If execution completed but saving failed, the answer remains visible and the
warning asks the user to repeat key context when needed, without rerunning work.
Failure warnings remain above the answer. Long answers include a short
excerpt notice; the complete answer, run ID and separate outcomes remain
available through local status and `run show`. A platform-accepted reply does
not prove that the user read it or accepted the task result.

Describe the task directly. A reliable project association selects that project
or set within the agent's catalog; otherwise the configured agent starts in its
default workspace. An empty catalog runs directly without project setup or a
classifier call. The Host creates no new project questions or waiting requests.
Default-workspace turns retain normal saved-history and memory processing.
The native assistant asks for business details when needed. This does not expand
directory or knowledge access; configuration, selected-directory and provider
errors still fail explicitly.

Project registration and settings stay local; historical manual selections no
longer determine new tasks. Existing waiting requests are preserved without
replay. Only their original requester can explicitly confirm or cancel them;
see the [compatibility notes](smart-project-routing-detail.md#existing-pending-request-compatibility).

| Host control | Behavior |
| --- | --- |
| `/new` | Reset the current compatible native conversation and follow-up routing context; keep memory. |
| `/status` | Show a safe conversation summary and any legacy pending-task count. |
| `/help` | List these controls and explain how to describe a task. |

Only standalone controls are handled by the Host; quoted or attached commands
remain task material. `/status` waits in the conversation queue. These controls
do not forward Claude's interactive commands, and there is no `/stop`. The current
conversation uses the most recently executed project set, or the configured
default when none exists. Tasks continue their resolved set's compatible native
conversation. Parallel multi-project work uses `~/workspace`; a named primary
uses its project cwd. Unassociated work keeps the configured default. Model and tool
settings come from startup; routing selects only registered projects.
After a failed/interrupted turn, the next message automatically uses a fresh
session with a short notice and the same project memory. Users do not need `/new`
to recover. The previous task is retained without replay; an unfinished memory
record does not block a new message.
See the [intelligent routing guide](smart-project-routing.md) and
[runtime assembly guide](runtime-assembly.md) for registration and boundaries.
Business instances use `bypassPermissions`; owner private requests have no added
Host approval or disclosure restrictions. Group instructions consider affected
resources, whether the result can be shared and the authenticated sender. This
prompt behavior does not provide hard access isolation.
This increment has no remote permission dialog
or memory-review interface.

## Prepare and start

Owner-private tasks automatically discover the [built-in DingTalk workspace](dingtalk-workspace.md).
It uses separate personal authorization for selected business products and
does not require installing DWS. Add `workspace_config` to the bot JSON, or
place `data-dingtalk-workspace.json` beside it. Without setup, the tool reports the
required configuration; bot reception and ordinary replies remain available.
Enable its `chat` service for owner-personal message sending, asynchronous send
status and bounded conversation reads. These use the owner's personal login;
normal replies to incoming channel requests continue to use the application bot.

For daily use, follow the [production installation guide](service-install.md):
prepare private configuration once, then use `make install` to install and start.
The commands below remain the foreground development workflow; use an explicit
test `--home` and a different bot when production is running.

1. Choose an existing application bot or create one with Stream reception
   enabled. Copy the
   [example configuration](../config/channel-dingtalk.example.json) into a private local
   configuration file and fill in the application, robot code, enterprise, and
   owner IDs. `robot_code` is explicit; it is not inferred from the application ID.
2. Put the application secret in a separate regular file with mode `0600` or
   `0400`; configure its absolute path. Prepare native Claude authentication and
   the selected Jev provider credential as for local chat.
3. Validate locally, then start the foreground receiver after deciding which bot
   will serve this channel:

```sh
make build
./bin/agent-host dingtalk check --config /absolute/path/dingtalk.json --workspace /absolute/path/project
./bin/agent-host dingtalk serve --config /absolute/path/dingtalk.json --workspace /absolute/path/project
./bin/agent-host dingtalk status
./bin/agent-host run show --workspace /absolute/path/project RUN_ID
```

`check` makes no platform connection and does not verify native authentication.
Use `--max-concurrent-conversations N` on `serve` to choose 1–32 active
conversations; omission uses four. Inputs may include attachments, but replies
remain text/Markdown; sending generated images or files is not implemented.
Use the same `--home` on every command if isolating state from local chat.
`serve` holds the local state lock: stop it before memory review, memory retry,
or another writer uses that home. Read-only status and run inspection remain
available. Ctrl-C stops the receiver and cancels active execution.
Status inspection also shows each marker's accepted, failed, or unknown outcome.
Marker failures remain visible locally and do not produce substitute text notices
or replay the AI task. Messages received before this feature are not marked
retroactively. The owner confirmed live marker display on 2026-10-10, and the
platform accepted all three success stages and both preceding removals.

## First live rollout

The owner authorized switching the existing memgov bot and stopping its old
receiver/group service. Do not run both receivers against the same application.
The [deployment record](service-install-detail.md#deployment-evidence) tracks the
actual cutover independently from offline tests.

The historical dws private-chat check, using the former project-switch commands,
verified project switching, native
follow-up, current-project `/new`, scoped memory recall and missing-directory
failure using isolated test state with the existing bot. Production was restored
and its default-project command round trip succeeded. See the
[platform acceptance record](runtime-assembly-detail.md#live-dingtalk-acceptance-with-dws).
Rejection of another sender and controlled interruption remain separately
unverified on the live platform. The newer inference, project-set and numbered
clarification checks have their own
[acceptance record](smart-project-routing-detail.md#validation-and-acceptance);
the earlier switching sequence is not their platform validation.
Bounded single-group mention, restart-continuation, command/reset and disclosure
checks are recorded separately in the [instance validation record](agent-instances-detail.md#validation-record).
These historical text-only checks do not establish acceptance of concurrent
conversation execution, attachments or inbound quotes; that increment's
[validation boundary](channel-conversations-detail.md#migration-recovery-and-validation)
is tracked separately.

Temporary connection loss reconnects automatically while running tasks continue.
Recovery progress remains visible in the local log. The installed macOS service
also restarts after unexpected process failure and uses `make install` for
explicit upgrades/restarts. Locally recorded duplicate messages
do not execute again. Interrupted claimed tasks and attempted replies are
marked unknown after restart and are not automatically replayed. Platform
redelivery is not guaranteed. Inspect the retained status before deciding what
to do next.
