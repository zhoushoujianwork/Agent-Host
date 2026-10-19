# Agent-Host

**Short alias:** `agh` refers to the Agent-Host project.

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
- **Work across projects:** discover local Git checkouts, enroll selected targets
  and route requests while preserving compatible project conversations.
- **Remember useful context:** keep facts as readable documents and observed
  procedures as versioned Skills; recall relevant knowledge and Agent history.
- **Use your sources:** search configured Dokki, Confluence and local Markdown
  on demand; create or update authorized Confluence pages and personal blogs.
- **Operate managed email:** AI lists configured Gmail/163 accounts, selects an
  account, then searches, reads, sends or replies through one email MCP.
- **Identify users:** keep verified private-user profiles and source grants separate.
  All channel users currently share Owner-equivalent execution; approval gating is paused.
- **See what happened:** inspect execution steps, tool results and memory in
  the local workbench or terminal logs; retain encrypted recovery backups.

Claude Code is the current execution engine. Email requires your own account
setup; 163 reads have been tested live. Gmail onboarding and real send/reply
acceptance remain separate checks. Migrated file-memory runtimes automatically
publish selected execution lessons; legacy SQLite runtimes retain `memory accept`.
Codex execution and meeting audio are planned.

## Plugin and integration support

Built-in capabilities use MCP tools or channel adapters:

| Integration | Current support |
| --- | --- |
| Email MCP | Gmail and personal 163 accounts; AI lists accounts and selects one for each read/send operation. |
| Knowledge MCP | Dokki/local Markdown retrieval; Confluence search/read and authorized page/blog writes. Sender grants and execution profiles constrain access. |
| Host MCP tools | Scoped conversation history, eligible memory, current matters and private suggestions. |
| Channel adapters | CLI, DingTalk, Feishu chat and optional Feishu meeting text. |

Accounts and source credentials require local configuration. Arbitrary third-party
MCP registration, a public plugin SDK and a plugin marketplace are not delivered.
Host mounts a private Claude Skill package from the task's eligible learned
procedures. It does not install arbitrary native plugins or rewrite handwritten
skills. See [support details](README-detail.md#plugin-and-integration-boundaries).

## Start

Requires Go 1.26+, Make, Python 3.10+ and Claude Code. Configure your Claude/Jev credentials
and channel settings using the [runtime setup guide](docs/service-install.md).

Clone and compile locally; distribution is source-only:

```sh
git clone https://github.com/zhoushoujianwork/Agent-Host.git
cd Agent-Host
make install
```

`make install` builds the CLI, links it into `~/.local/bin`, and installs the
memory skill for detected Codex, Claude Code, OpenCode, Gemini CLI, Cursor and
GitHub Copilot setups. It reports each result and preserves custom installations.
With private runtime configuration in place, it also starts the enabled channels
and enables automatic startup at login on macOS or Linux with user systemd.
Use `make uninstall-service` to stop and remove that service, or
`make install INSTALL_ARGS=--no-autostart` for CLI and skills only.
Use `make build` to compile only. Installation options and connection setup are
in the [external memory guide](docs/external-memory-skill-design.md).
An existing service can be moved into an independent home-directory runtime;
see [production/development separation](docs/service-install.md#separate-production-from-development).
After migration, `make install` retains the production links; deploying a new
development build remains a separate explicit operation.

After preparing your private configuration, rerun `make install` for background
operation. For a foreground session, remove automatic startup first, then run:

```sh
make run
```

Enabled channel services run together in the foreground, print their workbench URLs and logs
to the terminal. Ctrl-C stops it. Private configuration, credentials and state
live in the Git-ignored `.agent-host/` directory.

The [file-memory guide](docs/file-memory-design.md) explains explicit migration
and verification before enabling `host.json`'s `memory.backend: "files"`.
Published documents and Skills live in `memory/`; rebuildable SQLite indexes and
task context copies live in `cache/`. Operational state remains durable.

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
./bin/agent-host chat --email-config "$PWD/.agent-host/config/datasource.plugins.email.json" \
  -m 'List my managed mailboxes and show the latest three emails in my 163 account.'
```

The email command assumes an account registry you have configured. In channel
chat, `/new` starts a fresh conversation, `/status` shows its status, and `/help`
lists controls. Saved Host memory remains available after `/new`.

## More

[Runtime setup](docs/service-install.md) · [Email setup](docs/email-plugin.md) ·
[DingTalk workspace](docs/dingtalk-workspace.md) ·
[Detailed usage and guides](README-detail.md) · [Architecture](docs/architecture.md) ·
[Progress and next steps](docs/release-follow-up.md) ·
[File memory and Skills](docs/file-memory-design.md) ·
[Workspace layout](docs/workspace-layout.md) ·
[Feedback](docs/feedback.md) · [Development](docs/development.md)

```sh
make check
```

[Apache License 2.0](LICENSE) · [Third-party notices](NOTICE)
