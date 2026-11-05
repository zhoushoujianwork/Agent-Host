# Channel progress implementation

[User contract](channel-progress.md).

## Ownership and transport

Host mounts `channel_progress` only for a true persisted
`Source.ChannelMessageID` business run with tools enabled. A history-only source
cannot grant progress permission. Host explicitly enables the run in SQLite and
revokes it on cancellation and exit. The private `channel-progress-mcp` stdio
subprocess opens an existing schema-18 database with `mode=rw`, foreign keys,
a busy timeout, FULL synchronization and immediate transactions. It does not
migrate, recover, initialize file memory, acquire a Host lock, receive provider
credentials or use an HTTP/socket delivery bridge.

The run-bound launch arguments and fingerprint change each turn. The stable MCP
SessionKey covers conversation authority and runtime, preserving native
continuation while current arguments rebind the subprocess. Run authority uses
the stable Host session ID; a validated first-run transition from an Offload
pending native ID to its generated native ID does not invalidate the mount.
Each validation still requires matching persisted run/session native IDs. Extraction uses its
own tool-free native request and never inherits this mount. Offload retains
ownership of native execution and continuation. Managed catalogs include the
progress tool contract and guidance with the stable authority SessionKey, so
turn-specific run arguments do not change the Skill package identity.

## Reservation and dispatch

Schema 18 adds `channel_progress_runs` authorization and a `channel_progress`
outbox. Reservation transactions revalidate receipt provenance, active claimed
message, run, session, current conversation/session binding, partition, project
scope, runtime and the Host enablement record. A key lookup occurs before quota
and cooldown checks, so repeated keys preserve the first text and attempt.
Reservations serialize across processes; failed and unknown outcomes count.

A separate Channel polling worker runs even when reactions are disabled. It
atomically revalidates authority and commits `dispatch_started_at` before any
provider operation. Dispatch uses the original receipt's API reply target or
webhook and expiry directly, without `deliverChannelReply`. API sends derive a
separate stable UUID from the durable progress attempt so provider deduplication
cannot consume the final reply UUID. No progress code
updates final-reply, completion, acceptance or memory fields. Platform calls
have a 20-second timeout. MCP waits up to 25 seconds for a terminal outcome,
then returns pending honestly when delivery remains unresolved. Pending cannot
guarantee ordering relative to final delivery and must not trigger automatic
replay. Tests inject short wait durations. Final outcome persistence uses a fresh bounded context
so acknowledged results survive shutdown. Worker shutdown cancels and joins
all workers. A cancellation that precedes dispatch revalidation forbids a new
send; an already authorized in-flight operation can finish.

Only undispatched pending rows can be claimed. Restart recovery marks dispatched
pending rows `unknown` and obsolete undispatched rows `failed`, revoking old
run permissions. Neither unknown nor dispatched operations are replayed.
Provider errors are reduced to fixed delivery classifications in persisted
progress results; URLs, tokens and raw provider errors never cross MCP.

## Verification boundary

Isolated Go tests cover Unicode limits, cross-connection reservation and claim
atomicity, cooldown/quota/idempotency, stale provenance, session and runtime
bindings, migration/recovery, transport target binding, delivery outcomes,
final-reply independence, disabled reactions, cancellation/join, Host gating and
stable continuation with refreshed launch arguments. These tests do not claim
production rollout or live DingTalk/Feishu delivery acceptance.
