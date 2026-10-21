# Room conversation concurrency details

[User-facing design and delivered scope](conversation-branches.md).

## Identity and persistence

IM derives its room from the existing authenticated channel/account/tenant/
conversation binding and Agent/audience partition. Group bindings exclude the
individual sender; private/group endpoints stay separate. Explicit forwarded-card
isolation remains in its existing binding. Project selection and authorization
remain unchanged; message topics never create task branches.

Web persists a random room ID per configured CLI catalog/home/Agent beneath
`HOME/web-chat/rooms/`. Multiple tabs of that viewer use the same room. New Chat
atomically replaces that room and clears its projection. CLI also accepts
`chat --room ID --request-id ID -m MESSAGE`; this local owner entry cannot accept
an arbitrary remote sender identity. `--resume` remains the explicit linear API
and cannot mutate a sealed room execution.

Schema 16 adds `rooms` (reset epoch) and `room_messages` (unique room/request,
unique execution session, optional run link). Old sessions/runs remain unchanged.
A short immediate transaction selects the checkpoint and stores `Session.Room`:
authenticated actor/message, receive time, fixed history, parent and inherited IDs.
Run creation atomically links the receipt once; duplicates cannot launch another
native turn. Channel run creation validates the room against the durable provider
receipt, then derives `Run.Source` from that receipt.

The Web bridge persists the accepted request before returning its ACK, records
Host run/session IDs when observed, and saves a bounded final reply. After viewer
restart, unfinished projections become unknown and are never automatically
replayed. Existing execution records retain full output. The bridge does not
claim ownership of a surviving CLI child after a restart.

## Checkpoints and context

Every room execution gets a fresh Host/native binding. The newest compatible
completed receipt in intake order is eligible as a parent, independent of finish
order. Parent sessions never receive another prompt. ACP resolves its opaque
native ID from the private binding, verifies identical runtime/capability identity,
calls `session/fork`, then resumes/loads the returned child before prompting.
It saves a distinct child binding and refuses uncertain existing destinations.
The print adapter supplies explicit parent resume, fork and child session flags.
Unsupported ACP fork/reconnect capability fails explicitly; no engine fallback.

New rooms with no completed checkpoint start empty sessions concurrently; there
is no artificial seed prompt or business-tool replay. Failed, canceled, running
and unknown executions cannot be checkpoint sources. Native completion timestamps
prevent a queued input from inheriting results completed after its receive time.

A snapshot contains at most 20 prior receipts in the same room and exact
Agent/audience/project/runtime authority. Requests/replies are bounded to
4,000/8,000 characters with explicit truncation flags. Public replies and request
status remain linked to original IDs. Entries already present unchanged in the
parent history are marked inherited and omitted from the new native prompt;
changed status and sibling replies are supplied with speaker labels. This is
context assembly, not a native transcript merge. Existing history tools remain
available within their existing authorization.

Current actor metadata also appears in trusted per-turn instructions; the durable
prompt envelope preserves speaker labels across future forks. DingTalk uses the
authenticated callback's `senderNick` when available. Feishu keeps its verified
sender ID when no verified display name is supplied. Claimed names in text cannot
replace platform identity. Quote authors and current senders remain distinct.

## Scheduling, cancellation and database locks

Fork-capable channel Hosts claim up to the existing configured capacity, keyed
by receipt ID rather than room. A slow model or reply does not block the same
room's next message. Legacy test/custom runners without fork capability retain
linear scheduling. Feishu meeting wrappers preserve both capability and lifetime
cancellation. Provider reply targets remain unchanged: group attribution/quotes
for DingTalk, original-message reply IDs for Feishu IM.

Web owns a child process and worker per message. The composer stays sendable
until the four-message capacity is full; per-message Stop signals only that CLI.
Shutdown joins every owned child. A canceled/failed sibling does not require
resetting the room or cancel peers. No execution is automatically retried.

The file-memory index connection attaches the operational database. It now uses
`_txlock=immediate`, matching the standalone writable connection. Reserving the
write transaction before reads prevents an independent append-only event writer
from invalidating a deferred WAL snapshot and causing `SQLITE_BUSY`. Publication
locks still protect memory receipts; they never span native model execution.

## Upgrade and rollback

Stop old writers before installing the schema-16 binary; keep a consistent state
backup. Migration only adds tables and optional JSON fields. It neither converts
old native sessions nor replays old requests. Old binaries reject newer schemas:
rollback requires stopping new writers and restoring the pre-upgrade backup,
rather than editing `user_version` or deleting lock files. Preserve the newer
state separately when investigating rollback. Runtime deployment is a separate,
explicit operation from building/testing the checkout.

## Verification

- Deterministic file-memory run-update/event-write competition; repeated independent
  CLI process overlap, including two processes sending to the same room.
- Host overlap by same/different senders; shared sealed parent, distinct children,
  fixed snapshots, late queued input, out-of-order completion, inherited context,
  duplicate rejection, room reset and private/group isolation.
- Channel worker overlap and replies linked to original message/sender; Web
  capacity, peer-safe Stop, durable ACK/restart receipts, room persistence,
  copy/select text and per-message controls.
- Real Claude ACP 0.81.2 acceptance: two children forked the same completed parent
  concurrently, returned different native IDs, preserved Alice/Bob labels and
  recalled a marker supplied only to the parent.
- Real Claude print acceptance: explicit fork preserved the parent-only marker
  and assigned child ID. Opt-in tests use isolated temporary native profiles and
  inherited provider credentials, never production conversation transcripts.

Live checks can be repeated with `AGH_TEST_ACP_CONFIG`/`AGH_TEST_MODEL` for ACP or
`AGH_TEST_CLAUDE_FORK`/`AGH_TEST_MODEL` for print. Normal checks skip provider calls.
