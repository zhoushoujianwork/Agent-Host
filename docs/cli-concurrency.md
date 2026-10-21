# Concurrent CLI tasks

Independent CLI conversations and room message executions can execute at the same time using
the configured CLI state directory. They keep separate native sessions and run
records while sharing authorized memory and configuration. No alternate state
directory is needed.

An explicitly resumed native session still has one execution owner. A simultaneous attempt to resume
that same conversation is rejected before native execution. Follow-up messages
continue after its current turn finishes; failed or interrupted work is never
automatically replayed.

Maintenance, schema/data migrations and channel services retain exclusive
ownership of their own state directories. An older CLI binary also retains its
exclusive lock: finish that process before using the new concurrent path. Do not
delete lock files to force a transition.

The original independent-CLI lock change preserves its schema. Room messages add
schema 16; use the room design’s upgrade/rollback procedure for that version.

[Implementation and verification](cli-concurrency-detail.md).

[Delivered room conversation concurrency](conversation-branches.md) routes by room
entry, attributes messages to authenticated users, and isolates overlapping
requests in native execution branches without inferring tasks.
