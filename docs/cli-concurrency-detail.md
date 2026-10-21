# Concurrent CLI task implementation

[User-facing behavior](cli-concurrency.md).

## Ownership and persistence

- CLI Chat holds a shared OS lock on the existing `host.lock` for its lifetime.
  Exclusive owners use the same inode, keeping old binaries, channel workers
  and maintenance commands incompatible with concurrent writes by construction.
- A separate `chat-init.lock` briefly serializes database opening and crash
  recovery. Waiting for initialization respects caller cancellation. Native
  execution and model calls run after releasing this lock.
- Hashed session lock files protect each native conversation. Explicit resume
  takes the session lock before checking resumability; fresh and renewed sessions
  take it before creating their first run. Locks remain held through memory and
  experience processing. The OS releases them when a process exits.
- CLI startup only marks a running record `unknown` after acquiring its session
  lock. Live owners retain their status and evidence. Orphaned work is recorded
  as uncertain, without native replay. Exclusive workers retain global recovery.
- Writable SQLite transactions reserve the writer before their first read
  (`_txlock=immediate`, existing five-second busy timeout). Only brief persistence
  transactions serialize. File memory retains its existing publication lock and
  evidence/manifest coordination across processes.

No database schema or stored identity changes are required. Read-only inspection
continues to use its existing path; `HostActive` detects shared owners as active.
Per-session lock files remain at stable paths and are not removed after release.

## Verification

Subprocess tests hold two independent native execution fixtures at a barrier.
Both must be running with distinct Host/native session IDs before either may
finish. A third attempt to resume an owned session must fail before native work.
Both original runs then finish with separate execution and memory evidence.

State tests cover shared ownership, exclusive maintenance and old-owner
exclusion, cancelable initialization, process-exit lock release, recovery of an
abandoned run, and preservation of a live peer. Full repository checks and a
bounded live CLI overlap check supplement these fixtures.
