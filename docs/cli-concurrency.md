# Concurrent CLI tasks

Independent CLI and Web Chat conversations can execute at the same time using
the configured CLI state directory. They keep separate native sessions and run
records while sharing authorized memory and configuration. No alternate state
directory is needed.

One conversation still has one execution owner. A simultaneous attempt to resume
that same conversation is rejected before native execution. Follow-up messages
continue after its current turn finishes; failed or interrupted work is never
automatically replayed.

Maintenance, schema/data migrations and channel services retain exclusive
ownership of their own state directories. An older CLI binary also retains its
exclusive lock: finish that process before using the new concurrent path. Do not
delete lock files to force a transition.

The change keeps the existing database schema and data. Rollback requires ending
the new CLI processes, then starting the previous binary normally.

[Implementation and verification](cli-concurrency-detail.md).
