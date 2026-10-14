# Project development workflow: implementation notes

Status: bounded local checkout discovery, selected-project enrollment and shared
execution receipts are implemented. Managed feature tasks, worktree ownership
and PR/MR delivery remain proposed. The [main proposal](development-workflow.md)
owns scope and user-facing decisions; these notes keep future delivery boundaries
separate from the current [registry implementation](development-projects-detail.md).

## Current foundation and missing behavior

Current source checks on 2026-10-14 establish these extension points:

| Foundation | Current boundary | Proposed extension |
| --- | --- | --- |
| `internal/routing/router.go`, `Decision` | Project selection over registered and bounded local candidates, reasons, evidence and source-linked continuation. | Independently identify development action and continuation of a development task. |
| `internal/state/types.go`, `Project`; `internal/projectruntime` | Automatic enrollment, shared project identity, execution receipts and entry-local compatibility mirrors. | Durable feature tasks and observed delivery requirements. |
| `internal/assembly/runtime.go`, `Runtime` | Separates project memory identity from cwd; fingerprints execution configuration. | Substitute task worktree as execution cwd while retaining project identity/scope. |
| `internal/channel/project.go`, `routedSession` | Binds conversation to project set, cwd, configuration and access set. | Add development-task/worktree identity to compatible native sessions. |
| `internal/host/host.go`, native invocation | Starts the runner with the session's saved cwd. | Prepare the managed workspace before any editing invocation. |
| `internal/state/store.go`, run completion | Separates native execution, acceptance and memory outcomes. | Keep development delivery status outside individual run completion. |

`internal/assembly/native_settings.go` can already resolve shared Git metadata
for an existing linked worktree. It does not manage worktree lifecycle. Bounded
source inspection found no Host worktree manager, commit gate, PR provider or
merge controller. Native tools can invoke Git today, without a Host-owned
delivery record. `make check` is this repository's contributor check; target
projects need their own resolved validation command.

## Intent and project binding

Use a distinct development-action assessment alongside existing project routing:
`discuss`, `inspect`, `implement`, `revise`, or `merge`, with original message
references and the proposed deliverable. These are proposed internal values,
not commands users must learn. Do not infer implementation from an engineering
topic, nor publication from implementation.

The initial assessor can use the existing tool-free Jev decision path. Assess
actions even when project routing uses its exact-reference shortcut; a project
name alone says nothing about editing. Empty-catalog/default routing must still
handle conversation normally. Native inspection may resolve missing business
details and propose a structured development handoff; the Host validates its
project identity and authorization before starting an editing attempt. Provider
failure stays explicit; an unvalidated decision cannot start development.

For a resolved implementation request, persist a compact feature description,
acceptance conditions and the evidence identifying its project. Clear requests
proceed with a short acknowledgment; require clarification only when the target,
scope or authority is material and missing. Implementation planning happens
inside the native harness; no mandatory additional planning agent is needed.

P0 changes one bound Git repository. The [shared engineering records](development-projects.md)
already provide request-driven local lookup, automatic selected-target enrollment
and source-linked execution receipts. Needed clones and explicitly requested new
projects remain proposed. Routing can select a target for a read-only request;
directory discovery alone does not enroll projects or authorize editing.
Activation still needs that binding before editing. Do not clone an inferred
remote, switch repositories or execute an uncertain write in the default
workspace. Current general routing behavior remains delivered behavior; this
new gate is planned for managed development.

## A development task spans multiple runs

Add a development coordinator inside the Host, with a small internal Git service
and one GitHub provider. A first implementation can keep these together under
`internal/development`, reusing `internal/state`, routing and assembly. Split
packages only when ownership or another actual provider requires it.

Persist a development task linking:

- Original request, acceptance conditions, stable project, requester and audience.
- Canonical repository/common Git directory, configured remote identity, base
  branch/base SHA, branch name, owned worktree and current candidate commit.
- Native attempts, validation and diff-review receipts, publication operations
  and the exact PR identity/head revision.
- Current phase, blocker, task revision, writer ownership and applicable grants.

Suggested phases are `preparing`, `implementing`, `validating`, `local_ready`,
`review`, `merging`, `merged`, and terminal `canceled`/`closed_unmerged`.
Record waiting input, permissions, failed checks and unknown remote outcomes
as blockers without erasing the phase or previous evidence. Native `Run`
status, feature acceptance, merge status and memory status remain separate.

Use source-linked task references for follow-ups, checked against requester,
Agent, audience, project and active lifecycle. An ambiguous reference to two
features requires clarification. A writer lease serializes attempts across
conversations and Host processes; different worktrees may run concurrently.
Do not expire a lease and start another writer until the old process has been
reconciled. `/new` resets the conversation, not the task, branch or PR. A fresh
native session can receive the task's cited requirements and verified evidence.

## Worktree preparation and lifecycle

Worktrees are durable working material, not rebuildable context cache. The
[engineering record layout](development-projects.md) selects the configurable
Host-owned `.agent-host/projects/`, with worktrees under
`<project-id>/worktrees/<task-id>/`. Shared development state owns task
progress; channel state retains original evidence and task references. Channels
resolving the same common Git directory share repository locking. Task
context/Skill snapshots stay under the existing cache with their own retention.

Preparation should establish these invariants:

1. Canonicalize the repository, Git common directory and proposed worktree path.
   Verify the request-derived project binding and selected provider/remote. A remote
   name such as `origin` or a basename alone is not repository identity.
2. Fetch the resolved base ref and pin its SHA. Failure to fetch cannot silently
   select a stale base. An explicitly requested local base may be used with its
   provenance recorded. Do not copy the original checkout's uncommitted changes
   unless the user specifically asks to include them.
3. Under a repository lock, reserve the task's branch/path and create its worktree.
   Use an argument-vector Git invocation, with no interpolated shell script.
   The default branch prefix is `codex/`; include a task identifier for uniqueness.
4. Verify Git's worktree listing, repository identity, branch and recorded base
   before marking preparation complete. Recover an existing task-owned worktree
   only after these identities match. Never adopt an unrelated existing path.
5. Assemble the native runtime using the worktree cwd and stable project memory
   scope. Include task/worktree/branch identity and policy in session compatibility.
   Changing the base through conflict repair records a new revision; ordinary
   edits within the same task do not force a fresh native conversation.

Original checkouts are not extra writable project directories for this run.
Load repository instructions from the task checkout and retain their source;
instructions and recalled Skills cannot waive the Host's delivery gates.
Dependencies and temporary services belong to the task environment. Do not copy
ignored credential/state trees or share a mutable development database by default.
Repositories needing unsupported submodules or environment setup fail preparation
with a concrete explanation rather than using the original checkout for writes.

Worktrees share Git objects, refs and much configuration. Serialize Host ref,
worktree and publication mutations by canonical common Git directory. A worktree
does not isolate operating-system access. See [Git's worktree documentation](https://git-scm.com/docs/git-worktree).

The Host enforces preparation and lifecycle for operations it executes. Prompts
and cwd alone cannot prevent native shell tools from editing elsewhere or pushing.
Managed-mode adapter acceptance must demonstrate the configured write/publication
boundaries using native permission or environment controls. Unrestricted channel
`bypassPermissions` is not sufficient evidence. If a profile cannot enforce the
required controls, stop managed editing for that profile and expose the
limitation. Do not promise a sandbox based only on preflight/postflight checks.
Choose and validate one workable Claude profile before building more adapters.

## Validation, commits and PR delivery

The native harness edits and runs useful checks. The Host owns readiness: review
the actual diff, require the resolved project checks, stage only task-owned changes and
create a local commit. Repository-required hooks remain enabled. Native text and
structured results are proposals; Git, command and provider receipts establish
the facts. An empty diff produces no empty feature commit or PR.

Bind check evidence to the precise tested tree and execution configuration,
including relevant untracked inputs. After staging/committing, verify that the
candidate commit represents that tested tree; a hook or another edit invalidates
the claim when it changes relevant content. Record command, exit status, revision
and evidence location. Later edits, conflict repair or environment changes
invalidate local readiness as applicable. Never label an unrun/failed check as
passed; an explicit project exception remains visible to reviewers.

Publication is a Host-owned operation: push the task branch to its configured
destination, then create/reconcile one draft PR with correct base/head identities.
The title/body explain the delivered behavior, acceptance conditions and verified
checks. Move to ready-for-review only after local validation and required remote
checks meet policy. Capture PR URL, provider identity and observed head SHA.
Requested code changes return to the same worktree and PR. Read-only PR review
does not silently adopt a branch for editing.

Use Host credentials for publication; do not inject publisher credentials into
the coding prompt. Credential separation alone does not prove restriction if
ambient helpers or native tools retain remote access; include that in adapter
acceptance. First support the configured GitHub repository, including enterprise
hostname when configured. An unsupported provider fails explicitly; GitLab is a
later adapter, with no public plugin API prerequisite.

## Authorization and merge

Grants originate in authenticated user requests or existing trusted authority, scoped
to requester, project, operation and task. Retrieved source content, PR comments,
memory and successful tests are not grants. Existing task entries retain their
authenticated requester/audience model; dynamic enrollment introduces no new
group-member roles or private-history grants.

| Operation | P0 authorization |
| --- | --- |
| Implement, check, local commit | Clear development request for the bound project. |
| Push and create/update a PR | Explicit task request or applicable standing user authorization. |
| Merge | Explicit instruction for the concrete PR/current head; no default automatic merge. |

Preserve the source and applicability of grants across restarts and compatible
follow-ups. Before asking for publication or merge authority, finish the local
work and provide the concrete diff/checks or PR being authorized. A new candidate
head after an explicit merge instruction requires renewed head-specific approval
in P0. Later automatic-merge support may define different standing grants.

Before merge, reconcile PR owner/repository, base, head, review requirements,
checks and conflicts. Match the authorized head in the provider operation and
follow protected-branch/queue rules without administrative bypass. GitHub CLI
supports head matching; queuing or enabling auto-merge does not prove a merge.
See [GitHub CLI merge options](https://cli.github.com/manual/gh_pr_merge) and
[required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks).

Conflict repair merges the updated target into the owned task branch, preserving
history by default, then reruns checks and refreshes review. Do not reset or
force-push somebody else's branch. If repository policy requires a different
history strategy, support it explicitly rather than rewriting implicitly.
Once a merge is observed, record the provider's merge result and fetch/verify its
target-branch reachability; squash/rebase may produce different commit IDs from
the task head. Do not switch, reset or pull over the user's original dirty checkout.
Merge confirmation is not deployment or runtime acceptance.

## Recovery, cleanup and acceptance

Record an operation intent before worktree creation, push, PR creation or merge,
then save observed receipts. Recovery first reconciles Git and provider reality
using the persisted task/branch/remote/PR identity. A timeout is an unknown
outcome, not permission to create a duplicate PR, repeat merge or rerun coding.
Query-only retries can clarify reality; mutating retries need a reconciled result.
External branch/head changes invalidate relevant checks and approvals.

Keep failed, canceled, rejected and closed-unmerged worktrees by default. Stop
and confirm the writer has ended before cleanup. Remove only task-owned worktrees
with no unaccounted tracked, untracked or needed ignored files, after commits and
receipts are durable and no process needs the directory. Retain commits/branch
references needed for recovery; delete remote branches only under an applicable
cleanup policy. Never force-remove a dirty worktree as routine closeout.

Initial acceptance focuses on the following outcomes, not an exhaustive matrix:

| Scenario | Required observation |
| --- | --- |
| Discussion, investigation or ambiguous target | No managed editing attempt or publication starts. |
| Clear development target with no prior record | Runtime enrolls it without enablement, preferring an existing local checkout. |
| Clear feature; original checkout has unrelated edits | Implementation/commit stay in its owned worktree; original edits survive. |
| Two features and a follow-up | Separate task worktrees; one writer per tree; follow-up uses its original task/PR and memory scope. |
| Check failure, hook rewrite or changed head | Readiness/approval cannot rely on stale evidence. |
| Restart or uncertain remote operation | State reconciles without duplicate PR or silent native replay. |
| Authorized end-to-end feature | Correct PR, required review/checks, matched head, verified merge and safe retained/cleaned work. |

Use temporary local repositories and a controlled provider double first, plus
native-profile enforcement checks. Then exercise one real authorized GitHub
test repository through chat/CLI. Automated checks cannot establish live provider
or channel acceptance. This proposal records no implementation or live workflow
acceptance yet.
