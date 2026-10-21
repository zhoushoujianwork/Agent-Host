# Room conversations and concurrent execution

Implemented for Web Chat, DingTalk and Feishu through the Claude ACP and print
adapters. [Implementation details and verification](conversation-branches-detail.md).

## Goal and usage

The room entry identifies the conversation. Group members share that room's
history, with an authenticated sender attached to every message. Private chats
and different rooms stay separate. Host does not infer task boundaries from text.

Send another message while an earlier one is running. Each message has its own
execution and reply. Web supports four active messages per room and a Stop button
on each message; IM uses the configured worker limit and original reply targets.
Stopping one Web message leaves its peers running. New Chat starts a new room.

## Core approach

Each request gets an immutable snapshot of available, authorized room history.
Results completed after its arrival do not enter that snapshot. Later messages
can see completed sibling replies, with their original authors and message IDs.
Replies finishing out of order cannot replace each other or overwrite history.

Each execution owns a distinct native session. When a compatible completed
checkpoint exists, Host forks it internally; before the first checkpoint, requests
start independent empty native sessions. Parent sessions stay sealed. The native
engine owns its tool loop; Host supplies missing sibling context, without
claiming to merge native transcripts or replaying previous tools.

## Delivered scope

- Fixed the SQLite transaction-upgrade race between run updates and event writes.
- Enabled bounded same-room concurrency, including messages from the same person.
- Preserved authenticated sender IDs, available names, quotes and original replies.
- Kept Web copy/select text and added message-specific cancellation and durable
  request receipts. Refresh/restart retains the room; uncertain work is not replayed.

There is no task classifier, task selector or automatic task merge. Existing
Agent/project authorization still applies. Forking does not isolate simultaneous
edits to the same file or remote resource.
