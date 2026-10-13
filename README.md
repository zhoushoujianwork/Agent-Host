# Agent-Host

Give AI work continuity across conversations and projects.
Agent-Host connects your requests, workspaces, knowledge and mailboxes to
Claude Code, while keeping task history and reusable memory locally.

## Introduction video

https://github.com/user-attachments/assets/aa3b84c9-e689-4231-bff3-a8575509ba76

[Download the introduction (MP4)](docs/media/agent-host-intro.mp4).
Examples in the video are illustrative; current support is listed below.

## What it does

- **Chat where you work:** CLI, DingTalk and Feishu private messages, group
  @mentions, and optional Feishu meeting text.
- **Work across projects:** route requests to registered workspaces and continue
  compatible conversations with their project context.
- **Remember useful context:** reuse saved project facts and accepted execution
  lessons; look up earlier Agent conversations when needed.
- **Read your sources:** search configured Dokki, Confluence and local Markdown
  documents on demand, keeping their source references.
- **Operate managed email:** AI lists configured Gmail/163 accounts, selects an
  account, then searches, reads, sends or replies through one email MCP.
- **See what happened:** inspect execution steps, tool results and memory in
  the local workbench or terminal logs.

Claude Code is the current execution engine. Email requires your own account
setup; 163 reads have been tested live. Gmail onboarding and real send/reply
acceptance remain separate checks. Execution lessons currently require
`memory accept`; Codex execution and meeting audio are planned.

## Plugin and integration support

Built-in capabilities use MCP tools or channel adapters:

| Integration | Current support |
| --- | --- |
| Email MCP | Gmail and personal 163 accounts; AI lists accounts and selects one for each read/send operation. |
| Knowledge MCP | Dokki, Confluence and selected local Markdown sources; search and read on demand. |
| Host MCP tools | Scoped conversation history, eligible memory, current matters and private suggestions. |
| Channel adapters | CLI, DingTalk, Feishu chat and optional Feishu meeting text. |

Accounts and source credentials require local configuration. Arbitrary third-party
MCP registration, a public plugin SDK and a plugin marketplace are not delivered.
Claude-native skills/plugins belong to the selected harness profile; Host does
not install or manage them. See [support details](README-detail.md#plugin-and-integration-boundaries).

## Start

Requires Go 1.26+, Make and Claude Code. Configure your Claude/Jev credentials
and channel settings using the [runtime setup guide](docs/service-install.md).

Clone and compile locally; distribution is source-only:

```sh
git clone https://github.com/zhoushoujianwork/Agent-Host.git
cd Agent-Host
make build
```

After preparing your private configuration, start the channel service:

```sh
make run
```

The channel service runs in the foreground, prints its workbench URL and logs
to the terminal. Ctrl-C stops it. Private configuration, credentials and state
live in the Git-ignored `.agent-host/` directory.

For a terminal conversation, supply your configured Jev credentials and run:

```sh
make build
./bin/agent-host chat -m 'Inspect this project and explain how to run it.'
```

## Try asking

Send these requests privately to your configured bot, or use CLI chat:

| Task | Example request |
| --- | --- |
| Project work | “Find why the billing tests fail and fix the problem.” |
| Recall | “What did we decide about the deployment last time?” |
| Knowledge | “Find the deployment guide and cite the relevant source.” |
| Email | “Show the three latest emails in my 163 account and summarize the newest one.” |
| Email reply | “Use my Gmail account to reply to this message with the text below.” |

CLI examples with explicit project or mailbox configuration:

```sh
./bin/agent-host project add --id api --workspace /absolute/path/api
./bin/agent-host chat --project api -m 'Review the API error handling.'
./bin/agent-host chat --email-config "$PWD/.agent-host/email/accounts.json" \
  -m 'List my managed mailboxes and show the latest three emails in my 163 account.'
```

The email command assumes an account registry you have configured. In channel
chat, `/new` starts a fresh conversation, `/status` shows its status, and `/help`
lists controls. Saved Host memory remains available after `/new`.

## More

[Runtime setup](docs/service-install.md) · [Email setup](docs/email-plugin.md) ·
[Detailed usage and guides](README-detail.md) · [Architecture](docs/architecture.md) ·
[Feedback](docs/feedback.md) · [Development](docs/development.md)

```sh
make check
```

[Apache License 2.0](LICENSE) · [Third-party notices](NOTICE)
