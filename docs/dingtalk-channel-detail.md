# DingTalk channel implementation details

The [main channel guide](dingtalk-channel.md) governs scope and rollout. This
channel supports owner private messages and same-enterprise group mentions.
The [instance extension](agent-instances-detail.md) defines group routing,
audience partitions and public-output policy.
Offline tests exercise the adapter and host path; actual DingTalk reception,
delivery, and native permission interaction through this channel remain
unverified by those offline tests. The later
[production service extension](service-install-detail.md) adds an isolated
managed runtime and records actual deployment evidence separately.

## Ownership and configuration

| Package | Responsibility |
| --- | --- |
| `internal/channel/dingtalk` | Callback parsing, enterprise/owner/mention admission, Stream lifecycle, safe protocol errors, session-webhook delivery |
| `internal/channel` | Durable receipt, fixed-profile queue, session binding, host execution, final reply |
| `internal/cli` | Local configuration and secret loading, host composition, exclusive writer lock, receiver/worker supervision |
| `internal/state` | Receipts, delivery, reactions, project sets, numbered choices, deduplication, source linkage and recovery |
| `internal/host` | Existing recall, native execution, memory finalization, experience handling and event journal |

The [conversation/input details](channel-conversations-detail.md) define the
bounded scheduler, attachment download and persistence, quoted material and
native image input. These additions retain the existing admission and delivery
boundaries below.

`config/channel-dingtalk.example.json` contains only `client_id`, `robot_code`, `client_secret_file`,
`corp_id`, and `owner_user_id`. The secret path must be absolute and point to an
owner-private regular file. Unknown JSON fields and invalid configuration fail
before Stream startup. `dingtalk check` also validates the local workspace,
executable availability and selected Jev credential configuration. It does not
authenticate with DingTalk, Claude, or Jev.

The receiver uses official SDK `github.com/open-dingtalk/dingtalk-stream-sdk-go`
at `v0.9.2-beta.1`, with `github.com/gorilla/websocket` pinned to `v1.5.3`.
The SDK's logger is global, so the adapter permits one receiver per process and
installs one safe logger before SDK goroutines start. It never renders raw SDK
arguments: normal SDK INFO can contain a connection ticket, and DEBUG can
contain application credentials. See the [pinned client](https://github.com/open-dingtalk/dingtalk-stream-sdk-go/blob/v0.9.2-beta.1/client/client.go)
and [logger](https://github.com/open-dingtalk/dingtalk-stream-sdk-go/blob/v0.9.2-beta.1/logger/std_logger.go).

## Inbound identity and acknowledgement

The low-level subscription is `CALLBACK /v1.0/im/bot/messages/get`. Stream
reception is an application-bot capability, separate from custom group-robot
webhooks. DingTalk documents direct reception in private conversations and
`@bot` reception in groups. See the [official receive guide](https://open-dingtalk.github.io/developerpedia/docs/learn/bot/appbot/receive/).

Admission requires both `chatbotCorpId` and `senderCorpId` to equal the configured
enterprise. Private conversations (`conversationType == "1"`) require the owner;
groups (`"2"`) require verified `isInAtList` and matching bot identity when
mention details are present. Text containing a bot name is not a mention.
The sender must exist and must not equal the bot's
user identity. Display names and `isAdmin` confer no authority. The callback's
business `msgId` remains separate from the Stream header `messageId` used in
acknowledgements.

The parser bounds the frame to 1 MiB, text to 16 KiB, and individual identities
and reply URLs. Plain text is preserved exactly. Supported `richText` callbacks
contain ordered text, picture and file parts; text is concatenated without extra
separators and media retains a placeholder plus source-linked metadata. The
original message type stays in the receipt. Standalone `picture` and `file`
callbacks are also accepted. At most four attachments are accepted per message,
with 5 MiB per image and 20 MiB per file. Unsupported links, unknown attributes,
malformed fragments or duplicate keys invalidate the whole rich message; no
text prefix is dispatched on its own. Callback-provided quoted text or summaries
are stored separately, with missing/truncated content marked; they never cause
automatic DWS history retrieval or quoted-media downloads.
Forwarded `markdown` bodies are accepted as reference material, separately from
the current sender's statement. Picture callbacks may contain two distinct
download codes; the parser prefers `downloadCode` and uses `pictureDownloadCode`
when it is absent. The [input details](channel-conversations-detail.md#accepted-input-and-provenance)
describe the callback shapes and reference limits.
An authorized unsupported message
type retains its route so the host can persist a rejection and send a written
explanation without invoking Claude. Malformed, unauthorized, expired-capability,
or oversized inputs are explicitly rejected in a safe local observation.

For an admitted message, a success ACK follows the committed receipt and its
successful local observation. A rejected callback is acknowledged only after
the rejection observation succeeds. Persistence, observation, or envelope
failure returns a failed ACK and stops the receiver. The SDK allows at most 16
concurrent handlers; ingestion has a five-second context deadline. Saturation
and ingestion failures terminate the receiver visibly. Known transport failures
reconnect without canceling running tasks. On shutdown the host cancels the
connection and waits for active callbacks before closing state.

These ACKs mean receipt or explicit rejection, never completed execution or IM
delivery. The [official protocol FAQ](https://open-dingtalk.github.io/developerpedia/docs/learn/stream/protocol/)
describes robot callbacks as fire-forget, whereas newer SDK comments mention
retrying unacknowledged frames. No callback redelivery guarantee is assumed.
Local deduplication remains necessary and cannot recover a callback the host
never received.

## Queue, session, source and recovery

Schema v4 added `channel_messages` and `channel_sessions`; schema v5 adds the
durable `channel_reactions` outbox. Schema v6 expands its stages and adds source
run/event references, preserving reaction IDs, outcomes, removal audit and the
autoincrement sequence. Schema v7 adds the project registry and conversation
selection without rewriting existing message or evidence payloads. Schema v8
adds repeatable phase occurrences while preserving reaction IDs, source event
references, uncertain delivery/removal outcomes and the autoincrement sequence.
The subsequent routing migration adds project descriptions/aliases, manual/auto
selection, pending numbered choices and project-set snapshots. Existing payloads
remain readable without rewriting their original evidence.
Schema v10 adds agent/audience partitions; v11 adds the protected
`input_capabilities` column for attachment download codes. These credentials are
excluded from public payloads and model context. Existing queued payloads,
memory sources and reaction outcomes are preserved.
Historical manual selections and pending choices are retained. Current dispatch
does not consult manual pins or create new project questions; each new task
selects a reliable project association or executes in the default environment.
Writable opens migrate
earlier schemas transactionally; read-only commands require migration first.
This retains schema v3's existing memory annotations and indexes.

Receipt identity hashes the channel, application ID and business message ID.
A database uniqueness constraint provides durable deduplication. Repeated input
cannot replace its original route, text, or execution profile; conflicting
identity/content fails. The receipt retains provider creation time separately
from host receipt time.

Earlier versions discarded richText bodies and explicitly rejected them as
unsupported. Redelivery of those exact failed, unexecuted receipts is a duplicate
even after the parser can extract the text. This narrow compatibility exception
keeps the old payload, route, profile, capability and result unchanged. It never
replies or executes again. Other identity/content collisions still fail.

The profile includes application, enterprise, owner, canonical workspace, model,
native executable, tool settings and Jev provider/model. Receipt conversation
identity uses the application, enterprise, private conversation and sender,
independently of that profile. Execution session binding adds the selected
project set, canonical directories, primary cwd and shared native runtime fingerprint. Startup
selects one queue profile, and the worker
claims only its queued input. Changing these settings does not reinterpret
previously queued work under a new profile. `/new` replaces only the current
set's compatible session binding and clears follow-up routing context;
it neither clears durable project memory nor repeats an earlier task.

A bounded worker pool serializes each authenticated conversation binding and
each shared agent/audience partition, while different audiences can progress
concurrently. Default concurrency is four, configurable from 1–32. A lane is
held through native execution, memory processing and reply delivery. The worker
durably claims a message before attachment download, session changes or execution.
`CreateChannelRun` atomically creates the run and
links it to its claimed message; the source is derived from stored identity,
not caller-supplied prompt content. `run show` retains channel, enterprise,
application, conversation, sender and message provenance. The local run viewer
can inspect that run's raw payload and events; it does not provide a channel
inbox UI. Native execution, business acceptance, memory persistence, reviewed
experience and reply delivery keep distinct states.

Recovery keeps never-claimed queued messages eligible under their original
profile. Interrupted `claimed` work becomes `unknown`, and interrupted
`sending` replies become `unknown`; neither is automatically repeated. Unknown
or failed work remains inspectable locally. A new message automatically replaces
a terminally failed session, with a brief notice and durable recovery provenance.
Unfinished memory records remain available for maintenance while new messages
use saved knowledge in a fresh session. Active work and storage/configuration
errors still fail explicitly. No failed task is automatically replayed.
The process holds the same exclusive
state-home lock as local writers, so memory review/retry requires stopping it.

## Reply capability and delivery

The callback's `sessionWebhook` is a short-lived bearer credential. It is stored
only in the dedicated `reply_url` column of the owner-private local SQLite
database for the queued delivery path. It is excluded from public message JSON,
run source, model input, terminal events and status inspection. Protect the
database and any copies as credential-bearing local state. The application
secret stays in its configured file and process memory.

Only HTTPS `oapi.dingtalk.com/robot/sendBySession` URLs with a nonempty session
parameter are accepted, with no user information, explicit port, fragment or
redirect. Expiry uses the actual `sessionWebhookExpiredTime`, not an invented
fixed lifetime. Replies use HTTP; Stream ACK data is not an IM reply. See the
[official reply guide](https://open-dingtalk.github.io/developerpedia/docs/learn/bot/appbot/reply/).

The host prepares the exact reply durably before one HTTP attempt. It sends
Markdown (`msgtype=markdown`, title `Agent-Host 回复`), capped at a conservative
host limit of 4,000 UTF-8 bytes. Replies contain task results and necessary
business-level warnings. The Host footer exposes the reported actual model and
measured execution duration to every audience. It reads `Run.Model` and the
`phase.finished` / `execute` event's `duration_ms`; configuration aliases,
`<synthetic>` models and missing/invalid measurements are not substitutes.
The duration covers native business execution, not queueing, routing or subsequent
memory processing. Failed turns retain observed metadata alongside their existing
failure guidance. Group quote/mention bytes and the footer are reserved before
body truncation, keeping the footer outside code fences and within 4,000 bytes.
Local paths, memory phases, native identifiers and execution diagnostics retain
their existing local-inspection rules.
Commands and legacy confirmation replies follow the same presentation rule. Native body
disclosure is governed by appended Host instructions, not hard isolation.
Owner-private diagnostic requests can return the requested detail; group
answers retain the shared-audience guidance.

`harness.ExecutionError` carries a failure kind and unwraps its original cause.
The Claude adapter categorizes startup, provider, process, deadline, cancellation
and unknown outcomes from native status, context and process evidence; the
channel does not interpret diagnostic strings. Live categories survive Host
error joining. Stored run errors and native output retain their raw diagnostics,
without a new schema field or reinterpretation of historical runs. Untyped
errors use the saved execution status for safe generic guidance.
Failure replies identify the category and supported next step. Once execution
started, canceled, failed and unknown outcomes warn that effects may be partial
and ask the user to inspect them before resending. No reply implies rollback or
advertises `/retry`. A completed execution with failed later saving keeps its
answer and clearly separates that outcome from memory persistence. Unsupported
input retains the existing text/image/file guidance without execution.

Oversized answers reserve room for the warning and excerpt notice, keep a
UTF-8-safe body excerpt, close an open fenced code block, and show an explicit
excerpt notice without a run ID or CLI command. The original headings, lists
and code are preserved rather than reformatted by a second model. Terminal output and the
stored native result remain full. The exact rendered reply is persisted before
its single delivery attempt; protocol rejection does not retry as plain text.
Reaction markers are separate from the answer. There are no typing text notices,
remote streamed answers, cards, proactive recipients, or alternate answer APIs
in this increment.

| Delivery state | Evidence |
| --- | --- |
| `accepted` | HTTP 200 with an explicit JSON `errcode` of zero |
| `failed` | Invalid/expired capability, invalid reply text, explicit HTTP rejection, or nonzero business code |
| `unknown` | Network failure, server error, unreadable/oversized response, malformed JSON, or missing business result |

Acceptance is platform acceptance only; it does not establish reading or task
acceptance. Failed and unknown delivery are retained and reported without
automatic resend. Raw remote errors and credential URLs are not printed.
The owned sender also closes response bodies, rejects redirects, enforces a
15-second deadline and limits response size, rather than using the SDK's
[HTTP-status-only reply helper](https://github.com/open-dingtalk/dingtalk-stream-sdk-go/blob/v0.9.2-beta.1/chatbot/chatbot_replier.go).

## Original-message progress reactions

The adapter uses generic public labels and the existing text-emotion payload. All calls use
the admitted callback's original `conversationId` and business `msgId`, together
with configured `robot_code`. No reaction target or label comes from model output.
The provider adapter uses `POST /v1.0/oauth2/accessToken`,
`POST /v1.0/robot/emotion/reply`, and `POST /v1.0/robot/emotion/recall` on
`api.dingtalk.com`. Application credentials and cached access tokens stay in
memory and never enter model context or reaction inspection.

| Durable transition | Reaction intent |
| --- | --- |
| Authorized supported task receipt commits | received: 已收到 |
| Native message worker claims receipt | processing: 处理中 |
| Internal execution/knowledge/memory phases | Local events only; no public phase labels |
| Final reply is prepared durably | processing: 处理中 |
| Task succeeds and final reply is accepted | completed: 已完成 |
| Task fails, or reply fails/is uncertain | failed: 打叉 |

Standalone Host controls and unsupported-input rejection do not create Agent
stages. Receipt, claim, final
reply preparation and delivery transactions insert their reaction intent
atomically. Legacy phase projections remain readable with their persisted provenance,
resolved scope, the original claimed message and its receipt-stage enrollment.
New public messages do not enroll detailed phase reactions. A local CLI turn
or a later manual memory retry cannot enroll a historical
message. Each phase intent retains `source_run_id` and `source_event_seq`;
component, Agent, model and timing remain available in the source event journal.
Empty extraction results do not enter the Jev assessment phase.

A shared registry in `internal/event/phases.go` defines display labels, channel
visibility, repeatability and failure/interruption labels. Native tool call and
result IDs drive `phase.started`/`phase.finished` child events, including measured
duration and source identity. Parallel calls are paired independently; unfinished
calls close as failed, canceled or unknown rather than successful. Continuing
model output or another tool restores the execution stage. The original tool
observations remain unchanged in the journal. New registered feature phases use
the same rendering path; no per-feature channel transport is needed.

Host-owned system guidance asks the native agent to consult available sources
when internal documentation is needed despite weak or missing memory matches.
It requests original-document reads and citations, preserves source limitations,
and never turns retrieved text into authority. The extractor receives no such
tool-use guidance. Prompt instructions do not themselves prove tool execution.

The unique message/stage/occurrence key prevents duplicates. Singleton phases
keep occurrence zero; repeatable phases use the committed source event sequence.
Each newly inserted phase,
replying or terminal intent skips older never-attempted `ready` intermediate
steps, retaining the initial receipt marker. Duplicate events keep their
original attribution and cannot supersede newer progress. A later occurrence of
the same phase is new progress, not a replay of an attempted operation. Attempted states are
preserved. An independent serial worker claims `ready` stages durably as
`sending`, removes known accepted active previous markers, records confirmed
removal, and adds the next label. A claimed request may briefly lag behind a
new phase while HTTP is in flight; there is no artificial execution delay to
force every short phase onto the screen. All host phases remain in the journal.
No reaction HTTP runs in the callback or native execution worker. The bounded reaction
request has a 20-second total deadline including cleanup and token acquisition.

Only explicit platform success is accepted. Rejection is `failed`; uncertain
mutation responses are `unknown`. Each stage gets one attempt. A cleanup failure
prevents that stage's add. Removal has its own durable claim and outcome; a
failed or uncertain removal is not repeated by a later stage or after restart.
Replacement remains blocked with an explicit error while cleanup is unresolved.
A previous unknown add is not treated as accepted or blindly resent.
Thus platform ambiguity can leave an old marker visible; local status
retains that uncertainty instead of claiming a clean replacement. Reaction
errors do not rewrite task or reply truth, and never trigger text fallback or
native replay. Storage/observation failures stop the service explicitly.

Recovery changes interrupted `sending` markers to `unknown`, skips never-sent
intermediate markers for terminal tasks, and queues the terminal marker from
stored task/reply truth if it was never attempted. A task completed before its
reply was recorded is not marked successful. Historical messages without a
receipt-stage row are not enrolled during migration. Each stage's outcome,
error, timestamps and known-active flag is exposed by `dingtalk status`;
`channel.reaction.started/finished` events include stage, label, message ID,
outcome, elapsed duration and phase source references. These records remain separate from native output
and memory evidence.

Robot code is the reaction actor, not a native execution setting. Adding this
required configuration field preserves existing session bindings and queue
profiles. Changing the configured robot code affects subsequent reaction
attempts; removing another robot's existing marker may fail explicitly.
Original message targets, application, enterprise and owner remain fixed.
The read-only run/memory viewer supports schemas through v11 without migration;
private reply and input capabilities are not queried.

Offline validation on 2026-10-10 passed `make check` (formatting, vet, all Go
tests, 20 installer tests and 17 viewer tests), plus race tests for channel,
DingTalk transport, state and CLI. Regression cases cover original-message
replacement, duplicate receipt, slow feedback without blocking execution,
native/memory/reply failure, missing platform confirmation, token lifecycle,
interrupted adds/removals, atomic outbox insertion, and nonretroactive v4
migration. Live reaction acceptance and client display are tracked separately
in the deployment record; offline tests do not establish either.

Phase regression also covers durable event attribution, conditional extraction
and assessment, visible Jev progress before completion, skipped obsolete steps,
Jev failure without false completion, and migration preservation. New labels
require live platform acceptance separately from these offline checks.

## Lifecycle and validation

The worker starts only after the receiver reports a successful connection.
The official SDK reconnects after known read, ping and closed-connection
failures, including server-requested disconnection. Its
cancellable exponential backoff starts at three seconds and caps at sixty
seconds, with jitter. Safe `channel.reconnecting` and `channel.reconnected`
events expose recovery without logging credentials. ACK-write failures emit
`channel.transport_error` without claiming a reconnect has begun; the saved
receipt remains subject to deduplication. Native work and the queue
continue through temporary disconnection; durable receipt deduplication prevents
redelivered callbacks from executing twice. Startup, malformed-frame, callback
saturation, ingestion and observation failures remain explicit. The managed
production LaunchAgent restarts unexpected process failures with a 30-second
throttle. On shutdown the receiver, native workers and reaction worker are
joined before the store and home lock are released. Native turn deadlines use
`--timeout`; a separate bounded finish context records the outcome and permits
one final delivery attempt after cancellation or timeout.
SQLite transaction failures preserve cancellation together with the underlying
database error, including automatic rollback before commit. Successful commits
remain successful; cancellation never triggers native replay.

Native Claude still owns tool execution. Configured channel instances use
`bypassPermissions`; the Host does not forward native permission prompts to
DingTalk. Owner-private instructions impose no extra Host approval or disclosure
restrictions; group instructions judge affected resources, shared disclosure and
authenticated identity. Select local tool policy before startup; `--no-tools`
remains available
for controlled acceptance tests, and file attachments require reading tools.
Source text and remembered knowledge cannot grant a wider recipient or tool
scope. Host settings do not provide an operating-system sandbox.

Automated coverage uses temporary SQLite stores, synthetic native runners,
official SDK callback registration, and injected HTTP transports. It covers
identity rejection, text limits, duplicates, source linkage, configuration
changes, `/new`, interrupted claims, reply ambiguity, credential exclusion,
error ACKs, lifecycle failure and cancellation. These are offline contracts,
not evidence of a live platform round trip.

The 2026-10-12 failure-reply regression covers native category/error-chain
preservation, safe owner/group errors, partial/unknown/canceled outcomes, startup
and session guidance, completed execution with failed saving, and a real turn
deadline followed by fresh-session recovery without replay. Existing plain
`richText`, forwarded `markdown` and cancellation-persistence fixtures remain
valid; these earlier production error categories did not require another parser
or journal fix. Live failure display and provider recovery remain separate
acceptance checks.

`make check` and `make build` passed the assembled repository. The final
`go test -race ./...` passed 428 cases across 18 packages. The existing memory
recall regression passed 26/26, and the viewer's eight tests passed with schema
v4 inspection enabled. A local `dingtalk check` using the intended configuration
also passed, without network or native authentication validation.

Validation exposed a canceled-transaction error that could lose its cancellation
cause; the fix has deterministic rollback coverage. One race run overlapping
other Go checks exceeded an existing evaluation fixture's one-second deadline;
the standalone full race run passed. Evaluation deadlines and quality assertions
were not changed. This is not a latency or load-test result.

The owner subsequently authorized switching the existing application and stopping
the old memgov service. No production bot was connected or sent a message during
the original channel-code increment. See the
[service deployment evidence](service-install-detail.md#deployment-evidence) for
the later installation. Live acceptance must validate owner DM receipt and reply,
conversation continuation, `/new`, rejection boundaries, local provenance and
one interrupted-turn recovery with the real bot and selected native harness.

## Automatic routing and conversation controls

The [runtime assembly](runtime-assembly-detail.md) and
[intelligent routing](smart-project-routing-detail.md) paths select a project set
when its association is reliable. Other valid requests execute in the resolved
agent's default workspace; an empty catalog skips classification entirely.
The original unresolved decision remains audit evidence, but creates no Host
project question or waiting task. Native execution asks for business details
when needed. Historical manual pins remain readable but do not affect dispatch. Project
registration and instance settings are local operator interfaces.

The ordered conversation inbox processes standalone `/new`, `/status` and `/help` as Host controls without
model execution or memory extraction. `/new` resets the compatible native session
for the latest actually executed project set, or the default when none exists,
and clears follow-up routing context while preserving memory. `/status` returns
only a safe conversation/recent-task summary and any legacy pending count;
`/help` lists the three supported controls. Native interactive commands are not
forwarded. `/status` waits behind earlier work in its conversation lane. Messages
with quote or attachment material bypass Host command handling entirely; command
text in their material does not reset a session or alter configuration. Retired
`/project`, `/projects`, `/routing`, `/choose` and `/cancel`
receive migration guidance without a model call; other slash commands return
the supported control list.

Selection uses verified channel identity and the agent/audience partition.
Each native run pins its project set, directories, scoped context and
configuration before execution. The queue profile and reaction/delivery outboxes
retain their own configuration and lifecycle.

Existing pending requests remain unchanged without replay. Only their original
requester may act through "确认 <任务编号> <项目名称>" (multiple projects
separated by `、`) or "取消 <任务编号>". A confirmation may retain a later
constraint after ` -- ` as a separate source. Bare project names, bare "取消"
and ordinary answers remain native conversation input and cannot claim a legacy
request. Pending lookup filters by sender before limiting results. Final delivery
uses the confirmation's valid reply capability, linked to the original request.
Duplicate confirmations and recovery cannot replay work with an unknown result.

Default handoff preserves configured directory, project and source permissions.
Configuration, selected-directory and provider failures remain explicit errors.
New unmatched requests use normal default memory attribution and saved-history
tools even when the catalog is populated. Old `MemoryAttribution=unresolved`
evidence retains its original skipped memory outcome; it is not replayed or
reprocessed. Compatible default sessions may continue, with trusted per-turn
instructions separating the new
request from unrelated earlier context.

Joint runs retain selected scope labels and native additional directories;
automatic new memory and experience extraction are paused for those runs.
The [historical routing acceptance](smart-project-routing-detail.md#validation-and-acceptance)
passed 17 isolated owner messages, eight native runs and one production-restored
command. Seven key replies and the clarified task's final completion reaction
were independently observed. Its remaining unknown reaction and notification
observation limits are recorded separately from execution and reply results;
this evidence is distinct from historical channel and explicit-selection checks.
It used the earlier command and project-question interface and does not establish
live acceptance of the current default-execution policy.
