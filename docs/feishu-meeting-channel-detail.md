# Feishu meeting channel implementation details

The [main guide](feishu-meeting-channel.md) governs user-facing scope. A named
local CLI profile supplies optional automatic entry and meeting text polling.
The [combined Feishu increment](feishu-channel-detail.md) shares this entry
connection with opt-in owner private and group @mention text; the descriptions
below of ignored ordinary IM apply when `im_chat` is disabled.

## Ownership

| Component | Responsibility |
| --- | --- |
| `internal/channel/feishumeeting` | Named-profile event/REST calls, entry admission and ledger, per-meeting lifecycle and cursors, fixed-destination text delivery |
| `internal/channel` | Provider-neutral receipt, ordered execution, native session binding, routing and reply outcomes |
| `internal/cli` | Local configuration checks, runtime composition, home lock, receiver/worker supervision and status |
| `internal/state` | Source-linked receipts, deduplication, queue/recovery and provider-specific feedback policy |
| Native Claude harness | Existing model/tool execution loop and native conversation |

## Configuration and runtime

The [fixed-meeting example](../config/feishu-meeting.example.json) and
[automatic-entry example](../config/feishu-meeting-auto.example.json) share these fields:

| Field | Meaning |
| --- | --- |
| `profile` | Explicit authenticated local `lark-cli` profile |
| `app_id` | Application identity beginning with `cli_` |
| `bot_open_id` | Bot Open ID beginning with `ou_` |
| `meeting_id` | Required long meeting ID in manual mode; optional already-joined seed in automatic mode |
| `tenant_id` | Operator-provided actual tenant identifier for local conversation isolation |
| `allowed_sender_open_ids` | Permitted human `ou_` Open IDs for meeting questions, invitations and private entry requests |
| `poll_interval_seconds` | Integer of at least 5 seconds |
| `auto_join` | Opt-in boolean; omitted/false retains fixed-meeting manual entry |

The local checker validates JSON, identifiers, bounds, executable availability
and existing native/Agent/knowledge settings. It does not inspect the CLI
credential store, authenticate the selected profile or prove the app's published
permissions. Managed checks load the configured private environment file.
Configuration must be a bounded, non-symlink regular file owned by the current
user with no group/other access. Duplicate keys, unknown fields and trailing
JSON are rejected before transport startup.
The configured tenant ID selects the local namespace. Polling output supplies
no independent tenant field; raw entry events are checked against their app and
tenant envelope before they can select a meeting.

`feishu-meeting check` and `serve` take `--config` plus common execution options
such as `--home`, `--workspace` or `--project`, `--model`, `--claude`,
`--agents-config`, `--native-profile`, `--jev-provider`, `--jev-endpoint`,
`--jev-model` and `--timeout`. `--no-tools` supports a controlled text acceptance
test. Native and Jev authentication follows existing host configuration.
`lark-cli` is resolved from `PATH`; credentials remain under that CLI's control.

Manual mode requires an explicit prior entry:

```sh
rtk lark-cli --profile YOUR_PROFILE vc +meeting-join --as bot --meeting-number YOUR_NINE_DIGIT_MEETING_NUMBER --format json
```

Use the returned long `data.meeting.id` in the fixed configuration. Automatic
mode may omit `meeting_id`. A supplied seed is verified through the membership
read API without another join. The automatic-entry opt-in does not change the
default behavior of existing fixed-meeting configurations.

`serve` owns the state-home writer lock. Read-only `feishu-meeting status` and
run inspection use the same `--home`; a run also requires the `agent_id` and
`audience_id` from its receipt because meeting runs belong to that partition:

```sh
rtk ./bin/agent-host run show --home /absolute/path/feishu-host-state --agent AGENT_ID --audience AUDIENCE_ID RUN_ID
```

Stop the receiver before memory review/retry or another writer accesses that
home. Runtime state and native configuration can be isolated from the installed
DingTalk service with explicit `--home` and `--native-profile` paths.

Agent selection reuses the configured default and `group_bindings`, with the
long meeting ID as `conversation_id`. All permitted senders in that meeting
share the selected Agent's conversation. Public-output Host instructions and
the existing `bypassPermissions` native mode apply. Audience separation scopes
Host memory and native sessions; it is not filesystem isolation. `--no-tools`
keeps the acceptance test tool-free.

## Entry events and platform configuration

Platform prerequisites use application permissions `vc:meeting.bot.join:write`,
`vc:meeting.meetingevent:read` and `vc:meeting.message:write`, meeting-bot beta
admission, and a meeting that allows agents. Private entry requests also require
application permission `im:message.p2p_msg:readonly` and
`im.message.receive_v1`. Publish the meeting invitation/end subscriptions and
select long-connection event delivery in the console. Local `check` cannot prove
server-side grants or actual event delivery.

The existing CLI profile starts one raw event subprocess:

```sh
rtk lark-cli --profile YOUR_PROFILE event +subscribe --as bot --event-types vc.bot.meeting_invited_v1,vc.bot.meeting_ended_v1,vc.bot.meeting_activity_v1,im.message.receive_v1
```

This is the official CLI's hidden legacy compatibility command, verified in
`1.0.96`; it is distinct from `event consume`, whose current EventKey catalog
does not expose the `vc.bot.*` events. Keep a compatible CLI version and verify
the command before enabling entry. No Node sidecar or application-secret export
is used. The [official shortcut](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/event/subscribe.go)
registers explicit raw types and defaults to one NDJSON V2 envelope per line:

```text
{schema, header: {event_type, event_id, create_time, app_id, tenant_key}, event}
```

Do not add `--compact`, `--json` or `--force`. Compact output changes field paths;
pretty JSON changes line framing; extra connections can split delivery.
`vc.bot.meeting_activity_v1` is registered so the SDK can acknowledge it, then
ignored by entry processing. It does not drive model tasks or replace polling.
Subscriptions exist in the console; this command does not add them.

Readiness follows the SDK's actual `connected to ` status or a valid received
event. The shortcut's early `Connected... Waiting for events` line appears
before the handshake completes and is not a readiness signal. Disconnect and
reconnect observations are surfaced safely. Raw SDK URLs, source messages and
credentials are not copied into public logs. Event frame/parse or durable
handling failures stop the listener explicitly; the SDK acknowledges upstream
before Host persistence, so delivery across process crashes is not exactly-once.

CLI execution currently requires Unix process-group supervision. Each wrapper
and its descendants share a dedicated process group, which the host terminates
on cancellation or exit so a surviving native subscriber cannot retain the
same-app lock. Other platforms fail explicitly rather than supervising only the
wrapper.

### Meeting invitation and end

The [official meeting registry](https://github.com/larksuite/channel-sdk-python/blob/main/lark_channel/channel/meeting/registry.py)
reads `vc.bot.meeting_invited_v1` from `event.meeting` (`meeting_no`, long `id`,
`topic`), `event.inviter`, `event.bot`, `event.call_id` and `event.invite_time`.
The receiver checks app, tenant, bot and allowed inviter before durable entry.
Actor Open IDs can be a scalar `id`, `id.open_id` or direct `open_id`; other
identity namespaces do not grant admission. Large numeric meeting IDs retain
their decimal representation instead of passing through `float64`.

The join worker sends the nine-digit number and forwards an available
`call_id` through [the official join command](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/vc/vc_meeting_join.go).
An acknowledged `data.meeting.id`/`meeting_no` must match the selected meeting.
As in the [official SDK](https://github.com/larksuite/channel-sdk-python/blob/main/lark_channel/channel/meeting/registry.py#L273),
`join_user` fields are not required or used as bot-presence proof. Membership is then read with
`vc meeting get --as bot --user-id-type open_id --with-participants`; the matching
bot participant must have `status: 2` before its polling runtime starts.

`vc.bot.meeting_ended_v1` carries `event.meeting.id`. Ending that meeting marks
its lifecycle terminal and stops its local runtime. Removal confirmed by the
membership probe and polling failure also stop only the affected meeting.
Ending another meeting or automatically leaving all meetings is not a cleanup
action in this increment.

### Private entry requests

Only raw `im.message.receive_v1` from `event.sender.sender_type: user`, an
allowlisted `sender_id.open_id`, and `event.message.chat_type: p2p` is eligible.
`event.message.message_id` is validated; durable entry deduplication uses
`header.event_id`. `content` is a JSON string, not the rendered text supplied by
the newer `event consume` command. Accepted message bodies are narrowly decoded:

| Body | Accepted entry |
| --- | --- |
| `text` | Entire nine-digit number, three groups of three digits separated by whitespace (including copied nonbreaking spaces), explicit join command, or supported meeting link |
| `post` | Direct title/content body using only text nodes and supported link nodes; the whole result must match the same entry syntax |
| `video_chat` | Verified `meet_number` string of exactly nine digits, with an explicitly empty/null `end_time` |

Explicit prefixes are `/join`, `join`, `join meeting`, `入会`, `加入会议`,
`参加会议`, `会议号` and `会议 ID`, followed by space/tab or a colon. English
prefixes are case-insensitive. Arbitrary prose containing nine digits is not an
entry command.

The supported link is exactly `https://vc.feishu.cn/j/<9digits>`, without a
port, userinfo, query, fragment or escaped path. Opaque link tokens are rejected
rather than guessed or fetched. This is an implementation boundary; the general
platform meeting-link field does not guarantee a numeric URL.

A real private-card receipt verified this limited `video_chat` shape:

```text
content = JSON string {meet_number, topic, start_time, end_time}
```

Unknown card fields fail closed. `topic` and `start_time` are descriptive and do
not supply a destination. An absent, zero or nonempty `end_time` is not treated
as an active invitation. Unverified `meeting_card`/interactive shapes are not
assumed equivalent. Normal private messages are ignored rather than dispatched
to the model. Source titles, links and card text cannot change runtime settings.

### Durable entry and restart

The private entry ledger separates receipt, join attempt, membership proof and
active runtime. It deduplicates sources and merges repeated requests for the
same meeting. At most four held active/uncertain entries are permitted. The
CLI's short in-memory deduplication is not a substitute for this durable state.
Fresh invitations check the current membership even for a previously active
meeting. Confirmed absence retires the prior runtime and permits one new join;
confirmed presence keeps its receiver. A verified long ID takes precedence over
historical unknown requests with only the same number, including after polling
failure. Source-event deduplication does not permanently deduplicate a room.

After restart, pending unattempted invitations are skipped rather than becoming
fresh implicit joins. An interrupted `joining` attempt becomes `unknown` and is
not reissued. Existing active or already-joined seed entries are rechecked
against membership before polling resumes. A failed or uncertain result remains
inspectable; changes to the source or a later SDK reconnect do not silently
replay the attempt. Completed and ended entry records remain evidence.
An explicitly configured seed is read-only checked on each restart, including
after a prior absence; confirmed presence can resume polling without a join.

The service holds one state-home writer lock while active meeting runtimes have
their own fixed destination, cursor, context, audience and cancellation scope.
Shutdown cancels local work without ending the user's meeting or automatically
ejecting the bot. Entry authorization does not authorize arbitrary ordinary IM
tasks or widen model/tool permissions.

## Managed runtime

The existing `service check`/`service run` commands can use Feishu without
embedding tokens in command arguments. Prepare a separate runtime root:

```text
ROOT/
  config/service.json
  config/environment.json
  config/feishu-meeting.json
  state/
  workspace/
  native/
```

Directories belong to the current user, deny group/other access, and use no
symlinks. Configuration files are regular private files, normally mode `0600`.
Use the existing [service example](../config/service.example.json), adding
`"channel": "feishu-meeting"`. Its explicit native executable, model, Jev
provider/model, timeout and tool settings remain in effect. The
[environment example](../config/environment.example.json) shows supported
provider/native keys; put actual credentials only in the private runtime.
Optional `agents_config` and `knowledge_config` keep their existing meaning.
`discovery_config` is not supported for this channel.

```sh
rtk ./bin/agent-host service check --root /absolute/path/feishu-runtime
rtk ./bin/agent-host service run --root /absolute/path/feishu-runtime
rtk ./bin/agent-host feishu-meeting status --home /absolute/path/feishu-runtime/state
```

The managed runtime maps state to `ROOT/state`, default execution to
`ROOT/workspace` and native Agent/audience profiles to `ROOT/native`. It loads
the selected channel from `service.json`; omission retains DingTalk. Local
`service check` makes no API/model calls. `service run` remains a foreground
process. The production installer is not extended for Feishu in this increment.

## Input and identity

Each chat item is admitted separately using its own `operator`, message type and
content. An enclosing event can contain multiple senders; its `actors` list is
not sender authorization. Only text items from an allowlisted human with an
explicit `AI:` or `AI：` prefix become tasks. Bot identity and bot-shaped items
are excluded. Saved task text is the addressed question after prefix removal;
receipt identities retain the event/item and sender provenance.

The bot identity is its `ou_…` Open ID. The numeric `join_user.id` returned by
bot entry is a different identifier and must not be used for echo exclusion.
Display names, text mentioning a bot and source instructions confer no authority.
The CLI command output, not task text, supplies provider identities.

Receipts use the meeting, event ID and chat-item index so batches and redelivery
cannot conflate distinct inputs. Channel, application, tenant and meeting
identity partition the shared conversation. Meeting output is visible to all
participants; it uses a meeting audience rather than owner-private recall.
Existing on-demand conversation-history tools can retrieve prior admitted
questions and saved replies in the same authorized partition. Ordinary chat,
captions and raw meeting transcripts do not enter that history automatically.

## Polling and delivery

Each meeting runtime uses `lark-cli vc +meeting-events --as bot`, with the
selected profile and its fixed long meeting ID. Polling is at least five seconds. The
initial complete snapshot establishes history without dispatching tasks;
subsequent admitted items are committed before the cursor advances. A new
startup establishes a fresh baseline rather than turning downtime chat into
implicit tasks. This choice does not replay interrupted native work.
The bot Open ID is required on every result. Empty polls may omit CLI-derived
meeting metadata; any supplied page or event meeting ID must match the fixed
destination before intake. Polling, parse, identity or persistence failures are
explicit. In fixed-meeting mode they stop the foreground receiver; automatic
mode retires the affected meeting without canceling independent meetings.
There is no polling retry loop or automatic credential fallback.
Each REST CLI subprocess has a 30-second deadline, with stdout capped at 4 MiB and
stderr at 64 KiB. Large initial snapshots fail explicitly at that response bound.

Progress is persisted privately per configured profile, app, bot, tenant and
meeting. Queue receipts and reply outcomes remain in SQLite. The progress file
is advanced after durable intake, so a persistence failure cannot falsely
acknowledge a question. Retained seen-item identities are bounded to 100,000;
reaching the limit fails explicitly instead of discarding deduplication state.

Replies use `vc +meeting-message-send --as bot --msg-type text` and a durable
UUID. The destination is checked against the configured meeting before the
subprocess starts. Output includes an `[AI]` prefix. The reply adapter never
derives a meeting, application identity, profile or recipient from inbound text.
The final meeting text is bounded to 4,000 UTF-8 bytes; longer replies carry an
explicit excerpt notice. The shared Host formatter reserves space for its public
actual-model and measured-execution-time footer before truncation. Missing actual
models or execution measurements are omitted, following the
[DingTalk formatting contract](dingtalk-channel-detail.md#reply-capability-and-delivery).
The complete native answer remains in the saved run.

The official [event command](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/vc/vc_meeting_events.go)
supports bot queries and pagination. The official [message command](https://github.com/larksuite/cli/blob/v1.0.96/shortcuts/vc/vc_meeting_message_send.go)
supports text delivery and UUID-based deduplication. A UUID does not establish
an exactly-once platform guarantee; a rejected or ambiguous mutation remains a
separate failure or unknown outcome.

The durable reply target is `state.ChannelReplyTarget` with kind
`feishu-meeting`, the fixed meeting ID and a stable source-derived UUID. It
does not pretend to be a DingTalk webhook with an expiry. The receipt explicitly
disables reactions; no fake success markers or reaction outbox entries are
created. These fields use the existing receipt payload; no new SQL schema
migration is needed. Existing receipt, claimed-task and attempted-send recovery rules apply:
saved unclaimed work stays queued; interrupted claims and uncertain sends are
inspectable without automatic execution or message replay.

The adapter does not log tokens or export application secrets. Automatic joins
require the explicit opt-in and an authorized source; manual mode requires an
operator join. Neither mode logs in, changes permissions, changes identities or
automatically leaves meetings on shutdown.

## Validation boundary

The earlier fixed-meeting text increment passed validation on 2026-10-12:

| Check | Result and boundary |
| --- | --- |
| `rtk make check` | All Go formatting, vet and tests; 56 Python tests covering installer (20), run viewer (16), memory viewer (13) and DWS discovery (7) |
| Binary build | Completed successfully |
| Private managed `service check` | Validated the independent runtime without making API/model calls |
| Live owner conversation | Two fresh addressed questions, two completed native Claude runs, one Host session, two platform-accepted replies |
| Independent Feishu event observation | Both bot reply echoes appeared in the selected meeting |
| Follow-up context | Correctly recalled the synthetic owner and original deadline, including relative-date interpretation |

The live acceptance used tools disabled and a separate managed runtime.
Both runs retained `memory_status: unchanged`; this validates conversation
follow-up and delivery in that bounded test. It does not establish newly saved
long-term memory. Raw receipts,
messages, IDs and provider configuration remain in the private runtime; the
repository records only these aggregate results. The installed DingTalk
runtime was not changed during this acceptance.

Offline coverage includes batch admission, baseline/restart suppression, empty
polls, supplied wrong-meeting rejection, per-item sender and bot filtering,
event/item deduplication, exact CLI target/UUID, cancellation, ambiguous
delivery, shared public audience separation and source-linked meeting history.
Existing DingTalk regressions passed the assembled checks. A local
configuration check establishes configuration validity, not Feishu
authentication, model authentication, app permissions, bot presence or audio
access; the later live observations establish only the tested text path.

The automatic-entry increment passed the following repository checks:

| Check | Result and boundary |
| --- | --- |
| `rtk env GOFLAGS=-p=1 make check` | All Go formatting, vet and tests, plus the same 56 Python checks |
| `rtk go test -race -p 1 ./internal/channel/feishumeeting` | 152 meeting-channel tests passed with the race detector, including same-room re-entry after exit, polling failure and restart absence |
| Focused bridge race cases | 17 cases passed; this is not a full CLI race run |
| Binary build and `git diff --check` | Passed |

Live startup confirmed the SDK connection and rejected an already-departed seed
with `bot_absent_on_restore`, without issuing another join. A real shutdown and
restart with the installed npm CLI confirmed the old service, wrapper and
native subscriber all exited; the replacement reached a fresh SDK connection
and `channel.connected` without `--force` or manual orphan cleanup. A real
private `video_chat` event established the card fields documented above. These
observations alone did not establish a successful automatic join or a subsequent
model conversation; the later bounded acceptance below supplies that evidence.
An authorized private entry request later retained an unknown acknowledgement,
while an independent read-only roster check found the bot present. The original
join response was not retained, so its exact rejection remains unproven; that
uncertain attempt was not replayed. Join validation now follows the SDK's
meeting-ID contract and still requires independent configured-bot presence.

On source `4f8e024`, a later live private-entry request rejoined the selected
meeting and completed a fresh owner turn on 2026-10-12. Times below are UTC+08:

| Time | Observation |
| --- | --- |
| 18:08:35 | Authorized private entry request received |
| 18:08:37 | Join attempt started |
| 18:08:41 | Meeting polling connected and entry became active after presence verification |
| 18:10:33 | Owner sent a fresh `AI:` meeting question |
| 18:10:36 | The question was durably received and processing started |
| 18:10:43–18:10:44 | Native execution completed, then Feishu accepted the reply |

Independent read-only inspection matched the configured meeting and bot,
verified the owner's eligible text item and durable progress, and found one
completed receipt with an accepted reply and no error. The preceding owner
meeting message was before startup and correctly remained part of the baseline.
The inspection read metadata and aggregate counts without publishing participant
content. Platform acceptance does not itself prove the recipient read the reply.
This is meeting-chat evidence, not ordinary-IM chat acceptance.

Remaining automatic-entry platform cases include an authorized in-meeting
invitation, each supported private entry form, duplicate admission,
seed/restart presence recovery and scoped meeting-end/removal cleanup. The
observed private re-entry and fresh meeting reply do not establish every one of
those lifecycle paths. Larger traffic, multiple-sender use and new long-term
memory remain separately unmeasured. Raw platform receipts, source IDs and
provider credentials stay in the private runtime; only aggregate results belong
in this guide. Meeting-text push transport and audio remain outside this
increment.
