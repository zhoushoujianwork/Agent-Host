# Feishu channel implementation details

The [main guide](feishu-channel.md) defines this increment's scope. Existing
[meeting details](feishu-meeting-channel-detail.md) retain the entry lifecycle,
polling and meeting-delivery contract.

## Configuration and runtime

The [example](../config/feishu.example.json) adds IM fields to the existing
meeting configuration:

| Field | Meaning |
| --- | --- |
| `im_chat` | Opt in to owner private text and same-tenant group @mentions |
| `im_reactions` | Optional progress reactions on newly received ordinary IM tasks; false by default and requires `im_chat: true` |
| `owner_open_id` | Explicit human `ou_` identity allowed to use private model chat |
| `profile`, `app_id`, `bot_open_id`, `tenant_id` | Selected local CLI profile and expected application, bot and actual tenant identities |
| `allowed_sender_open_ids` | Meeting-question and automatic-entry allowlist; it does not limit ordinary same-tenant group mentions |
| `auto_join` | Optional automatic meeting entry; false does not join on invitation or private entry text |
| `meeting_id` | Optional long ID of an already-joined meeting to restore through a membership read |
| `poll_interval_seconds` | Meeting interval, at least 5; retained in the common config even for IM-only operation |

An IM-only configuration sets `im_chat: true`, `auto_join: false` and omits
`meeting_id`. It creates no meeting manager and performs no VC reads or joins.
The common schema still requires the sender allowlist and polling interval;
putting the owner in that allowlist keeps the example ready for optional entry.
When automatic entry is enabled, authorized meeting entry has first refusal on
private cards, supported links and exact number/join commands. Other eligible
owner text proceeds to IM. A `post` is only supported by the narrow entry parser,
not as an ordinary question. Source text never changes the profile or identities.

`agent-host feishu` and `agent-host feishu-meeting` use the same command handler.
The new name supports `check`, `serve` and `status`; existing execution and
Agent options remain available. Managed `channel: "feishu"` selects
`ROOT/config/feishu.json`. Legacy `channel: "feishu-meeting"` still selects
`ROOT/config/feishu-meeting.json`; neither selection migrates or overwrites the
other file. An omitted managed channel retains DingTalk.

One `event +subscribe --as bot` subprocess handles IM and entry events for the
configured app/profile. Do not run another subscriber for that app or bypass its
lock with `--force`: the official CLI warns that competing connections split
delivery. Host keeps one worker and a shared concurrency limit, with each
conversation ordered through execution and delivery. Per-meeting polling and
cancellation remain independent. CLI credentials stay in its own store; Host
does not export application secrets or create an additional SDK connection.
The pinned [official subscription source](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/event/subscribe.go)
defines this raw-event command and its app-level lock.

## Platform configuration

Publish application/bot grants for the enabled paths, then enable
`im.message.receive_v1` with long-connection event delivery:

| Capability | Application permission |
| --- | --- |
| Owner private messages and private entry requests | `im:message.p2p_msg:readonly` |
| Human group @mentions | `im:message.group_at_msg:readonly` |
| Reply as the bot | `im:message:send_as_bot` |
| Optional private/group progress reactions | `im:message.reactions:write_only` |

The [official receive-event reference](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/events/receive)
describes the receive permissions and `im.message.receive_v1`. The
[reply reference](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/reply)
and [pinned CLI implementation](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/im/im_messages_reply.go)
describe bot replies. The bot must be available to the owner; for group use it
must be a member with permission to speak. Receiving all group messages is not
required for this addressed-text scope.

Publish the optional reaction write grant before enabling `im_reactions`. An
already published broader `im:message` grant also authorizes these calls; this
increment does not require it. The adapter retains its own returned reaction
IDs and performs no reaction listing, so `im:message.reactions:read` is not
required. The official
[create](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-reaction/create)
and [delete](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-reaction/delete)
references define the write grant and require access to the message's conversation.

Optional meeting entry additionally uses `vc:meeting.bot.join:write`,
`vc:meeting.meetingevent:read`, `vc:meeting.message:write`, meeting-bot beta access
and the three `vc.bot.*` subscriptions listed in the
[meeting configuration](feishu-meeting-channel-detail.md#entry-events-and-platform-configuration).
The IM-only path does not require meeting participation.

These are configuration requirements, not a report of the current app's grants.
Local `check`, an OAuth login and a default user-scope listing cannot establish
published application/bot grants. One owner private greeting and its visible
accepted reply establish bounded P2P receive/send evidence. Group grants and
group-mention delivery remain pending verification; absence from a user-scope
result is not evidence that a bot permission is missing.

## Admission and conversation boundaries

`internal/channel/feishu` parses full schema `2.0` envelopes from the shared
receiver. It validates header app and tenant, the human sender's Open ID, message
and chat IDs, and timestamps. The configured bot is excluded. Only `text` bodies
are eligible, with a 16 KiB UTF-8 input limit.

Private messages require the exact configured owner. A supplied sender tenant
must match. Ordinary groups require the sender's tenant to match and structured
`mentions` metadata identifying the configured bot. Typed display names or text
that looks like an @mention do not authorize intake. Leading verified bot mention
tokens are removed from the question. Unsupported chat kinds, including
`topic_group`, and non-text bodies do not become tasks. No attachments, quoted
messages or external conversation history are fetched.

The bridge suppresses IM events predating its startup window. A valid receipt is
persisted before the callback returns; the callback never executes a model.
Durable deduplication uses the provider message ID with application/channel
identity, so redelivery under a new event ID cannot create another task.
The CLI SDK acknowledges before the Host's downstream persistence; this is not
an exactly-once or lossless transport across process crashes.

The CLI's `--event-types` registers local SDK handlers; it does not narrow the
application's published upstream subscriptions. The shared connection therefore
also registers the existing app's auxiliary bot membership, message-read,
reaction-created/deleted, document-comment and meeting/minutes notification
events. After checking the full envelope's app, tenant, event ID, timestamp and
object payload, these notifications are acknowledged without creating a task or
calling the IM handler. They grant no routing or execution authority. SDK errors,
invalid envelopes and durable intake failures still stop the service. Publishing
an additional event outside the registered set requires a corresponding handler;
the service never treats an unhandled event as successful intake.
This legacy raw subscription uses immediate raw transforms, including the
[reaction processor](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/event/processor_im_message_reaction.go)
and [generic processor](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/event/processor_generic.go),
without notification-driven API enrichment or additional read grants.

Owner private messages select `OwnerAudienceID`. Groups and meetings select
separate public audiences that include their transport and conversation
identity. A group and meeting never share history merely because their visible
names match. The default Agent or explicit `group_bindings` selects the role;
public-output policy still applies when the owner asks in a shared conversation.
History tools receive only the current authorized Agent/audience/conversation
partition. Host audience boundaries do not provide filesystem isolation.
Private owner execution and native permission settings follow the existing
configured channel policy.

## Reply and recovery contract

The shared receipt stores `state.ChannelReplyTarget` with kind `feishu`, chat
`ID`, original `MessageID`, and a stable 48-character source-derived `UUID`.
The Host creates and persists that key; incoming data cannot choose it. Runtime
profile or Agent changes do not change the same source's outgoing key. The
meeting kind remains `feishu-meeting` with its fixed long meeting target.

The IM adapter makes one call to `im +messages-reply --as bot`, using the saved
original `--message-id` and `--idempotency-key`. Its acknowledgement must identify
bot execution, a new message and the expected chat. It cannot redirect a reply
using question or model text. The
[official command](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/im/im_messages_reply.go)
maps the idempotency key to API `uuid`, with a 50-character limit.

Outgoing IM text is at most 4,000 UTF-8 bytes including `[AI]`, its line break and any model/time
footer. Longer native answers retain the full saved run and send an explicit
excerpt. The formatter does not apply DingTalk @mention or quote syntax to IM.
This increment sends no attachments or thread-specific reply modes. Progress
reactions are a separate opt-in delivery path described below.

IM replies use `--msg-type post --content` with a separate label row followed
by one Markdown row:
`{"zh_cn":{"content":[[{"tag":"text","text":"[AI]"}],[{"tag":"md","text":"..."}]]}}`.
Keeping the label separate preserves a leading heading, list or fenced code
block in the native answer. The
[official CLI helper](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/im/helpers.go#L902)
uses this Markdown node shape. JSON encoding keeps model output inside that text
value; it cannot create extra post nodes. This uses Feishu's supported Markdown
subset, not a promise of full CommonMark rendering. Raw post content avoids the
CLI `--markdown` shortcut's automatic image download/upload behavior. This
adapter creates no media nodes and does not download or upload image references.
Original-message routing, UUID and acceptance checks remain unchanged. There is
no plain-text fallback or second send after an ambiguous rich-text attempt.

Delivery remains separate from execution: `accepted` requires a matching
acknowledgement; explicit local/authorization rejection is `failed`; an
unconfirmed attempt is `unknown`. Sending is persisted before the subprocess
starts. Interrupted claims and ambiguous sends are retained without automatic
execution or reply replay. The idempotency key does not itself guarantee an
exactly-once platform result.

## Progress reaction delivery

`im_reactions` applies only to ordinary private/group IM receipts with an
original `om_` message ID and an `oc_` chat target. Meeting receipts use a long
meeting target and a local composite event ID; there is no verified IM-reaction
target in that transport. Their reactions remain disabled, including in a
combined service. Host controls such as `/status` do not receive task markers.

The adapter maps the existing public task stages to exact, case-sensitive
Feishu emoji names:

| Stage | Host label | `emoji_type` |
| --- | --- | --- |
| Received | `已收到` | `Get` |
| Processing | `处理中` | `Typing` |
| Completed | `已完成` | `DONE` |
| Failed | `打叉` | `ERROR` |

These are this adapter's task meanings, not built-in platform task statuses. The
[official emoji list](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-reaction/emojis-introduce)
and [pinned CLI reaction reference](https://github.com/larksuite/cli/blob/v1.0.96/skills/lark-im/references/lark-im-reactions.md)
document the supported names and raw API arguments. Creation uses
`im reactions create --as bot --params '{"message_id":"EXAMPLE_ID"}' --data
'{"reaction_type":{"emoji_type":"Typing"}}'`; removal uses
`im reactions delete --as bot --params
'{"message_id":"EXAMPLE_ID","reaction_id":"SAVED_ID"}'`. Both use the selected
`--profile` and `--format json`, with no additional event connection or credential store.

Creation is accepted only after one stdout `ok: true` acknowledgement identifies
bot execution, a bounded opaque reaction ID, the requested emoji and operator
`operator_type: "app"` with `operator_id` equal to the configured `app_id`.
That operator ID is the application ID, not the bot's `ou_` Open ID. Schema 14
adds `ProviderReactionID`; accepted status and the exact provider ID are saved
atomically. Removal uses only this saved ID and verifies the returned ID and
own-app operator. It does not infer IDs from a label, source message or UUID,
and does not look up or remove another participant's reaction.

The durable outbox updates progress independently of native execution and final
reply delivery. Replacement first removes the previously acknowledged reaction.
Each mutation is attempted once. Known validation or authorization rejection is
`failed`; missing, conflicting or ambiguous evidence is `unknown`. An unknown
creation, or an unresolved removal, stops later markers for that original
message. Task execution and its answer continue. Interrupted `sending` states
become `unknown` after restart and are never replayed; no list-based reconciliation
or automatic retry is performed.

The reaction policy is retained with each receipt. Changing `im_reactions` does
not rewrite the old receipt on redelivery, execute its task again or backfill
historical markers. The schema upgrade likewise creates no progress work for
old messages.

## Validation boundary

The merged IM and rich-reply increment passed `make check`: formatting,
`go vet`, all Go tests and 64 Python tests. Group and multi-turn ordinary-IM
acceptance remain pending. Automated coverage includes
owner/group admission, wrong app/tenant/bot rejection,
structured mentions, source-message deduplication, public/private history
separation, fixed reply targets, the 48-character key, byte limits, ambiguous
send recovery, and one shared receiver/worker. The IM-only assembled runtime test
receives a private `/status` and records one accepted reply through local CLI
fixtures, with one subscription, no meeting API invocation or entry ledger.
Combined-agent tests retain source-specific audience policy and isolated native
profiles; canceling an old meeting cannot send its reply or cancel ordinary IM
and a replacement meeting. Offline checks do not establish
published app grants, native authentication or platform delivery.

Race checks passed for the five affected Go packages (1,122 test cases), with
the added composite-lifetime cancellation regression also passing its focused
race check. The binary build passed.

On 2026-10-12 at 19:02 (UTC+08), source `8e21d5d` received the owner's ordinary
private greeting, completed a native turn and recorded its reply as `accepted`.
The user's screenshot independently showed that reply in the chat. It also
showed Markdown emphasis and separator syntax literally, establishing the
outgoing plain-text formatting defect. The rich-reply regression preserves
Markdown, Chinese code blocks, literal JSON/shell syntax and the model/time
footer in a single `md` node after the label row, while retaining the exact 4,000-byte visible-text
limit. The user subsequently confirmed that rich-text rendering had recovered;
this establishes that observed case, not general Markdown conformance.
The formatting patch passed `make check`, 49 focused race cases and an official
CLI dry run confirming one bot reply request with `msg_type: post`, one `md`
node and the expected idempotency key. The dry run performs no message mutation
and does not prove client rendering.

The progress adapter passed 68 focused race cases covering exact stage emoji,
own-app acknowledgement, saved-ID removal, original IM targets, sanitized errors,
ambiguity and cancellation without retries. The complete progress increment
passed `make check` (all Go tests, formatting, vet and 64 Python tests). Race
checks also passed for state (431 cases), channel (269 cases), CLI
integration/context (19 cases) and workbench startup (6 cases). Schema 14
read-only viewer compatibility is covered, including preservation of private
provider IDs. The bounded owner-private progress check below passed; group and
failed-marker platform acceptance remain pending. These
fixture-based checks do not establish a published reaction grant or successful
platform mutation.

On 2026-10-13 at 10:46 (UTC+08), source `93a3127` received a fresh owner-private
question. The `Get` creation was accepted and its own-app provider ID was saved.
The shared event subscriber then reported a transport/event parsing failure;
its cancellation interrupted the old-marker removal and native execution. The
run ended `canceled` with no native answer, and the failure explanation's reply
was accepted. A subsequent read-only platform listing showed the original `Get`
still present. Neither the uncertain removal nor the original task was replayed.
The original SDK stderr was not retained, so its exact error type cannot be
recovered. The published app configuration independently showed reaction-created,
reaction-deleted and other auxiliary subscriptions missing from the local SDK
handler set. This mismatch is corrected by registering and acknowledging those
authenticated notifications; it does not change reaction acknowledgement rules
or weaken real transport failures.
The listener fix passed `make check` and 216 focused race test/subtest results. Its stream
regression sends every auxiliary event with request-like content, then verifies
one ordinary IM dispatch and continued readiness. Wrong identity, invalid schema
and timestamps, non-object payloads and SDK failures retain rejection behavior.

After the fix was merged and deployed from `40dd9be`, two fresh owner-private
tasks completed with accepted received, processing and completed reactions and
accepted replies. The explicitly requested three-sentence capability test was
received at 11:05:50 (UTC+08). Its observed sequence was:

| Local time | Observed result |
| --- | --- |
| 11:05:51 | `Get` creation accepted |
| 11:05:54 | Saved `Get` removal and `Typing` creation accepted |
| 11:05:58 | Native execution completed |
| 11:05:59 | Final reply accepted |
| 11:06:02 | Saved `Typing` removal and `DONE` creation accepted |

A read-only platform listing confirmed that only the own-app `DONE` remained on
the test message. The service stayed running after feedback events. The old
canceled task and uncertain removal were retained; restart skipped its later
marker rather than replaying either operation. This establishes bounded ordinary
owner-private progress and reply delivery, not group, failed-marker or general
multi-turn semantic acceptance.

The [meeting evidence](feishu-meeting-channel-detail.md#validation-boundary)
records the independent two-turn test and the later automatic re-entry and
accepted reply on source `4f8e024`. Those results establish neither ordinary
private-chat nor group-mention acceptance. This increment changes no production
installation and adds no voice transport.
