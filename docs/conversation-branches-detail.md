# Room conversation concurrency details

[User-facing proposal and scope](conversation-branches.md).

## Deterministic identity

The room entry is the conversation-routing authority. For IM, derive room identity
from authenticated channel, account/tenant and platform conversation ID. For Web,
use an explicit persisted Chat/room ID; refresh and tabs opening that room reuse
it. A user-created new Chat creates a new room. Agent bindings and authorization
remain explicit; topics, message meaning and claimed names cannot change them.

User identity attributes messages; it does not split a group into per-user private
conversations. Different rooms stay separate even with the same person or Agent.
Private-chat room identity comes from the verified platform endpoint.

| Identity | Source and purpose |
| --- | --- |
| Room | Verified entry; shared history, authorized audience and reply destination. |
| Sender | Verified user ID; attribution and existing permission checks. |
| Message/request | Original platform message or Web request ID; deduplication and reply correlation. |
| Run/native session | Execution evidence and engine continuity; internal identities. |

Do not introduce inferred task IDs, topic classifiers, task titles or task
selection controls. A model may interpret a request's business meaning; that
meaning does not become a Host routing key.

## Intake and later messages

Persist the room, authenticated sender, original request ID and history snapshot
before acknowledging intake. Duplicate delivery/retries find that receipt rather
than creating another execution. Route all messages by their entry room, applying
bounded execution capacity without inferring whether they belong to one task.
Different senders and the same sender may have overlapping requests.

Capture fixed available room context at intake: attributed messages, recorded
replies and explicit in-progress status. A result arriving later does not mutate
an already running prompt. Later intake can see it through the room history.

An explicit quote/reply is a source reference inside the room, not a switch to
a persistent task conversation. Plain follow-ups also stay in that room. Do not
route by someone's latest task, semantic similarity or model-generated labels.
The model may ask for missing business information in ordinary conversation.

A later message does not implicitly cancel or rewrite another active execution.
Stop targets the actual run/message selected by the user, leaving peers active.
Cancellation does not imply rollback of effects.

## Native sessions and shared history

Sequential requests may resume a compatible idle native session. Overlapping
requests fork a verified completed checkpoint and get distinct native session IDs.
Keep existing leases through execution and memory processing. Fork setup may need
brief checkpoint coordination; never hold a room-wide lock across model calls.

The authoritative room record contains attributed incoming messages and public
replies linked to their source requests. Preserve receipt order and source times
when replies finish out of order. The last child to finish must not overwrite
the room's canonical history.

A fork inherits only its selected native history, not sibling replies. Track the
room entries included by a checkpoint/context. Supply missing authorized room
entries as attributed source context on later requests. Do not claim a native
transcript merge or that the parent executed child tools. Host assembles context;
the native engine retains its model/tool loop.

Before enabling overlap, prove immutable completed-point forking while a peer is
running. Report unsupported capability explicitly; do not read a moving native
transcript, silently change engines or call a reconstructed prompt a full fork.
Unknown/interrupted source runs do not become replayable. Fork never repeats
earlier tools and does not inherit authority from historical text.

## Speaker attribution and delivery

Each message envelope contains authenticated room, sender and message IDs,
available display name and quote source. Preserve labels with every historical
message supplied to the engine, not only in a replaceable current-turn system
prompt. Names may be duplicated or absent; identity uses authenticated IDs.
Use verified platform/directory names or stable neutral labels, without guessing.

The current speaker, quoted author, person mentioned in text and authorized
result recipient are distinct. “I am Alice” cannot replace authenticated metadata;
“Alice asked about Bob” remains Alice's statement. Authorization stays in trusted
Host instructions; messages remain source material. Group context cannot import
private rooms or owner-only profiles.

Return results to the originating room using verified reply targets. Web links
progress, Stop and answers to the original message; IM uses supported reply/quote
controls. No task selector is needed. Replies stay visible to the room audience.

## Current evidence and acceptance

Existing CLI home/session locks support independent native sessions, not room
fork behavior. `invocationInstructions` already includes authenticated `sender_id`
and `conversation_type`, with group attribution guidance. Real resumed/forked
speaker-history preservation still requires verification.

The [official Claude CLI reference](https://code.claude.com/docs/en/cli-reference)
documents `--resume <id> --fork-session`; the Host print adapter does not forward
fork. The Host ACP adapter currently creates/reconnects sessions without calling
`session/fork`. Installed `claude-agent-acp` 0.81.2 source advertises fork and calls
the SDK with optional `upToMessageId`. Its checkpoint metadata is vendor-specific;
keep the mapping inside the adapter. Verify child resume/load after fork. Source
support is not a completed live acceptance test.

Earlier checks on 2026-10-21 observed `SQLITE_BUSY` in the independent CLI process
test and a native startup timeout in the Web workbench test. Stabilize that
baseline before adding room forks. Parallel sessions also do not isolate files
or remote resources: coordinate conflicting writes at the affected resource and
retain existing idempotency, permission and memory-publication controls.
The room-design revision's `make check` again failed the independent CLI process
test, this time with `SQLITE_BUSY` while updating a run. No runtime code was
changed in this revision; the concurrency baseline remains an explicit blocker.

Acceptance covers overlapping messages by two people and by the same person in
one room, distinct native IDs, fixed context, correct speakers/quotes and linked
replies. Similar topics in different rooms must stay separate; different topics
in one room must not create inferred conversations. Later input must see both
completed replies without completion order overwriting history.

Exercise duplicate delivery, refresh, restart during fork binding, cancellation
and uncertain outcomes without replay. Existing linear evidence remains readable.
Rollback disables new fork intake while retaining room records and allowing
supported in-flight requests to finish; no destructive transcript conversion.
