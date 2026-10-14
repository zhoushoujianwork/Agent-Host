# Shared engineering records and workspaces

Status: proposed; no shared development catalog, clone service or record exporter
is implemented. This extends the [development workflow](development-workflow.md).
See [implementation details and record examples](development-projects-detail.md).

## Decision

Keep project records and development progress in **one shared development
workspace per local user**. Keep each Agent's conversation and per-run context
in its existing runtime. An Agent takes responsibility for a task; the project
and its progress survive a change of Agent or channel.

The default development workspace is `~/workspace/development/`, configurable
through the proposed `config/development.json` in the
[unified configuration layout](configuration-layout.md). Existing repositories
under `~/github`, `~/gitlab` or an explicitly
selected directory stay where they are. New, explicitly requested clones go to
the shared workspace's `repositories/` directory. Feature work always uses a
task worktree.

## Where things live

```text
~/github/Agent-Host/                         # Existing checkout: stays here
~/gitlab/team/service/                      # Another existing checkout

~/workspace/development/
├── state.db                                # Shared project/task state
├── repositories/<host>/<namespace>/<repo>/  # Only Host-created clones
├── projects/<project-id>/
│   ├── project.json                        # Project record snapshot
│   ├── PROJECT.md                          # Readable project/progress overview
│   ├── tasks/<task-id>/
│   │   ├── task.json                       # Feature/task record snapshot
│   │   ├── TASK.md                         # Goal, progress, checks and PR
│   │   └── artifacts/                      # Retained diffs and check evidence
│   └── worktrees/<task-id>/                 # Actual feature development
└── locks/                                  # Cross-runtime writer/repository locks
```

GitLab namespaces can contain nested groups. Provider hostname and namespace
keep similarly named repositories apart. Existing source checkouts and newly
cloned source checkouts have the same role: starting points for task worktrees.

| Information | Owner and location |
| --- | --- |
| Repository path, identity, aliases and development policy | Shared project record |
| Feature goals, progress, commits, checks and PR | Shared task record |
| Work in progress and task files | Task worktree |
| Agent role, private conversation and native session | Existing Agent/channel runtime |
| This run's instructions, memory and task binding | Existing context cache |
| Learned knowledge | Existing memory domains and access rules |

The database maintains current operational state. JSON and Markdown files are
Host-generated views for inspection, backup and handoff; edits to a view do not
silently change a task. Records stay outside the user's source repository.

## Finding and preparing a project

1. Match a registered project or the current task first.
2. If needed, inspect bounded candidates under configured roots such as
   `~/github` and `~/gitlab`, or a directory the user explicitly selected.
3. Reuse a uniquely identified existing checkout. A clear development request
   can authorize binding that checkout under the configured local policy;
   discovery alone does not activate every repository. Ambiguity needs one
   focused question.
4. Clone only when the request identifies a remote and authorizes cloning.
   Use `repositories/<host>/<namespace>/<repo>/` by default, then bind the
   verified checkout. Never clone automatically because a local path is missing.
5. Create the feature worktree and bind the selected Agent's run to that task.

For example, using an existing `~/github/Agent-Host` creates a shared
project record pointing to that path. Its code is not copied into an Agent
workspace. A requested clone of `github.com/example/api` lands at
`~/workspace/development/repositories/github.com/example/api/`.

## Sharing and progress

Authorized Agents serving the same owner can inspect the same engineering
progress. Each task has one writer; other Agents can inspect or take over after
the writer stops. A handoff passes the goal, current diff/commit, check results
and PR status. It starts a compatible session for the receiving Agent without
moving the prior Agent's native conversation.

Shared storage does not make private conversations or memory public. Keep raw
messages in their original channel state and link their evidence. Project/task
views obey audience access; owner-private progress is not exposed to groups.

Moving a repository updates its path binding, not its project identity or task
history. Worktree cleanup never deletes an external checkout, the source clone
or the project's engineering records.

## Implementation order

1. Add shared project/task state and generated record files, preserving existing
   project IDs, channel history and memory scopes.
2. Bind selected existing repositories; add explicit clone preparation and
   default-directory handling.
3. Connect task worktrees, Agent handoffs, status views and PR receipts to the
   same records. Validate reuse across CLI and two channel/Agent runtimes.

Local lookup and explicit clone onboarding are included in this revised first
scope. GitHub PR delivery remains first; GitLab records do not claim an implemented
GitLab merge provider. Broad organization discovery and multi-machine shared
workspaces remain later work.
