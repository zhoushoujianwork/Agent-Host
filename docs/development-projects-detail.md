# Runtime engineering records: implementation notes

Status: proposed. The [main document](development-projects.md) owns scope and
defaults. Linked examples are illustrative generated records, not startup
configuration. No user repository or runtime state is changed by this design.

## Host root and lifecycle

`ROOT` is the current Host's already resolved runtime root, normally the managed
`.agent-host/` directory. Derive `ROOT/projects` from that identity; do not
introduce a separate development root or `config/development.json` switch.
Supervisor/launcher context passes the same root to each entry and worker. A
native task's cwd cannot change it. A standalone CLI belongs to its resolved Host
instance; a different Host root is a different catalog, not an implicit merge.

Development requests lazily create the registry and necessary project state.
Ordinary greetings, discussion and read-only review do not create a development
task or enroll every observed directory. Startup can inspect/reconcile existing
records without scanning the user's disk, cloning repositories or replaying work.
P0 shares local storage within one Host instance and OS user; it does not promise
network-filesystem or multi-machine writers.

Existing configuration still owns models, native profiles, credentials and entry
identity. Engineering paths, progress and observed repository facts are runtime
records. Repository rules and the current request determine the target branch
and required checks; retain their source instead of requiring a prefilled policy
file. A missing branch/check decision is preparation work, not a passed check.
Publication and merge grants retain their original authenticated source.

## Paths and ownership

The registry owns `ROOT/projects/state.db` and `ROOT/projects/locks/`.
Each stable project ID owns a record directory at `ROOT/projects/<project-id>/`:

- `external`: record the existing checkout's canonical absolute path; no source
  copy is created under the project record directory.
- `managed_clone`: Runtime prepares the identified remote under `repository/`
  when needed for the development request.
- `managed_new`: Runtime initializes a local source repository under
  `repository/` for an explicit new-project request.

Task worktrees are siblings at `<project-id>/worktrees/<task-id>/`. A new source
repository must reach a valid worktree starting state before feature execution;
retain its initialization evidence and keep substantive edits in the task
worktree. Lack of a remote for a new project blocks PR publication until a real
destination is supplied; it does not authorize creating a hosting account/repository.

Hidden Host storage is durable engineering material, not ordinary cache. Source
repositories and unmerged work cannot be discarded by context-cache cleanup.
The Host runtime area remains private and excluded from source publication;
it may live beneath the Host's own checkout without altering tracked source.
Preserve unrelated edits in external repositories. Do not blanket-copy ignored
credentials, databases or other runtime files into task worktrees.

## Records and authority

The shared database owns the dynamic catalog, tasks, preparation/publication
operations, grants and writer ownership. Channel databases retain original
messages, native attempts and their existing memory boundaries. No local channel
catalog or generated file becomes a second writable development authority.

| Record | Essential contents |
| --- | --- |
| `project.json` | Stable ID/aliases, canonical checkout and ownership, observed Git/remote identity, enrollment origin, memory references and derived delivery requirements. |
| `PROJECT.md` | Source location, active tasks, blockers and latest outcomes. |
| `task.json` | Goal/acceptance conditions, origin Agent/entry/audience, worktree/branch/base/head, writer, phase, check receipts, PR and source/grant references. |
| `TASK.md` | Current goal, completed/remaining work, blocker, checks and next action. |
| `tasks/<task-id>/artifacts/` | Referenced immutable diffs, command/check logs and provider receipts. |
| Existing run `manifest.json` | Host-root identity, project/task IDs and revision, worktree, Agent and audience. |

Examples: [project record](examples/development-projects/project.example.json),
[task record](examples/development-projects/task.example.json),
[project overview](examples/development-projects/PROJECT.example.md) and
[task overview](examples/development-projects/TASK.example.md). They contain no
real execution, successful check or PR receipt. There is no enablement sample.

JSON views carry schema version, revision and export time. Export by atomic
replacement after committing state; compare stale views with database revisions
and regenerate them. Markdown summaries cannot establish readiness or merge.
Direct file edits do not grant permission. Full recovery needs a consistent
database backup, referenced artifacts and retained Git material, not just views.

## Request-driven lookup and enrollment

The current request's explicit target wins over old task context. Reuse an active
task only for a compatible follow-up. First check runtime-known paths and trusted
explicit locations; then perform bounded local lookup using current directory
context, prior path observations and common `~/github`/`~/gitlab` locations when
existing native access permits. These are lookup hints, not newly granted roots.

Inspect Git identity and short project descriptions, not all source contents.
Deduplicate verified common Git directories and recognize linked worktrees.
Host-managed repositories already present under `ROOT/projects` also count as
local candidates. Do not create another clone solely because an alias was missed.
Several candidates require a focused target question before editing.

Once the target is resolved, the Host transaction creates/updates its record and
links the development request. This is a task-triggered runtime observation;
there is no `project enable`, per-project enable flag or component-config step.
Inventory without a development request does not enroll all candidates. Existing
authenticated entry/audience/tool permissions still apply to actual operations.

Stable project ID is independent of path, name and Agent. Preserve prior IDs and
memory associations when adopting the referenced existing record; allocate a
new ID for a new binding. Multiple unrelated clones/remotes remain candidates
until a primary source is clear. Repository evidence includes canonical common
Git directory plus sanitized host/namespace/remote identity. Handle equivalent
transports without erasing host/port or case-sensitive paths. Never export tokens
or raw credential-helper output.

A first request with no checkout can prepare a clone when the remote identity
is known and preparation is necessary for its authorized development work; the
user need not separately say “enable” or “clone.” Record the operation before
preparing into an owned staging path, verify its Git identity, then install it at
`<project-id>/repository/`. Reconcile collisions/interruption; never overwrite
an unrelated directory or retry an unknown mutation blindly. If the user wants
a new project, record `managed_new` and initialize that source location instead.
A missing remote for an existing project is not permission to invent an empty
replacement. Report missing known paths and unresolved identities explicitly.

Catalog/preparation support remains separate from PR-provider support. GitLab
paths may be identified and recorded; requested full delivery cannot silently
downgrade to local edits when its PR provider is unavailable.

## Entries, sharing and handoff

Existing CLI and channel entries use the same development coordinator whenever
their user requests project development. No per-entry development switch or new
`channels.development.json` is needed. Entries pass authenticated requester,
Agent, audience, Host identity and source references. The capability keeps the
current entry's authorization model; dynamic enrollment grants no new filesystem,
private-history or remote operation rights.

Generated full records are owner-local runtime files. Native runs receive only
their worktree and audience-appropriate task context/status tools, not the entire
`projects` tree. Project overviews and channel replies filter accessible tasks;
private-owner progress is not copied into public/group replies. Keep raw messages
and native session IDs in original channel state. Shared task facts do not merge
Agent memory domains or authorize access to prior private transcripts.

Repository/task locks under `ROOT/projects/locks` enforce one writer even across
different channel state homes. Writer transfer requires old-process reconciliation,
not lease expiry alone. A handoff stops the writer, records current diff/head and
task revision, then lets another Agent bind that worktree with its own compatible
native session. Deleted context can be rebuilt; missing work requires explicit
recovery rather than an invented clean slate.

## Compatibility and acceptance

Current `internal/cli/project.go` and `internal/state/project.go` persist projects
in the selected `state.Store`; `internal/cli/service_unified.go` assigns separate
channel state homes. Dynamic Host-wide enrollment is new behavior, not delivered
catalog functionality.

On a development request, adopt a referenced legacy binding after verifying its
identity and scope. Preserve IDs, evidence and memory associations; surface ID,
clone or policy conflicts instead of merging by name. Historical runs remain in
their stores. Do not require a bulk manual registration/migration step before
ordinary development; do not partially switch an active writer's authority.

Acceptance focuses on: no enable/config requirement; local checkout preferred
over clone; one durable enrollment across entries; owned clone/new-project
preparation under the hidden Host root; worktree-only feature edits; one writer;
audience-correct status; and restart recovery without duplicate enrollment, clone
or PR. Cleanup preserves source repositories and task history. Deleting external
checkouts or retained project records is separate explicit maintenance.
