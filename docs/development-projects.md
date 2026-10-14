# Runtime-managed engineering projects

Status: proposed; dynamic project enrollment, managed preparation and shared
engineering records are not implemented. This extends the
[development workflow](development-workflow.md). See the
[implementation notes and record examples](development-projects-detail.md).

## Decision and usage

A development request brings its project into Host management automatically.
Users do not enable a development component, edit project configuration or
register a repository before asking for work. Runtime locates the project,
records its directory and task, and retains progress for later requests.

Prefer an existing local checkout, including projects under `~/github`,
`~/gitlab`, the current request's directory or previously observed locations.
Keep those repositories in place. If preparation needs a new clone or the user
requests a new project, use the current Host's hidden `.agent-host/projects/`
area. All entries of that Host use the same registry; Agents keep their own
conversations and receive references to the shared engineering task.

## Where things live

```text
~/github/Agent-Host/                    # Existing checkout: stays here
~/gitlab/team/service/                 # Another existing checkout

.agent-host/                           # Current Host runtime root
├── config/                            # Existing user-maintained configuration
├── projects/                          # Generated engineering state
│   ├── state.db                       # Shared project/task authority
│   ├── locks/                         # Repository and task writer locks
│   └── <project-id>/
│       ├── project.json                # Generated project record
│       ├── PROJECT.md                  # Readable project/progress overview
│       ├── repository/                 # Only for a Host-created clone/new project
│       ├── tasks/<task-id>/
│       │   ├── task.json               # Generated feature/task record
│       │   ├── TASK.md                 # Goal, progress, checks and PR
│       │   └── artifacts/             # Retained diffs and check evidence
│       └── worktrees/<task-id>/        # Actual feature development
└── channels/<channel>/                # Original evidence, sessions and context
```

For an external checkout, `project.json` points to its original absolute path;
there is no duplicate `repository/`. Host-created code, task worktrees and
engineering records are retained runtime material. Although their location is
hidden, they are not disposable context cache.

## Finding and preparing a project

1. Resolve the current request's target and any clearly referenced active task.
2. Check known project paths and inspect bounded local candidates. Local lookup
   comes before preparing another clone; directory discovery does not enroll
   every repository it finds.
3. Automatically create or update the project record once its identity is clear,
   retaining the originating request and observed path. No enable step is needed.
4. When no local checkout exists, prepare the identified remote repository as
   needed for the authorized development request, or initialize a local project
   when the user requests a new one. Ask only if the target/source is missing or
   ambiguous; never invent a remote or replace a missing known project silently.
5. Bind a task worktree and run the selected Agent. Save progress, checks,
   commits and PR receipts against the same project/task.

“Fix export in Agent-Host” can therefore find `~/github/Agent-Host`, record it
automatically and start in a task worktree. A later request through another entry
can reuse that record. A new clone lives under
`.agent-host/projects/<project-id>/repository/`, with its verified remote recorded.

## Records and sharing

| Information | Location |
| --- | --- |
| Project identity, observed directory, source/remote and request origin | Shared project record |
| Goals, progress, commits, checks, blockers and PR | Shared task record |
| Current code changes | Task worktree |
| Agent role, private conversation and native session | Existing Agent/channel runtime |
| This run's instructions, memory and task binding | Existing context cache |

Runtime updates the operational database and generates JSON/Markdown views.
These files make progress inspectable; editing a view does not authorize work or
change its phase. They are not a second set of startup configuration.

Authorized Agents can take over the same task after its writer stops. Each
worktree has one writer. Engineering handoff includes the goal, current changes,
checks and PR; native conversation history and private memory remain separate.
Every existing task entry uses this same mechanism with its current identity,
audience and tool permissions. The originating audience still controls what
progress or evidence can be disclosed.

Project identity survives a directory move or Agent handoff. Worktree cleanup
preserves external checkouts, Host-created source repositories and task records.
Dynamic enrollment does not itself authorize remote publication or merge.

## Implementation order

1. Add the Host-level runtime registry and generated record files; reuse existing
   project IDs/history when their identity is verified.
2. Connect development intent to local lookup and automatic enrollment; prepare
   needed clones or explicitly requested new projects under the hidden Host root.
3. Connect worktrees, status, handoffs and PR receipts to that registry across
   existing entries. Validate shared progress without shared native sessions.

The [configuration layout](configuration-layout.md) documents this generated
runtime area. No `development.json` or manual enablement is proposed. GitHub PR
delivery remains first; GitLab paths and records do not imply a delivered GitLab
merge provider. Broad remote-account discovery and multi-machine state remain later work.
