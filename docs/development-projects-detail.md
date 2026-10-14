# Engineering records: implementation details

Status: proposed. The [main document](development-projects.md) owns scope and
defaults. All linked examples are illustrative contracts, not configurations
recognized by the current binary. No user repositories or runtime data were moved.

## Roots and ownership

Introduce the optional Host-owned `config/development.json` described in the
[unified configuration contract](configuration-layout-detail.md#工程开发配置待实现).
Its [example](examples/development-projects/development.example.json) uses the
existing `version`, `enabled`, `access`, `settings` envelope and defines one
development root, selected lookup roots and default delivery policy.
The current loader does not recognize this filename; do not install the example
as live configuration before the planned loader extension is implemented.
It is read-only during execution. Resolve `~` using the local runtime owner's
home directory; resolve relative overrides against the configuration file,
canonicalize them and never invoke a shell to expand paths.
Unspecified base branches or empty validation-command defaults mean unconfigured,
not successful validation. Resolve the target branch during preparation and the
required project checks before readiness can be granted.

All participating CLI/channel runtimes must resolve the same canonical
`development_root` and workspace identity before sharing tasks. Another root
is another catalog, not a copy to merge automatically. P0 is local storage owned
by one OS user; do not use network filesystems or promise multi-machine writers.
Create private state directories/files with owner-only access.

Use the following ownership labels for source checkouts:

- `external`: an existing user checkout, regardless of its folder name.
- `managed_clone`: a checkout created by a recorded Host clone operation.

Discovery cannot change ownership. A pre-existing directory under the clone
root remains external unless its ownership receipt is verified. The registry
has one primary source checkout per project in P0; additional discovered clones
are candidates, not permission to switch the source path. Recognize an existing
linked worktree as an attachment to its repository rather than a new project.

Worktrees live at `projects/<project-id>/worktrees/<task-id>/`, outside both
external checkouts and `repositories/`. Clone/worktree paths are durable, not
cache. Reuse the existing `cache/workspaces/<run-id>/manifest.json` for an
Agent run's development binding; no second per-Agent task-progress authority.

## Record files and a single state authority

Keep the shared operational catalog, tasks, operations and writer leases in
`development_root/state.db`. This database is distinct from channel evidence
databases and from formal memory files. JSON/Markdown projections and artifact
files belong to this shared workspace; they are not learned memory.

| Record | Essential contents | Write responsibility |
| --- | --- | --- |
| `project.json` | Stable ID, aliases, checkout ownership/path, verified repository/remote, memory references, development policy and enabled scope. | Host snapshot exporter after a project transaction. |
| `PROJECT.md` | Repository location, current task list, blockers and latest delivery outcomes. | Generated from project and accessible task records. |
| `task.json` | Request summary, acceptance conditions, project, worktree/branch/base/head, writer, phase, checks, PR and source refs. | Host snapshot exporter after task transitions. |
| `TASK.md` | Readable goal, completed/remaining work, blocker, revision/check results and next action. | Generated from the task record. |
| `artifacts/` | Referenced immutable diffs, command/check logs and provider receipts. | Host evidence recorder; metadata indexed in state.db. |
| Existing run `manifest.json` | Development-root identity, project/task IDs, task revision, worktree and current Agent/audience. | Current runtime assembly. |

Every JSON projection has a schema version, record revision and export time.
Write snapshots by atomic replacement after committing the database transaction.
A crash may leave an older snapshot: readers treat it as a view, compare revisions
against the Host and rebuild it. A Markdown summary is never the merge authority.
Do not maintain authoritative task status in JSON, Markdown and a channel DB
simultaneously. No independently appended `events.jsonl` is required in P0;
durable operation history is in the shared database.

Users update projects through explicit Host operations. Direct record-file edits
do not grant access or change phase/policy. A future explicit file import can
validate a proposed edit against its recorded revision; it is not implicit hot
reload. Snapshot-only backups are useful for inspection but are insufficient for
full recovery: retain a consistent database backup plus referenced artifacts,
source repositories and worktrees. Existing memory backups remain separate.

Concrete templates: [project snapshot](examples/development-projects/project.example.json),
[task snapshot](examples/development-projects/task.example.json),
[project overview](examples/development-projects/PROJECT.example.md) and
[task overview](examples/development-projects/TASK.example.md).
Examples have no real executions, commits, successful checks or PR receipts.

## Identity and repository lookup

Project identity is a stable catalog ID, not a directory basename, URL or Agent
name. Preserve existing IDs during an explicit import; allocate a fresh ID for
a genuinely new binding. Renaming an alias, moving a path or changing the
responsible Agent does not rename the project. Existing project memory scopes
remain unchanged and retain their Agent/audience partitions.

Repository evidence includes the canonical checkout/common Git directory and
the configured remote's host, namespace and repository. Normalize equivalent
SSH/HTTPS transport descriptions without discarding hostname, port or namespace.
Do not lowercase case-sensitive filesystem paths or equate unrelated servers.
Multiple remotes/forks require an explicit source and publication destination.
Exported records contain sanitized remote URLs and credential-profile references,
never embedded tokens, passwords or raw credential-helper output.

The current request's explicit target takes precedence over old task context.
Reuse an active task only for a compatible follow-up. Resolve exact path/project
references and registered aliases before bounded candidates within configured
roots. Lookup only inspects Git
identity and short local project descriptions. It does not recursively ingest
source, read private unrelated repositories or register all children. Respect
native filesystem access and configured scan limits. Missing roots are reported
as unavailable candidates; an already bound missing source is a blocker, not a
reason to switch to a clone with the same name.

When one candidate clearly matches a development request, create its binding
with the default local policy and acknowledge the chosen path. This registration
records the authorized target, not permission to publish. If the project is not
development-enabled, clarify/enable it through the trusted local policy path.
Ambiguous targets require clarification before any editing attempt.

For an explicit clone request, require a concrete remote identity, supported Git
transport and existing credential access. Clone into a task-owned staging path,
verify the resulting remote/repository, then move it into the reserved destination
and record ownership/binding. Keep an operation receipt so interruption can be
reconciled. An existing destination is inspected for an exact match or reported
as a collision; it is never overwritten, recloned or cleaned by inference.
Nested namespaces produce nested directories. Sanitize path components, resolve
symlinks and ensure derived clone paths remain inside the configured clone root.
No network credentials appear in record templates or prompts.

Catalog/clone support is separate from PR-provider support. Existing GitLab paths
can be recorded and inspected; full managed feature delivery waits for a supported
provider and policy. Do not silently downgrade a requested PR workflow to local
editing because the selected provider is unavailable.

## Shared progress, private execution

Shared project/task access is evaluated from authenticated owner, selected Agent,
audience and configured project grants. One OS user owns the workspace in P0;
shared storage is not a new cross-user access model. Group use remains outside
the first development workflow. Do not mount the entire development root into
every native run: supply only its worktree, authorized status tools and task
context. The coordinator, not each coding Agent, updates the records.

Keep raw channel messages, replies and native session IDs in their original
runtime. A task stores the minimum engineering summary suitable for its scope,
plus source references containing runtime/channel/message/run identity. Source
reads still require their original grants. A different Agent can receive allowed
task facts without obtaining the previous Agent's private transcript or learned
memory. Engineering facts are not automatically published as Host knowledge.

The shared database owns task phase, current candidate and intended writer.
Cross-process repository and task locks under `locks/` prevent two writers even
when requests originate in different state homes. Lock ownership and process
liveness must agree before transferring a writer; lease timeout alone does not
authorize a second process. Independent tasks use separate worktrees.

A handoff pauses/stops the old writer, verifies current worktree/diff/head and
saves a task revision before releasing ownership. The receiving runtime binds
that same task/worktree, with its own native session and scoped context. Progress
does not depend on its Agent workspace surviving. A deleted context cache can
be rebuilt; a lost worktree requires recovery, never an invented clean slate.

`/status` resolves the current conversation's task reference and returns an
authorized view. A future project overview can aggregate permitted tasks from
other Agents; it never dumps unrelated owner-private requests into group replies.

## Compatibility and delivery

Today project registration lives in a selected `state.Store`: `internal/cli/project.go`
opens the chosen `--home`, and `internal/state/project.go` persists its project
rows. `internal/cli/service_unified.go` selects each channel's separate state
directory. The proposed shared database is a new authority
for development bindings, not a claim that a global catalog already exists.
Existing runs and conversation references must remain readable in their stores.

Implement a controlled import from selected existing catalogs into the shared
registry. Preserve project IDs, path/scope associations and provenance; detect
conflicting IDs, different clones and policy differences rather than merging
them by name. Channel stores retain historical data and a mapping to the shared
project. Memory remains in its original stores/domains; sharing a project record
does not combine Agent memories. Once a binding adopts shared development state,
its local catalog copy is a compatibility view, not a second writable authority.
Record adoption/reconciliation explicitly; do not partially switch a running task.

P0 delivery order is shared records and import, selected-local-path binding,
explicit clone preparation, then worktree/native/PR integration. No runtime
migration is performed by this design change. Validate the following boundaries:

- Existing checkout with unrelated changes: unchanged path/content, external
  ownership, feature work only in its worktree.
- Clone identity/collision/interruption: correct destination and one owned clone;
  no overwrite or hidden retry.
- Two Agent/channel runtimes: one shared task revision and writer, different
  native sessions, unchanged memory boundaries.
- Stale view, restart or moved checkout: reconstruct records from authority;
  preserve task/PR identity and report unavailable worktrees explicitly.

Worktree cleanup preserves the source checkout and project/task history.
Repository deletion, external checkout removal and deletion of development
records are separate explicit maintenance operations, not merge side effects.
