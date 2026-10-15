# Runtime-managed engineering projects

Status: local checkout discovery, automatic enrollment and shared execution
receipts are implemented. Managed clones/new repositories, durable development
tasks, worktree ownership and PR/MR delivery remain proposed. See the
[implementation notes](development-projects-detail.md) and
[development workflow](development-workflow.md).

## Decision and usage

Ask for work in a project through the existing CLI or channel. Host first looks
for local Git checkouts, including `~/github`, `~/gitlab` and the current workspace.
Routing selects the target, then Runtime records it automatically. No enable
flag, manual project registration or `development.json` is required. A read-only
project question can enroll its selected target; discovery does not enroll every
candidate it finds.

Existing checkouts stay in place. For example, asking about POP authentication
can select `~/gitlab/pop`, record its location and link the execution to that
project. Original messages, responses, native conversations and memory keep
their Agent/audience boundaries.

## Where things live

```text
~/github/Agent-Host/                    # Existing checkout: stays here
~/gitlab/pop/                          # Existing checkout: stays here

.agent-host/                           # Current Host runtime root
├── config/                            # User-maintained configuration
├── projects/                          # Generated shared operational records
│   ├── state.db                       # Enrolled projects and run receipts
│   ├── registry.lock                  # Serializes registry updates and exports
│   └── <project-id>/
│       ├── project.json                # Directory, identity and enrollment origins
│       ├── PROJECT.md                  # Readable project overview
│       └── tasks/<run-id>/
│           ├── task.json               # Source-linked execution receipt
│           └── TASK.md                 # Execution/acceptance/memory status
├── cli-state/                         # Original CLI evidence and conversations
└── channels/<channel>/state/           # Original channel evidence
```

CLI and ordinary channel state homes under the same Host root share this
registry. An arbitrary custom state home remains an isolated runtime.
The [configuration layout](configuration-layout.md) describes the same area.

## What is shared

Runtime keeps project identity, checkout location, enrollment origins and
execution receipts in the shared database, then generates JSON/Markdown views.
The files under `tasks/<run-id>/` describe one execution attempt; they do not yet
represent a feature spanning several runs, writer ownership, commits, checks or
PR acceptance. Execution, acceptance and memory statuses remain separate.

Receipts identify the original state home, run, Agent and audience. They omit
original request/response text and model context. Another Agent or channel can
reuse the project record without gaining access to private conversation history.
Editing a generated view does not change authoritative state or authorize work.

Identity is stable across symlink aliases and linked worktrees sharing the same
canonical Git common-directory path. Existing registered IDs and legacy memory
scopes are preserved. Moving a repository can change its derived identity;
move reconciliation is not implemented. CLI requests can retain an existing
worktree as their execution directory.
Channel worktree bindings remain part of the proposed writer lifecycle.

## Next development increment

1. Persist a durable feature and its acceptance conditions across execution runs.
2. When no local checkout exists, prepare an identified clone or explicitly
   requested new repository under `projects/<project-id>/repository/`.
3. Bind one owned writer/worktree per feature under `worktrees/<task-id>/`, then
   validate, commit and retain progress without moving external checkouts.
4. Add authorized PR/MR publication, review, merge and safe cleanup.

These steps are proposed in the linked workflow. GitHub delivery is planned
first; finding local GitLab projects does not imply a delivered MR provider.
Enrollment does not grant new filesystem, private-history or publication rights.
