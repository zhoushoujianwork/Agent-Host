# Feishu text channel

Status: owner private text and group @mention handling are implemented and
assembled checks passed. One ordinary private greeting and its visible reply
passed a bounded live check, and the user later confirmed rich-text rendering.
Group and multi-turn IM acceptance remain pending. Optional progress reactions
are implemented and assembled checks passed. After correcting the listener's
auxiliary event registration, fresh owner-private tasks passed received,
processing and completed markers plus reply delivery. Group and failed-marker
platform acceptance remain pending.
Meeting text and automatic re-entry have separate passing live evidence in the
[meeting guide](feishu-meeting-channel.md). [Implementation details](feishu-channel-detail.md)
cover permissions, delivery and recovery.

## How to use it

Use one Feishu bot for three conversations:

| Where | How to ask | Who can ask |
| --- | --- | --- |
| Private chat | Send a normal text question; no prefix needed | The explicitly configured owner |
| Ordinary group chat | @mention the bot, then ask | Human members of the configured tenant |
| Meeting chat | Start with `AI:` or `AI：`; no @mention needed | Configured meeting senders |

The [owner-only operation profiles](dangerous-operations-design.md) restrict
non-owner execution and support correlated owner approval replies in IM groups.
Bare-profile provider credentials and live approval acceptance remain separate
requirements; meetings do not send approval prompts.

The optional [users.json registry](workspace-layout.md#multiple-users-and-external-knowledge)
also enables private questions from other authenticated same-tenant users. Each
has a separate private audience and profile; non-owner/shared tasks need explicit
external-source grants. Without the registry the table retains its owner-only
private default. Multi-person live acceptance remains pending.

Owner private chat uses the owner's audience. Each group and meeting has its
own public audience and conversation history; an owner asking in a group or
meeting does not bring private-chat permissions into that shared conversation.
Answers return to the source conversation and start with `[AI]`.
Private/group replies use Feishu rich text to render its supported Markdown
formatting. Meeting replies keep the meeting text format.

One local `lark-cli` event connection handles IM and optional meeting-entry
events. One Host worker shares the configured concurrency limit across these
conversations. Meeting questions still use polling, normally every five seconds.
The existing native Claude harness executes tasks; credentials remain in the
selected CLI profile.

## Configure and start

Enable the app's bot capability, publish the IM permissions and subscribe to
`im.message.receive_v1` using long-connection delivery. The
[permission list](feishu-channel-detail.md#platform-configuration) separates IM
from optional meeting permissions.

Copy [the configuration example](../config/channel-feishu.example.json) to a private
file, mode `0600`, and replace its placeholder identities. `im_chat: true`
requires an explicit `owner_open_id`. Start with `auto_join: false` and no
`meeting_id` for ordinary IM only; this mode makes no meeting API reads.
Prepare the existing native and Jev credentials as described in the
[meeting setup](feishu-meeting-channel-detail.md#configuration-and-runtime).

Progress feedback is off by default. To show received, processing, completed or
failed reactions on the original private/group message, publish
`im:message.reactions:write_only` for the app, then set `im_reactions: true` with
`im_chat: true` and restart the service. Meeting chat does not use IM reactions.
Uncertain feedback stops later markers for that message while its task and
answer continue.

```sh
rtk make build
rtk ./bin/agent-host feishu check --config /absolute/path/feishu.json --home /absolute/path/feishu-state --workspace /absolute/path/project
rtk ./bin/agent-host feishu serve --config /absolute/path/feishu.json --home /absolute/path/feishu-state --workspace /absolute/path/project
rtk ./bin/agent-host feishu status --home /absolute/path/feishu-state
```

`check` verifies local configuration, not platform grants or live delivery.
For a managed runtime, set `"channel": "feishu"` in `ROOT/config/service.json`
and use `ROOT/config/feishu.json`; keep its existing model/provider settings.
`feishu-meeting` commands and the legacy managed configuration remain supported.

Set `auto_join: true` to also accept authorized meeting invitations and private
meeting cards, supported links or numbers. A supplied long `meeting_id` is an
already-joined seed checked through a read before polling starts. With automatic
entry enabled, a recognized private entry request joins the meeting instead of
becoming an ordinary model question.

## Current limits

Ordinary IM accepts text only. Attachments, rich-text `post` questions, topic
groups and voice are outside this increment. Narrow meeting-entry cards and
posts retain their existing meaning. Old meeting chat and IM events predating
startup are not replayed as new tasks. Uncertain sends remain inspectable and
are not retried automatically. The
[details](feishu-channel-detail.md#validation-boundary) keep implementation,
offline checks and live acceptance separate.
