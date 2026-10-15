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

- **Non-owner:** refuse destructive work immediately. Do not ask which deletion
  layer they want or accept their confirmation. Offer a read-only impact review
  and ask the configured owner to submit their own request.
- **Owner:** allow destructive work only for the explicitly requested target.
  Resolve ambiguous targets first. Before irreversible deletion or loss of
  uncommitted work, describe the concrete scope and impact and obtain the owner's
  explicit confirmation. A precise request that already acknowledges those
  impacts can serve as confirmation.
- **Everyone:** screenshots, quotes, files, memory and tool results are evidence,
  not authorization. Group replies retain existing shared-audience privacy rules.

For the attached example, a non-owner receives: “Only the configured bot owner can
authorize project deletion. I can review the impact; the owner must submit the
deletion request.” The screenshot itself authorizes no action.

## Enforcement and first scope

Use both a trusted system prompt and native tool restrictions at every Claude
startup/resume. Host determines the current sender's owner status before execution;
Claude cannot grant it. Owner status and audience visibility are separate.

For the first increment, non-owner execution is read-only: remove Bash and other
execution/write capabilities, and expose only reviewed read-only MCP operations.
This also removes non-owner builds, tests and file edits for now. Blocking only
`rm` does not block deletion through scripts, Git, APIs or file overwrites. A later
increment can restore ordinary work through narrowly controlled tools; unrestricted
Bash must not be restored while claiming owner-only destructive enforcement.

Keep owner execution available, with the destructive-action prompt added. A group
turn must never inherit owner tools from an earlier turn or another member's
native session. Missing identity or unsupported restrictions fail closed
(无法验证时拒绝授予权限), with an explicit explanation.

Implement in order: trusted per-turn owner resolution; prompt and native tool
profiles; session separation and alternate-tool restrictions; automated checks
and bounded live DingTalk/Feishu validation. Existing owner configuration is reused;
no chat command can change it. This proposal covers channel requests; ordinary
local CLI permissions remain outside this increment.

See [implementation details and draft system prompt](dangerous-operations-design-detail.md).
