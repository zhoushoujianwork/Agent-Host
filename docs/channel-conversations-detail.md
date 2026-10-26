# Conversation execution and attachment details

The [main design](channel-conversations.md) defines scope and user behavior.
This document describes the current implementation contract; validation results
must distinguish automated fixtures, native execution and live DingTalk delivery.

## Scheduling and lifetime

The receiver validates enterprise, sender and mention identity, resolves the
agent/audience selection and commits a deduplicated receipt before acknowledging
it. Downloading attachments and executing tasks happen after a durable claim,
not inside the callback.

The scheduler claims eligible messages in receipt order within the fixed queue
profile. It excludes an active authenticated conversation binding and an active
`AgentID`/`AudienceID` pair. This preserves conversation FIFO and prevents two
turns from judging and finalizing against the same stale memory snapshot. A
busy conversation does not prevent a different eligible audience from running.
One group remains shared across its members; sender identity does not create a
parallel group lane. Different transport conversations in the owner partition
also serialize when they share the same agent/audience.

`max_concurrent_conversations` in `service.json` and the foreground
`--max-concurrent-conversations` option bound active turns. Omitted/zero uses
four; explicit nonzero values must be 1–32. The scheduler retains each occupied
binding and partition through native execution, synchronous memory/experience
processing and the final reply attempt. `/new`, `/status` and `/help` enter the
same ordered queue. There is no immediate status path, asynchronous memory
finalization, command cancellation or `/stop` in this increment.

The reaction outbox still has its own worker and delivery states. A slow
reaction call does not hold the native turn or final reply. Shutdown cancels
and joins active task workers before releasing the store and state-home lock.
SQLite transactions protect durable state; concurrency does not introduce a
second process writer or permit local memory review while the service owns
the state home.

Native calls receive their own cwd, environment and agent/audience profile.
These isolate conversation state, **not project files**. Different audiences
may share an actual cwd or additional directory and can make conflicting edits.
There is no checkout lock, worktree creation or multi-agent coordination.

## Accepted input and provenance

The existing identity and size checks still apply. The parser accepts ordinary
text, forwarded `markdown`/`interactiveCard`, standalone `picture`/`file`, and supported ordered
`richText` parts: text, HTTP(S) links, pictures and files. Rich text retains textual
order with attachment placeholders. Links preserve both label and target without
fetching the target; supported fragment shapes are described in the
[DingTalk parser contract](dingtalk-channel-detail.md).
Unsupported attributes, unknown part types and malformed or
duplicate keys reject the whole input; an executable text prefix is never
dispatched on its own.

Pictures may supply distinct `downloadCode` and `pictureDownloadCode` values,
as shown in the [official callback examples](https://open-dingtalk.github.io/developerpedia/docs/learn/bot/message/).
The parser prefers `downloadCode`, with `pictureDownloadCode` as a fallback;
the two values need not match. Both remain protected capabilities.

Inbound `markdown` uses `text.content`, with `content.text` and optional
`content.title` as a fallback, matching the [DingTalk connector parser](https://github.com/DingTalk-Real-AI/dingtalk-openclaw-connector/blob/main/src/core/message-handler.ts).
The receipt keeps the original type and a neutral forwarding marker. Its body
is stored in the existing quote field as reference material, bounded to 4,000
runes with explicit truncation; an optional title is bounded to 256 runes.
Forwarded commands do not invoke Host controls or become the sender's memory
statements. Markdown links are retained as text, without image/history fetching.
Merged chat records still require callback-provided quote summaries; accepting
Markdown does not establish support for every DingTalk forwarding format.

Standalone `interactiveCard` forwards reuse the quoted-card preview projection:
object/stringified `content` and top-level preview fields supply bounded text,
title and an optional action link. The receipt retains the provider type and a
neutral forwarding marker; the preview stays separate reference material with
`summary_only`, no inferred original-card ID, media downloads or approval action.
An unreadable preview in an owner-private chat can use the configured owner
workspace's `chat` service to read exactly the current forwarded copy. The private
adapter checks the official read contract, owner profile, returned message ID
and conversation ID, with a 30-second timeout and 12,000-byte result limit.
The same redaction/preview bounds apply before saving `source` as
`dingtalk_workspace_message` on the unexecuted claimed receipt. Saved enrichment
does not change duplicate identity or overwrite the original callback preview.
Groups, other senders and tool-disabled/workspace-disabled agents do not use the
owner's reader. Missing/failed reads return an explicit explanation without
starting native execution. Earlier failed receipts are never automatically
rewritten, replayed or replied to again.

Validation on 2026-10-19 reproduced the unsupported standalone-card path before
the change. Parser and assembled-channel regressions cover object/stringified
previews, private/group intake, top-level preview fields, unavailable content,
source attribution, command isolation and duplicate suppression. The channel
suite passed 947 tests and `make check` passed. The opt-in
`AGENT_HOST_CARD_LIVE_RUNTIME=/absolute/runtime go test ./internal/channel -run
TestForwardedCardNativeLive -count=1 -v` also passed: real `claude-sonnet-5` read
a synthetic forwarded approval's identifier, destination and port in isolated
state with fixture delivery; no platform message was sent by that test.
The independent production binary was replaced with a retained rollback copy.
Fresh Feishu and DingTalk readiness appeared at 12:17:30 and 12:17:33 Singapore
time, both workers remained running and the workbench returned HTTP 200.
The fresh client forward at 12:24 still produced an empty parsed preview, so the
initial deployment did not establish support for that card. An exact authorized
owner-workspace read then recovered its approval text. An isolated assembled
channel/native test independently exercised empty-preview intake, the embedded
workspace read, source persistence and real Claude interpretation of the actual
VM-upgrade request. Delivery remained a fixture; this test did not send an IM or
perform an approval action. Regressions cover source mismatch, unavailable text,
reader scope, duplicate reads and post-execution evidence preservation.
`make check` passed for the correction. The corrected independent runtime
reconnected Feishu at 12:29:56 and DingTalk at 12:29:59 Singapore time; both
workers stayed running and the shared workbench returned HTTP 200. The prior
failed production receipt remains unchanged. New inbound delivery through this
fallback remains a separate client round trip from the isolated real-card test.

| Material | Bound and treatment |
| --- | --- |
| Attachments | At most four per message, across all supported kinds |
| Pictures | At most 5 MiB each; PNG, JPEG, GIF or WebP; native image blocks |
| Files | At most 20 MiB each; private local file for native reading tools |
| Quoted text | Callback-provided body, capped at 4,000 runes; truncation marked |
| Quoted chat record | Callback-provided title/summary, explicitly summary-only |
| Forwarded Markdown | Callback-provided body/title as reference material; same quote bounds |
| Interactive card quote | Callback `cardContent` TEXT nodes, supplied title/text and HTTP(S) action link; marked as a preview, without remote fetching |
| Quoted Markdown / rich text | Callback text fields, with media capabilities excluded |
| Missing/other quoted content | Retain available message identity/type without inventing its body |

`ChannelInput` keeps attachments and quotes separate from the current sender's
statement. The quote retains available original message ID/type, body/title,
summary-only and truncation flags. A quoted author is not authenticated as the
current sender. The Host does not fetch DWS history, resolve missing original
messages, or download media referenced only by a quote. Normalized quoted
provider objects omit transport capability fields.

Only messages without quote or attachment material can invoke Host controls.
A command-looking sentence accompanying such material, or inside that material,
goes to the native agent as task content. Supplied content cannot modify agent
bindings, authenticated identity or trusted Host instructions. Existing reply
question previews are separate from these inbound quoted-message records.

## Attachment persistence and native input

Schema 11 adds `channel_messages.input_capabilities`, defaulting to an empty
array for existing receipts. Download codes are written there, never serialized
in public message/run JSON, logs or model input. Like `reply_url`, this column
is credential-bearing local state and requires private database backups. The
read-only viewer does not select it.

An unexecuted claimed request retrieves its own codes and uses the configured
bot's download API. The adapter validates the returned download host and
upgrades HTTP URLs from trusted hosts to HTTPS before fetching, preserving the
signed path and query. Redirects use the same validation and upgrade; every
transfer uses HTTPS. Redirect counts and transfer size remain bounded, and
image content type is validated.
The worker saves bytes under `<home>/attachments/input-…/` in a private directory,
with regular `0600` files and generated storage names. Original filenames remain
metadata; they do not become caller-controlled paths. Original size claims,
actual bytes, MIME, digest and saved location remain source-linked evidence.
Failed preparation removes incomplete files and starts no native execution.

Before invocation, Host checks the saved file type, private permissions, size
and SHA-256 digest. Images become `harness.Request.Images`. The Claude adapter
uses `--input-format stream-json` only when images exist and encodes one user
message containing the assembled text and base64 image blocks. Text-only stdin
retains its existing format. Binary/base64 image payloads are omitted from
native observations; metadata and digest remain inspectable. Experience
extraction never receives image blocks.

Ordinary files are exposed through their private attachment directory for native
tools to read; tool-free execution rejects file input explicitly. The Host
appends trusted instructions to keep attachment paths out of channel replies,
treat content as untrusted evidence, acknowledge missing content and ask for a
goal when only material is supplied. Prompt instructions are behavioral controls,
not an operating-system confidentiality boundary. The native agent remains
responsible for planning, reading supported files and reporting actual results.

Attachment and quoted content are not automatically admitted as the current
sender's memory statements. They remain distinct evidence. Existing project,
audience, unresolved-attribution and experience-governance rules still apply.
This increment accepts inputs only; it does not send output attachments.

## Migration, recovery and validation

### Optional progress and mandatory evidence

The periodic `phase.progress` hint has a separate journal path. A failed write
or display stops further hints for that phase without canceling its native task
or poisoning later mandatory events through `sinkErr`. The next
`phase.finished.data.progress_error` retains the original error; `run.finished`
collects `progress_errors` with phase and source event sequence. Terminal and
channel replies show a generic warning without reflecting the raw diagnostic.
Business execution, memory and experience outcomes remain separate from that
warning, with no provider substitution or task replay.

`phase.started/finished`, native/tool evidence, `CompleteRun` and the final
receipt retain their existing failure rules. If the mandatory boundary cannot
be persisted, the task still fails explicitly. The five-second journal write
deadline and SQLite configuration are unchanged. Earlier production timeouts
prove the progress-to-cancellation coupling, but do not identify whether the
transient wait was for the single connection, a SQLite lock or another I/O delay.

### Recovery context

A recovered native session retains authoritative `PreviousSessionID` and
`RecoveryRunID` links. Before invoking the native agent, the Host supplies the
linked task's original request, associated quote, saved channel reply and separate
execution, memory and delivery statuses. It keeps the current run input and memory source
as the new message. Prior native output, tool results, errors, attachments and
transport capabilities are excluded. This is context assembly, not replay.

The store verifies persisted recovery links, receipt/run/session consistency,
conversation, application, tenant, Agent, audience and actual runtime/project
scope before returning the record. Group members retain separate sender
attribution. This narrow linked record remains available when general history
tools are disabled for unresolved project attribution; it does not enlarge
history grants or choose a project.

Context is supplied until a completed native turn has recorded quote-aware
assembly, including a recovered session created by an older binary without
the context or with only the original text-only handoff.
Later compatible turns use native continuation. If another recovered retry
fails, up to three directly linked recovery records preserve the original
question; the latest request appears first. Each original request is capped at
4,000 runes and each saved reply at 2,000, with explicit truncation.
The native instructions require reviewing available context and,
when needed and authorized, searching recent history before asking the user to
restate an unclear follow-up. A quote title alone cannot establish that the
original request is unavailable. Execution completion remains distinct from
task acceptance; returning a clarification does not prove the task was done.

When a fresh native session has no recovery record, the Host supplies up to
three prior user requests from a bounded eight-candidate window in the same
authenticated conversation and exact current project/scope. Each request is
capped at 4,000 runes and retains its sender and message identity. This
inbound-only handoff includes the request's supplied quote as separate reference
material and tolerates a model or configuration fingerprint change;
it excludes earlier project scopes, replies, native output, media and transport
capabilities. Compatible resumed turns do not repeat it. Current input and
memory attribution remain unchanged, and general history grants remain separate.

Historical quotes retain their own source message ID, type, title/body,
summary-only and truncation flags. The recorded request sender is not treated
as the quote's authenticated original author. Quote bodies are capped at 4,000
runes and titles at 256; further truncation is marked without rewriting the
original receipt. Quoted attachments and missing original bodies are not fetched.

Migration retains old receipt payloads, queue identity, session bindings, memory
sources and reactions. Old queued inputs remain eligible under their original
profile. Previously rejected inputs, including Markdown forwards, are not silently
revived. Redelivery after this parser upgrade remains a duplicate with its
original rejection, without execution or a second reply. Claimed work with
unknown results, attempted uncertain replies and uncertain reactions retain the
existing no-replay rule. A missing or expired input capability fails the new
request explicitly rather than switching to another source.

Validation on 2026-10-11:

- `make check` passed. The relevant Go race suite passed 886 tests across eight
  packages, covering conversation/partition ordering, bounded parallel progress,
  duplicate receipt and shutdown, media limits and integrity, quoted provenance,
  command-looking material, native image encoding and capability exclusion.
- Separate isolated native Claude calls correctly interpreted an image and read
  a file using the native reading tool.
- After `make install`, a live group task and owner-private task executed during
  overlapping intervals. Two private turns completed in order, with the second
  resuming the first session. All three replies were accepted; a DWS event also
  independently observed the group reply.
- Live media acceptance remains incomplete: DWS file upload failed before any
  Host callback, and a programmatic quoted reply arrived without quoted-content
  metadata. Its successful answer validates session continuation, not quote
  parsing. Image/file delivery and client-provided quote metadata still require
  platform acceptance.

Validation on 2026-10-12 for private forwards and image-with-text input:

- Read-only production receipts confirmed two private failures at 10:19
  (Asia/Singapore): `markdown` and `richText`, both rejected before native execution.
  DWS reread found both messages, but did not expose their original Stream shapes.
- `make check` and the race tests for DingTalk, channel handling and state passed.
  Regressions cover both image codes, ordered text and native image bytes,
  Markdown reference attribution, literal JSON fidelity, commands, and duplicate
  rejection without replay.
- `make install` completed at 10:48 (Asia/Singapore), with a fresh Stream
  connection and a stable running process. A new client forward/image turn
  remains unverified; old rejected messages were not replayed.

Recovery-context validation on 2026-10-12:

- Regression checks cover a failed question followed by a title-only quoted
  retry without history tools, an independent new task, older recovered sessions,
  repeated failed retries, model/configuration changes, original memory
  attribution and duplicate prevention.
  Boundary checks reject cross-conversation, audience and runtime evidence.
- `make check` and race tests for channel handling, state, Host and history
  passed. General unresolved-project history remains unavailable.
- An isolated native Claude check used a synthetic failed CBS question, followed
  by only `再试试` and quote title `Agent-Host 回复`. With the linked record
  supplied, Claude correctly identified the CBS CLI / `s3-proxy` question in
  9.6 seconds. The check executed no object-storage operation and sent no IM.
  This confirms bounded native input handling, not a new live group round trip.
- `make install` completed at 14:32 (Asia/Singapore), with a fresh Stream
  connection and a stable running process. The new group retry flow still needs
  a live client round trip; no previous task or reply was replayed.

Associated-quote validation on 2026-10-12:

- A source snapshot based on `175b49a`, with only this increment applied, passed
  `make check` and all 593 race tests for state, channel handling and Host.
  The shared checkout also contained unfinished parallel changes and was not
  used as the validated build source.
- Regressions cover forwarded Markdown and supplied quote summaries across
  failure recovery and model changes, source identity and preview flags,
  immutable receipt bounds, attachment exclusion and no duplicate execution.
  Older text-only recovery bridges receive the quote-aware context once.
- `make install` from that snapshot completed at 14:54 (Asia/Singapore), with a
  fresh Stream connection and a stable running process. This confirms service
  installation; a new client quote round trip remains unverified.

Real input samples, test messages and detailed platform records stay outside the
repository. Automated fixtures and native checks do not establish live media
delivery.

### Signed image download correction (2026-10-14)

The reported group image-with-text receipt was parsed successfully but failed
before native execution. Its authenticated download resolver returned an HTTP
signed OSS URL, which the HTTPS-only downloader rejected. The same signed path
and query returned a valid PNG over HTTPS. Trusted resolver URLs and redirects
are now upgraded before dispatch, while untrusted hosts, explicit ports,
credentials in URLs, redirect limits and existing media limits remain enforced.

Regression fixtures exercise signed resolver URLs and redirects, unchanged
signatures, HTTPS-only requests and application-token exclusion. An assembled
image-with-text test covers private attachment evidence, native image input and
duplicate prevention. Separately, the patched Go downloader fetched the original
233,325-byte PNG; an isolated tool-free native Claude call correctly read its
visible URL and provider. The failed channel task was not replayed. A fresh
client image round trip remains separate from these download/native checks.
`make check` passed, along with all 491 race tests for DingTalk and channel
handling. The rebuilt runtime restarted with fresh DingTalk and Feishu readiness
at 14:05:47 Singapore time; both worker PIDs remained stable and the shared
workbench returned HTTP 200. Detailed receipt evidence remains in the ignored
runtime area.

### Native background completion correction (2026-10-14)

The resubmitted image downloaded and was preserved successfully, then Claude
read it and delegated a source-code search. A completion notification caused
Claude Code 2.1.223 to re-emit `system/init` for the same session and initial
model. The adapter previously rejected every repeated initialization and
canceled the native process before the final answer.

Repeated snapshots now retain native evidence without starting another Host
execution or overwriting the resolved model and tool correlation. Each snapshot
still validates the session, initial model, MCP connections and extraction
restrictions. Native task starts link to observed tool calls; a matching terminal
notification and validated initialization authorize its continuation result.
An intermediate submitted-turn result can arrive before or after that
initialization. The adapter retains the later answer and waits for native process
exit, required results and completed task boundaries. Unrelated notifications,
invalid linkage, duplicate results and incomplete work cannot establish success;
an unsuccessful accepted result remains a failure.
Synchronous Agent calls consume their notice through the correlated completed
tool result. If an async notification is handled by a new parent response in the
already-active query, completion waits for a clean process exit; a child response
cannot satisfy that boundary. The notice remains pending during streaming, so a
later initialization and continuation can still replace an interim waiting answer.

The separate isolated native image check reproduced the async lifecycle and
completed with its final synthetic marker and the screenshot's visible URL.
It executed no original business task and sent no IM. A fresh client round trip
remains separate from this native verification; the failed production receipt
is preserved without replay.
The final `make check` passed. Regression coverage includes initial/resumed
image runs, synchronous and asynchronous Agent completion, queued result order,
inline parent responses, missing final boundaries, stale waiting results,
duplicate rejection and repeated MCP/extraction checks.
All 485 race tests for the Claude adapter and channel handling passed. The
rebuilt runtime emitted fresh DingTalk and Feishu readiness at 15:01:20 Singapore
time, both worker PIDs remained stable, and the workbench returned HTTP 200.
Detailed native and runtime evidence remains in the ignored local runtime area.


## Interactive-card quote parsing (2026-10-13)

Previously `parseQuote` recognized only text and chat-record summaries. It kept
an `interactiveCard` message ID/type but discarded its supplied content.
An empty normalized receipt therefore did not establish an empty raw callback.
The parser now handles object/stringified content and extracts visible TEXT
values from `cardContent` LIST/RICHTEXT children in order. Optional title/text
and `biz_custom_action_url` are retained as callback-provided source material.
Top-level supplied preview fields are also supported. These previews remain
`summary_only`; no full approval record or original author is authenticated.
Markdown/rich-text references retain callback text without downloading media.

Card traversal stops at depth 16 or 1,024 nodes and retains at most 4,000 body
characters and 256 title characters, marking truncation. Only explicit TEXT
values and documented preview fields become text. Media/download/webhook fields
remain redacted. Raw platform IDs still cannot be read as Host history references.

Regression checks use the [reported live callback shape](https://github.com/soimy/openclaw-channel-dingtalk/issues/598)
and exercise private/group quotes, encoded content, ignored image metadata,
text/link fallbacks, Unicode bounds and deep trees. An assembled channel check
verifies that the preview reaches the native prompt and saved run evidence while
the current sender's request remains separate. Older receipts cannot be reconstructed because raw callbacks were not retained.
The owner resent the same card after deployment; its new quote body contained
the visible approval fields, the native reply accurately restated them, and
DingTalk accepted delivery. This validates that specific quoted card, not every
card schema or approval execution. Detailed receipt evidence remains in the
ignored runtime area.


`make check` passed after the correction. The updated runtime was rebuilt and
restarted; DingTalk and Feishu emitted fresh connection/readiness events at
14:59:58 Singapore time, and the shared workbench returned HTTP 200 on port 8765.
No earlier task was replayed. The owner-card acceptance at 15:00 Singapore time
then passed independently of offline checks: callback preview extraction, native
reading and final reply delivery all completed. Approval processing and machine
changes were not part of this validation.
