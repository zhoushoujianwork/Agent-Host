# Agent conversation history implementation notes

The [main design](conversation-history.md) governs scope. Both routes are
implemented independently and passed bounded owner-DM checks. Continuous
polling, group quality and the remaining platform checks are listed below.

## Host history source and access

`internal/state/conversation_history.go` derives a read-only view directly from
durable `channel_messages` and linked runs. No second bot cache or history
migration is needed. `internal/history` exposes newline-delimited MCP stdio
through the private `history-mcp` command. It offers exactly:

| Tool | Contract |
| --- | --- |
| `search_conversation_history` | Optional lexical phrase and inclusive/exclusive RFC3339 receipt-time bounds; at most 20 snippets, with a continuation cursor |
| `read_conversation_history` | An authorized search reference, at most three neighbors on each side, and bounded exact-text excerpts; Unicode offsets and expected digest support continuation |

`history.Resolve` takes the current durable receipt ID. Identity and grants
come from its admitted account/tenant, conversation, Agent, audience, route and
linked run; callers cannot choose a different principal or project. The MCP
launch fingerprint is checked again at startup and on every query. Predicates
apply before matching, limits and neighboring reads. Mixed-project turns require
all their project grants. `Store.ChannelMessages` remains a Host-wide
administrative queue and is never exposed as a tool.

Ordering uses durable receipt order and a snapshot ending before the current
request. New receipts do not change a paginated query. Reply/delivery changes
alter the content digest; continuation with an old digest fails explicitly.
Native resets do not delete evidence. A stable history-policy marker enters
runtime compatibility; per-turn receipt IDs and snapshot boundaries do not
rotate otherwise compatible native sessions.

Input, quotes and saved `ChannelMessage.ReplyText` retain separate attribution.
Raw `Run.Output` is not the channel reply. Receipt, execution and delivery
statuses remain distinct; `accepted` means platform acceptance, not that the
recipient read it. Results omit reply webhooks, download capabilities, native
tool traces, raw execution errors and absolute attachment paths.

Eligible failed executions retain their status. Historical unresolved handoffs
are eligible only when their saved scope equals the receipt's original default
scope and no project is selected. Reading them never reprocesses memory or
changes their original records. Unrouted queued receipts are excluded;
standalone `/new` and `/help`
can be included only in their original default scope. Older records whose
identity/project provenance is insufficient remain unavailable. History covers
admitted group mentions, not the group's full timeline.

The Host mounts history after linking the current channel run and adds trusted
usage instructions without prefetching transcript content. Default handoffs keep
normal history access; malformed bindings fail before
native execution. Local CLI tasks, `--no-tools` and tool-free extraction do
not mount it. Paired retrieval calls/results remain run evidence and are excluded
from execution-experience extraction. Native tools retain existing OS access;
this MCP boundary is not filesystem isolation.

Search is a case-insensitive lexical scan over retained receipts in this
conversation, not semantic retrieval. Excerpts/results are bounded; latency
for very large retained conversations has not been measured.

## DWS configuration and operation

Copy [discovery.example.json](../config/data-discovery.example.json) outside the
checkout, replace placeholders and set mode `0600`. Configuration must be a
regular current-user-owned private file, without symlinks, duplicate or unknown
fields. It selects an absolute DWS executable, exact `corpId:userId` profile,
bounded polling/import/reconciliation/retention limits, native model, and explicit
sources. Every source names its verified conversation ID, `dm` or `group`,
and one registered project ID; a DM additionally names its
`peer_open_dingtalk_id`. `default` and unmapped sources are rejected.

```sh
agent-host discovery check --config /absolute/private/discovery.json --home /absolute/host/state
agent-host discovery collect --config /absolute/private/discovery.json --home /absolute/host/state --claude /absolute/path/claude
agent-host discovery sources --home /absolute/host/state
agent-host discovery topics --home /absolute/host/state
agent-host discovery show --home /absolute/host/state TOPIC_ID
agent-host discovery accept --home /absolute/host/state --project PROJECT_ID TOPIC_ID
agent-host discovery dismiss --home /absolute/host/state TOPIC_ID
```

Options precede the positional topic ID. `check` validates local configuration
and registered project mappings when state exists; it does not authenticate DWS
or invoke a model. Read-only inspection needs no model credentials. Acceptance
exports a bounded cited snapshot for owner-private handoff; `show` preserves
access to the exact snapshot before and after acceptance. It is not public
group context or task authorization.

Standalone mutations acquire the Host ownership lock. Stop the managed service
before standalone collection/acceptance/dismissal against its state. For polling,
use `discovery serve` with the same options, or add an absolute
`discovery_config` reference to the private managed `service.json` and reinstall.
Omission keeps collection disabled. Its worker shares the serialized Store,
runs independently of bot execution, and reports structural coverage/failures
without source bodies. Collector failures do not disable bot history or delivery.

## DWS evidence, coverage and topics

`internal/discovery` invokes the installed `chat +chat-messages` leaf under
the fixed profile, with an exact peer/group selector, ascending bounded
`[start,end)` query and page/message limits. Local profile registry identity
is checked; this does not independently prove remote authentication.
Positive messages must match the configured conversation and time range.
An empty result proves only the executed selector's returned window, not
conversation existence or cross-chat absence. No invented `resolvedFilters`
field is assumed: the verified installed leaf does not return a conversation
filter there. The earlier [live private retrieval check](knowledge-sources-detail.md#dws-private-chat-retrieval-acceptance-2026-10-11)
remains separate evidence.

Schema 12 adds separate sources, collection ledgers, immutable evidence
revisions, current-message pointers, topic proposals and exact citation links.
Evidence and checkpoint writes are transactional. Source identity includes the
profile, conversation, type, peer and project. Overlapping history polls deduplicate
identical revisions; changed content creates a new immutable snapshot.
The provider's completeness, failures, continuation and truncation ledger
remains privately inspectable; ordinary status output exposes only compact
coverage/counts.
Valid incomplete failure ledgers also retain attributable rows: an initial read
may have zero successful pages, and a pagination failure may leave its next
page unknown. These outcomes never establish complete coverage or invent a
cursor. Identity, range, counts and duplicate fields remain strictly checked.

Only complete windows advance the checkpoint and enter extraction. Interrupted
or failed windows retry their retained range. A bounded partial result remains
pending; with unchanged page/message limits the collector marks it blocked
instead of refetching the same pages indefinitely. Raising limits explicitly
allows another attempt. Current pagination is the provider's bounded
`--page-all`; no durable cursor continuation is claimed. Very busy ranges can
remain incomplete at maximum limits.

History reconciliation captures owner-sent messages that the personal incoming
event feed omits. Polling is not loss-free real-time listening and does not
promise inaccessible historical messages or complete edit/delete coverage.
Restart catch-up uses bounded windows without silently skipping the gap.

A separate native extraction session runs with tools disabled, bounded input,
output and deadline. It proposes at most three actionable topics, preserves
reported outcomes and cites only supplied immutable evidence IDs/digests.
Up to 200 retained current snapshots enter one extraction; reports show
available/selected counts and partial analysis. Identical evidence digests skip
successful repeated extraction. Proposal evidence fingerprints suppress
duplicates; a correction remains new source evidence.

Topic review and acceptance never create a fake run, trigger delivery, or write
memory. The first scope is local owner inspection and export under the exact
configured project. Cache expiry removes old active-message pointers and uncited evidence, while
every retained proposal keeps its immutable cited snapshots. Conversation-derived episode review,
notifications and accepted-topic task creation remain deferred.

## Migration and validation

Schema 11 upgrades additively to 12; existing receipts, runs, sessions, project
registrations, private transport state and memory are preserved. Installer
project inspection reads schema 11 without migration, and the read-only viewer
accepts schema 12 while omitting discovery/private payloads. Back up the private
SQLite database before a production upgrade; rollback to an older binary also
requires its matching pre-upgrade database.

Automated tests cover current-conversation/project/audience/Agent isolation,
guessed references and neighbors, reply/delivery revisions, controls, reset and
native continuation, duplicate receipts, concurrent updates, protocol bounds and
secret omission. DWS tests cover explicit selection/profile, source revisions,
transactional checkpoints, partial/restart/failure states, bounded extraction,
citation validation, topic review/acceptance, retention and no run/memory side
effects. Genuine schema 11 migration fixtures preserve all legacy tables.

Native acceptance uses the real Claude harness with isolated synthetic history
and discovery inputs; it never sends a DingTalk message or reads a live DWS
account. The [native reports](../experiments/history/README.md) record passing real
search/read, refreshed receipt access in the same resumed native session, and
topic extraction/review/export checks on 2026-10-11. `make check`, 45 focused
race tests and local document links passed.
The installation report (private validation evidence)
records a fresh production Stream connection, schema 11-to-12 upgrade, private
pre-upgrade backup and unchanged rows in all 23 legacy tables. DWS remained
unconfigured with zero collected sources.
Earlier text-only platform acceptance does not validate these new tools.
The selected live DWS discovery report (private validation evidence)
records four real messages from the configured owner-bot DM and one native topic
with all four original citations. Review preceded acceptance in private test
state; sessions, runs, channel tasks, candidates and memories stayed empty.
A reopened two-hour overlap reread all four messages with no added evidence or
topics. A real three-message cap retained a pending range and blocked an identical
retry; raising the limits completed that original range, added only its missing
message and produced a cited topic. Source content and identities remain outside
the repository.
The genuine saved-receipt native check (private validation evidence)
also passed against a private SQLite backup of actual platform receipts: a fresh
native session searched/read the full exact saved reply, attribution and delivery
status without altering the copied database.

The live fourth-turn report (private validation evidence)
verifies a genuine owner recall after `/new` and a service restart. Both actual
MCP calls recovered the original reply and its `accepted` delivery status, and
excluded the current request. DWS independently returned all four sent requests
and the three completed bot replies; genuine reply IDs were reread through
`+messages-mget`. Production was restored with a fresh Stream connection and all
8,962 original rows in 28 non-internal tables preserved. Production DWS collection
remains unconfigured.

The original platform sequence (private validation evidence)
was interrupted by an immediate send-status read returning
`PARAM_ERROR / 消息不存在`. A later read of the same genuine task confirmed the
third send's success, but no Host receipt was recorded during receiver switching.
It was not replayed. The remaining fourth approved message ran in the original
isolated state; no replacement receipts were manufactured. The experiment
runner now retries only that exact observed lookup error for at most 45 seconds,
without resending. This bounded follow-up proves fresh-session history recall
after reset/restart; compatible native continuation across a live restart still
needs its own complete platform sequence. Synthetic continuation remains
separate evidence.

Sustained polling, group noise quality, account-wide coverage, group history
platform acceptance and compatible native continuation across a live restart
remain rollout checks.
