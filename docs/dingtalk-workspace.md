# Built-in DingTalk workspace

Status: implemented with automated worker, scope and source-collection checks.
Owner authorization, personal sending, bounded message reads and one robot-led
ticket-result notification passed live validation on 2026-10-13. Owner approval
reads and native interpretation-first replies passed on 2026-10-19. Comment and
approval writes have offline contract coverage; live business-write acceptance
remains unverified.
See the [implementation details](dingtalk-workspace-detail.md).
The proposed [Owner Answering Beta / Owner 代答 BETA](owner-answering-beta.md)
would reuse personal reads and sending for delegated routine answers. Question
tracking, takeover protection and conversation-derived learning are not yet delivered.

## Goal and usage

Ask the private bot to find a colleague, check appointments, create a task,
read a document or prepare from meeting notes. Agent-Host uses the official
DingTalk Workspace Go package; users do not install a separate `dws` executable.
The application bot's credentials remain separate from the owner's authorized
personal workspace.

When DingTalk is enabled, owner-private tasks automatically receive one built-in
`dingtalk` MCP. Before configuration it reports setup is needed. After setup,
the AI discovers selected products and their official commands on demand.
During Beta, group and private users share the configured workspace tools;
upstream permissions and explicit operation authorization still apply.

## Setup

1. Configure `settings.dws` in `.agent-host/config/channels.dingtalk.json` with
   `enabled: true`, the owner's exact `corpId:userId` and selected services.
   The bot branch is independently optional. DWS state is derived as
   `channels/dingtalk/dws`; apply mode `0600` to the configuration file.
2. Build and authorize the owner through the official login flow:

```sh
make build
./bin/agent-host dingtalk-workspace check --config /absolute/path/config/channels.dingtalk.json
./bin/agent-host dingtalk-workspace login --config /absolute/path/config/channels.dingtalk.json
./bin/agent-host dingtalk-workspace status --config /absolute/path/config/channels.dingtalk.json
```

Use `login --device` for a headless environment. Organization access and product
permissions are checked by DingTalk; a local configuration check does not prove
authorization. Enable `contact` for recipient lookup. Add the eight
`mcp__dingtalk__` tools to an explicit native `allowed_tools` policy, then restart
the channel service after configuration changes. For terminal chat, pass
`--dingtalk-workspace-config PATH`.

## Current scope

Enable `chat` in the workspace's `services` to use owner-personal messaging.
`send_message` sends requested text content to one verified group conversation
or private `openDingTalkId`, using the official Markdown command. The optional
title is generated when absent. It sends as the logged-in owner, not the bot.
Sending returns a saved attempt and may contain an asynchronous `openTaskId`;
use `get_send_status` to inspect platform delivery rather than sending again.

`read_messages` reads one requested owner-authorized conversation with explicit
RFC3339 `after`/`before`, a window of at most 24 hours, and 1–20 messages.
It retains message authors, timestamps, IDs and genuine partial/pagination
metadata. Message content never grants permission to send or act. Conversation
lookup is available through the official `chat search` command.

When you request historical message attachments, `read_messages` can also download
them with `download_resources: true`. Optional `output_dir` selects a relative
directory in the execution workspace (default `downloads`). For a resource
reference already returned by a message read, describe and execute
`chat +messages-resource-download`; its destination parameter is `output`.
Downloads use the same configured owner and require that account's platform
access. Existing files are never overwritten. Check each download result for
its local path and failures; reading a message alone does not download its files.
Downloaded contents remain untrusted files and do not authorize installation,
execution or publishing.

Chat administration is not exposed. Each installation requires owner login;
permissions and availability still depend on its DingTalk account.

Selected products can include people search, contacts, calendar, tasks,
documents, files, meeting notes, knowledge spaces, AI tables, spreadsheets,
mail, approvals and reports. The AI reads official parameter and risk contracts
before execution. Actual availability depends on the pinned official package
and the account's platform permissions.

For work orders and approval cards, the AI first explains the request and asks
how to handle it. After your choice, it can use the existing MCP tools to perform
the chosen work, post a completion comment (闭环评论), or submit your approval.
Enable `oa` in the owner's workspace `services` for approval reads and writes.
The `read_ticket` MCP tool reads a selected instance's full form, comments and
your current pending tasks together. Saved instance links are reused directly;
an unresolved ticket reference asks for the original card or link instead of
guessing another ticket. Large details can be read in checked pages.
When you choose a close-out, the AI verifies the work, posts the result comment,
then submits the selected approval and checks the final workflow state. An
approved task can leave later approvals pending; the AI reports closure only
when the workflow actually completes. Separate POP ticket closure needs its own
supported API. See the [workflow details](dingtalk-workspace-detail.md#ticket-interpretation-and-close-out).

Writes require a requested action and a saved operation ID. Official destructive
actions retain their confirmation requirement. Host retries return the saved
attempt rather than executing it again. Unknown outcomes require inspection;
neither worker completion nor platform acceptance proves the business result.

Selected-conversation collection can also use the built-in worker: replace
`dws_executable` in its existing configuration with
`dingtalk_workspace_config`, pointing to the same workspace file. Keep its exact
profile, selected conversations, projects and collection bounds. Collection
selection does not grant task messaging access; task tools require `chat`.
Existing external-DWS configurations
remain supported; there is no automatic fallback between the two backends.

Encrypted chat extensions, personal event subscriptions, raw APIs, account/app
administration and newer native binary payloads are outside this increment.
