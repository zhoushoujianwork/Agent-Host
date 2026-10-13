# Built-in DingTalk workspace

Status: implemented with automated worker, scope and source-collection checks.
Real owner login and business read/write acceptance remain unverified.
See the [implementation details](dingtalk-workspace-detail.md).

## Goal and usage

Ask the private bot to find a colleague, check appointments, create a task,
read a document or prepare from meeting notes. Agent-Host uses the official
DingTalk Workspace Go package; users do not install a separate `dws` executable.
The application bot's credentials remain separate from the owner's authorized
personal workspace.

When DingTalk is enabled, owner-private tasks automatically receive one built-in
`dingtalk` MCP. Before configuration it reports setup is needed. After setup,
the AI discovers selected products and their official commands on demand.
Group tasks do not receive the owner's personal workspace.

## Setup

1. Copy the [example](../config/data-dingtalk-workspace.example.json) to a private
   file named `data-dingtalk-workspace.json` beside the bot configuration. Use the
   bot owner's exact `corpId:userId`; choose products and an absolute, private
   state directory. Apply mode `0600`. Alternatively set `workspace_config`
   in the bot JSON to the absolute configuration path.
2. Build and authorize the owner through the official login flow:

```sh
make build
./bin/agent-host dingtalk-workspace check --config /absolute/path/data-dingtalk-workspace.json
./bin/agent-host dingtalk-workspace login --config /absolute/path/data-dingtalk-workspace.json
./bin/agent-host dingtalk-workspace status --config /absolute/path/data-dingtalk-workspace.json
```

Use `login --device` for a headless environment. Organization access and product
permissions are checked by DingTalk; a local configuration check does not prove
authorization. For terminal chat, pass `--dingtalk-workspace-config PATH`.

## Current scope

Selected products can include people search, contacts, calendar, tasks,
documents, files, meeting notes, knowledge spaces, AI tables, spreadsheets,
mail, approvals and reports. The AI reads official parameter and risk contracts
before execution. Actual availability depends on the pinned official package
and the account's platform permissions.

Writes require a requested action and a saved operation ID. Official destructive
actions retain their confirmation requirement. Host retries return the saved
attempt rather than executing it again. Unknown outcomes require inspection;
neither worker completion nor platform acceptance proves the business result.

Selected-conversation collection can also use the built-in worker: replace
`dws_executable` in its existing configuration with
`dingtalk_workspace_config`, pointing to the same workspace file. Keep its exact
profile, selected conversations, projects and collection bounds. No general
personal-chat tool is mounted to the AI. Existing external-DWS configurations
remain supported; there is no automatic fallback between the two backends.

Encrypted chat extensions, general IM/event access, raw APIs, account/app
administration and newer native binary payloads are outside this increment.
