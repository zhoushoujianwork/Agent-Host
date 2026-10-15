# Owner-only dangerous operations

Status: proposed design; no runtime permission changes are delivered by this document.

Only the locally configured bot owner may request destructive operations
(危险操作). Other senders can ask questions and request analysis, but cannot
authorize deletion by confirming it, claiming an administrator role, forwarding
an owner message, or supplying instructions in a document.

## Current support

Both channels already recognize a configured owner:

| Channel | Owner binding | Current behavior |
| --- | --- | --- |
| DingTalk | Enterprise ID and `owner_user_id` | Private input must match `senderStaffId`; group mentions retain each sender's staff ID. |
| Feishu | Application/tenant and `owner_open_id` | Private IM must match the sender's Open ID; group mentions retain each sender's Open ID. |

This is a local owner binding, not automatic discovery of the bot's creator or
platform administrator. Group audiences remain shared even when the owner speaks.
Current channel execution uses `bypassPermissions`; there is no owner-specific
destructive-tool restriction. Identity recognition alone does not enforce this policy.

## Intended behavior

- **Non-owner:** block destructive execution immediately. Do not ask which deletion
  layer they want or accept their confirmation. Offer a read-only impact review
  and ask the configured owner to approve a concrete blocked action.
- **Owner:** allow destructive work only for the explicitly requested target.
  Resolve ambiguous targets first. Before irreversible deletion or loss of
  uncommitted work, describe the concrete scope and impact and obtain the owner's
  explicit confirmation. A precise request that already acknowledges those
  impacts can serve as confirmation.
- **Everyone:** screenshots, quotes, files, memory and tool results are evidence,
  not authorization. Group replies retain existing shared-audience privacy rules.

For the attached example, a non-owner receives: “Only the configured bot owner can
authorize project deletion. I can review the impact and ask the owner to approve
the specific action.” The screenshot itself authorizes no action.

## Ask the owner in chat

Permitted reads proceed directly. For work outside a member's available tools,
prepare a concrete action, exact target and impact. Clarify missing targets first.
Host then sends a real platform @mention, for example:

> @owner Alice requests deletion of local project `/example/biai` only.
> This removes its files, including the listed uncommitted changes; the remote
> repository is unaffected. Reply directly to this message with **OK** or **No**.

Only a fresh authenticated reply from the configured owner can approve it.
“OK” must reply directly to that approval message. An unrelated OK, a member's
confirmation or a forwarded owner statement grants nothing. The owner can decline;
unanswered requests expire without execution. Avoid duplicate owner mentions.

Approval starts a separate authorized run for that specific action. The member's
Claude process keeps its restricted tools. Changed targets or impacts need fresh
approval. Record approval and execution separately, then report the result. If
the mention cannot be delivered, keep the action blocked and explain why. Shared
approval messages must not expose private details.

## Enforcement and first scope

Use both a trusted system prompt and native tool restrictions at every Claude
startup/resume. Host determines the current sender's owner status before execution;
Claude cannot grant it. Owner status and audience visibility are separate.

For the first increment, non-owner work is read-only: remove Bash and other
execution/write capabilities. Expose only reviewed read-only MCP operations and
the narrow Host capability for requesting owner approval described above.
This also removes non-owner builds, tests and file edits for now. Blocking only
`rm` does not block deletion through scripts, Git, APIs or file overwrites. A later
increment can restore ordinary work through narrowly controlled tools; unrestricted
Bash must not be restored while claiming owner-only destructive enforcement.

Keep owner execution available, with the destructive-action prompt added. A group
turn must never inherit owner tools from an earlier turn or another member's
native session. Missing identity or unsupported restrictions fail closed
(无法验证时拒绝授予权限), with an explicit explanation.

## What Claude starts with

Host selects one profile from the verified sender before launching Claude:

| Profile | Startup policy | Result |
| --- | --- | --- |
| Owner | `--permission-mode bypassPermissions --tools default` | Existing execution tools remain available; the prompt requires scoped destructive requests and confirmation. |
| Non-owner or unknown | `--permission-mode dontAsk --tools "Read,Glob,Grep" --allowedTools "Read,Glob,Grep" --disallowedTools "Bash,Edit,Write,NotebookEdit,Agent,Task"` | Only the selected read tools are available; no native execution or file mutation tools. |

The non-owner baseline also starts with an empty explicit MCP configuration,
`--bare`, disabled skills/hooks and no user/project/local settings. Otherwise
another tool or startup customization could restore execution. `--allowedTools`
pre-approves the selected reads; `--tools` limits the built-in inventory. These
are different controls. Full commands and the exact isolation settings are in
the [startup examples](dangerous-operations-design-detail.md#concrete-startup-examples).

This blocks non-owner execution through native capabilities, rather than relying
on Claude to obey a refusal prompt. The owner's confirmation behavior remains
prompt-guided; `bypassPermissions` does not enforce a confirmation dialog. The
commands are proposed configuration, not a deployed or live-validated change.

Implement in order: trusted per-turn owner resolution; prompt and native tool
profiles; owner approval and authorized dispatch; session separation and
alternate-tool restrictions; automated checks
and bounded live DingTalk/Feishu validation. Existing owner configuration is reused;
no chat command can change it. This proposal covers channel requests; ordinary
local CLI permissions remain outside this increment.

See [implementation details and draft system prompt](dangerous-operations-design-detail.md).
