# Persistent engineering missions: research and implementation notes

Status: proposed, 2026-10-23. The [main proposal](engineering-missions.md) owns
scope and product decisions. These notes explain evidence and implementation
boundaries; names below are design sketches, not existing configuration or CLI.

## 1. Investigation scope and revisions

Read-only source and GitHub inspection; no target repository edits, workflow
runs, issue comments, labels, PRs, merges, service changes or EDA calls.

| Source | Observed revision / state |
| --- | --- |
| Agent-Host checkout | `4636da464bf3f461f66018976a63c10aa120aa3f`; pre-existing edits in workspace-layout and memory viewer files excluded from this work |
| easyeda-agent local `dev` | `46bd24cb0b67f7d123ebb4648555dcc08efc2e70`; clean when inspected |
| easyeda-agent remote `dev` | `024e62e75e6bed8bba35b6aaf8aa78a2dc95ea44` |
| easyeda-agent remote `main` | `7cde1ae1769d3e8c50fd9ada43f9bcc166d69d70` |
| ClawFlow local `main` | `dff005a8984c05e82af5aa4689f3650e734c44ae`; clean when inspected |
| ClawFlow remote `main` | `1b0219bbf3618b5f357b100290c27629964db4fe` |
| Installed ClawFlow | `v0.72.0-56-g20af7b0-dirty`; not assumed identical to either source revision |
| Configured ClawFlow project | `eda` contains easyeda-agent; Pilot disabled, 60-minute cooldown; repository execution also disabled |
| Live GitHub backlog | Six open issues: #284, #280, #273, #244, #55, #43; no open PRs in the queried repository |

Local source findings describe those commits, not deployed behavior. Remote
branch tips came from authenticated `gh api` reads; issue/PR/project observations
used ClawFlow. Repository web pages were also consulted, but their cached
descriptions/counts do not override the live queries. No full runtime acceptance
of either target system was performed.

### Agent-Host foundations

| Evidence | Finding and implication |
| --- | --- |
| [Architecture](architecture.md), [development workflow](development-workflow.md) | Claude ACP and explicit print compatibility exist; Codex execution remains a scaffold. Durable feature delivery is explicitly proposed. |
| `internal/projectruntime/registry.go:249` | `RunReceipt` records execution/acceptance/memory outcomes per run. The following source-channel override means the initial `Entry: "cli"` is not a CLI-only constraint. |
| `internal/state/store.go:400`, `:751` | Runs begin with unverified acceptance; interrupted runs are reconciled as unknown without automatic execution replay. This is not a feature acceptance controller. |
| `internal/taskfeedback/queue.go:203`, `:225`, `:260` | Claim/Finish/Recover provide a useful notification delivery pattern. They do not schedule engineering tasks. |
| [Project records](development-projects.md) | Shared registry and project identity already exist; `tasks/<run-id>` currently exports attempt receipts, not durable business tasks. |
| [Development detail](development-workflow-detail.md) | Worktree ownership, revision-bound checks, publication intents and head-specific merge are already designed. Extend this proposal rather than creating a competing lifecycle. |

Focused source searches and the architecture agree that a mission scheduler and
durable engineering coordinator are not delivered. Existing service polling,
backup timers and native session continuation are different responsibilities.
Do not equate a stored `AcceptanceStatus` field with implemented acceptance.

### ClawFlow: useful existing functionality and integration limits

Source references below are relative to the inspected ClawFlow checkout; the
[public repository](https://github.com/zhoushoujianwork/clawflow) provides context.

| Source | Observation |
| --- | --- |
| `cmd/clawflow/commands/run.go:480` | Unscoped runs wake Pilot after operators; repository/issue-scoped runs avoid that global Pilot phase. |
| `internal/project/project.go:28`, `internal/pilot/schedule.go:79` | Project automation has cooldown, machine binding and scheduling. |
| `internal/pilot/schedule.go:320` | Per-wake timeout, Claude budget and local metadata/event records are present. |
| `internal/pilot/budget/budget.go`, `client.go` | A per-wake write reservation mechanism exists for budget-aware VCS commands; this is not an OS boundary around arbitrary native tools. |
| `internal/chat/project_pilot.go:57` | Pilot performs backlog triage, consumes project context and recent wake summaries. It is already a project-management layer. |
| `skills/decompose/SKILL.md`, `skills/track-progress/SKILL.md` | Native sub-issue decomposition and tracking procedures exist. |
| `skills/implement/SKILL.md` | Implementation assumes a prepared worktree and directs commit, push and PR creation. Its `agent-implemented` outcome means a PR was opened, not business acceptance. |
| `internal/snapshot/lock.go:40`, `commands/run.go:701` | Current source uses local process lockfiles. README/skill references to `agent-running` as a universal execution lock are historical; labels are not a cross-system mutex. |
| `cmd/clawflow/commands/automerge.go` | Automatic merge and repeated-pass convergence exist; do not run this path under a mission whose grant stops at a draft PR. |
| `internal/vcs/interface.go:66`, `:174`; `internal/vcs/github/client.go:901` | The inspected PR DTO omits head SHA and the merge method has no expected-head argument. Its request sends `merge_method` only. Extend this boundary or use a documented GitHub adapter before promising head-matched merge. |

The installed CLI and local source differ, so negotiate actual command/JSON
capabilities at onboarding. Skills are reusable methods, not drop-in permission
contracts. Adapt the implementation Skill's push/PR steps to Host publication
ownership; do not mount the current autonomous operator unchanged into a
read-only or local-only mission.

Historical ClawFlow issues provide relevant incident classes, without proving
they remain defects in current code:

- [#325](https://github.com/zhoushoujianwork/clawflow/issues/325): orphaned Pilot
  subprocesses after watchdog exit; motivates process-tree reconciliation.
- [#320](https://github.com/zhoushoujianwork/clawflow/issues/320): billing-cap
  failures misclassified and paid work lost; motivates distinct stop reasons.
- [#113](https://github.com/zhoushoujianwork/clawflow/issues/113): duplicate
  multi-machine wakes; motivates explicit host ownership.

### easyeda-agent: product claims versus acceptance

Read the local README, `AGENTS.md`, `docs/cli-STATUS.md`,
`docs/e2e-automation-acceptance.md`, `docs/pcb-solver-community-design.md`,
Makefile and `.github/workflows/ci.yml` at the recorded local revision.

The README links broad user workflows to narrower evidence and limitations.
Some status pages preserve September/early October acceptance, while GitHub
contains later issue updates. Neither the latest code date nor an old status
table proves the current feature works. Reconcile by capability and tested
revision, not by blindly picking whichever document was modified last.

| Existing issue | Suitable managed work | Acceptance boundary |
| --- | --- | --- |
| [#273](https://github.com/zhoushoujianwork/easyeda-agent/issues/273) | First offline research/fix: reproduce two frozen schematic inputs, compare search changes | Preserve required direct connections and geometry checks; bounded exhaustion does not prove infeasibility |
| [#284](https://github.com/zhoushoujianwork/easyeda-agent/issues/284) | Later API feasibility probe, typed actions and export tests | Issue claims about beta APIs require checking current official declarations and a real supported host |
| [#280](https://github.com/zhoushoujianwork/easyeda-agent/issues/280) | Investigate overlapping PCB net-name display | Distinguish display density, duplicate copper and electrical errors; screenshots alone cannot decide |
| [#244](https://github.com/zhoushoujianwork/easyeda-agent/issues/244) | Investigate block-instance pin/net resolution | Reported environment is V3, while current mainline targets V4; establish compatibility scope before implementation |
| [#55](https://github.com/zhoushoujianwork/easyeda-agent/issues/55) | Track actual page-rename acceptance | Open despite `agent-implemented`; error propagation is not a working rename |
| [#43](https://github.com/zhoushoujianwork/easyeda-agent/issues/43) | Later chip-level end-to-end milestone | RF, impedance and physical success cannot be certified by offline Go tests |

The PCB public packages and `make pcb-solver-test` are another offline research
lane. They do **not** validate #273's schematic solver. Select targeted
schematic regression checks for #273 and the repository's relevant Go checks;
determine exact commands after locating its implementation.

## 2. Integration decision

| Option | Advantage | Limitation | Decision |
| --- | --- | --- | --- |
| Enable ClawFlow Pilot/operators alone | Shortest route to an existing issue-to-PR loop | Separate execution, context and recovery path from Host; native boundaries still need acceptance | Valid alternative if standalone ClawFlow service is the goal |
| Host invokes whole `clawflow run` loops | Reuses most pipeline code | Imports nested planning, writeback, merge, provider and process policies; command success is not each Pilot/task's success | Not the first managed-mode integration |
| Host mission + narrow ClawFlow VCS adapter + Host native execution | One task owner, common evidence and native sessions | Requires mission/workspace lifecycle development | Recommended |

Disable automatic Pilot/operator execution for an enrolled managed repository,
after checking in-flight runs. Keep ClawFlow manual/read-only tools available.
Disabling a schedule alone does not stop an already running writer: enroll only
after it is reconciled. Import existing issue/PR links as observations, not
retroactive acceptance or authority. Do not adopt an unowned worktree blindly.

ClawFlow remains the preferred issue/PR tool where its contracts suffice. Add a
small internal adapter for missing SHA/check/review data or extend its JSON
surface. No public plugin SDK, general workflow language or provider rewrite is
needed for the first repository. Merge support follows later and must use an
expected-head operation such as the one documented by
[GitHub CLI](https://cli.github.com/manual/gh_pr_merge); a preflight check alone
does not prevent a head-change race.

## 3. Mission state and source authority

Suggested objects, to refine during implementation:

| Object | Minimum responsibility |
| --- | --- |
| Mission | Objective, owner/audience, stable project/repository, charter revision, mode, budgets, status and next wake condition |
| Capability claim | Source commit/section, intended behavior, evidence and limits, linked work items |
| Work item | Question/change, acceptance criteria, dependencies, allowed resources, issue link, phase and blocker |
| Attempt | Existing Host run reference, native session/configuration, workspace, inputs and result evidence |
| Operation | Durable intent, target identity, idempotency/reconciliation key and observed receipt |
| Decision | Authenticated owner decision and affected mission/task/revision; keep separate from retrieved discussion |

Use one authoritative shared coordinator store under the chosen Host root,
extending the project runtime schema as appropriate. Separate new mission/work
item tables and readable exports from existing `tasks/<run-id>` receipts; do not
reinterpret or overwrite those receipts. Original channel evidence stays in its
own state home. Views in Web/GitHub are projections, not additional schedulers.

Mission lifecycle: draft → active ↔ paused → completed/retired. Finite issue or
research missions complete on acceptance; repository stewardship remains active
but can be idle. Work items move through ready, researching/implementing,
validating, awaiting review and accepted. Use explicit blockers for dependencies,
environment, owner decisions, exhausted budget and uncertain external results.

README and linked specifications supply product intent. Applicable repository
instructions supply development conventions. Host grants bind allowed actions.
Store their separate origins; no repository text, bot label, issue comment or
memory can mint new authority. A verified owner response can update the charter
through an authenticated decision path; an arbitrary `ready-for-agent` label
cannot. Repository grants explicitly adopted by the owner remain reusable.

Separate these facts: native execution completed; checks passed for a revision;
business acceptance established; PR merged; result delivered; memory published.
An issue closed as duplicate or not planned does not count as accepted work.
Likewise, all sub-issues being closed does not establish a parent milestone's
acceptance; reconcile each child's disposition and the parent's integration checks.
Avoid `Fixes #N` auto-close wording until the PR satisfies that issue's full
acceptance scope; partial fixes use a non-closing reference.

On conflicting source changes, invalidate affected plans/evidence, explain the
conflict and continue unrelated eligible work. Implementation may suggest a
requirement change, but cannot silently approve its own proposal. Keep a
versioned acceptance baseline and verifier outside the worker's ability to
replace the accepted baseline without review.

## 4. Wakeups, work selection and recovery

Initial deployment is one host with durable local storage. A service loop
observes source revisions, issue/review/check changes, run completion, dependency
completion, owner decisions and due experiment steps. Persist wake events before
dispatch and coalesce duplicates per mission. Do not launch a model to decide
whether an unchanged blocked item is still blocked.

Use bounded polling for a local pilot without a public endpoint; prefer webhooks
when available. GitHub recommends webhook delivery over frequent polling and
conditional requests when polling is necessary. A later webhook listener should
verify signatures, queue quickly and deduplicate delivery IDs; retain periodic
reconciliation for missing events. See [REST guidance](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api)
and [webhook guidance](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks).

Each dispatch:

1. Reconcile active processes, local Git changes and pending external operations.
2. Refresh changed sources and invalidate stale acceptance/plan inputs.
3. Select an eligible item by explicit user priority, blocking impact and
   readiness; prefer existing review fixes and bounded deliverables over new work.
4. Reserve budget and a writer/resource lease transactionally.
5. Assemble the cited charter, task, applicable Skills and evidence; invoke the
   existing harness once with bounded scope.
6. Verify actual outputs, persist the result and schedule its next condition.

Start with one active item, a small review queue and a bounded candidate backlog.
Suggested pilot tuning: 30-minute source reconciliation when idle, immediate
local completion wakes, one new candidate per planning wake, and at most two PRs
waiting for review. These are proposed controls, not measured optimum values or
an automation configured by this document.

Use leases for the mission, task writer and exclusive external environments.
Each lease has an owner and generation so stale workers cannot publish accepted
results. Generation checks alone cannot stop filesystem writes: confirm the old
process tree has stopped before granting a replacement writer. Cross-machine
execution is deferred; do not advertise a local lockfile as a distributed lock.

Record intent before push, PR creation, comment or EDA action. External APIs do
not become exactly-once merely because Host has a transaction. After timeout,
query by recorded repository/branch/head/marker and compare the intended result;
do not blindly retry. If outcome remains unknown, block that operation while
preserving the work and evidence. Reconcile self-generated events without
starting a new planning loop for every status comment.

Pause stops new dispatch. A separate stop request cancels owned work, waits for
termination and reconciles possible partial effects; it does not roll back
unknown external writes or delete worktrees. Resume restores mission records,
then either resumes a compatible native session or starts a fresh one with
verified context. Session loss must not lose the objective or repeat a mutation.

## 5. Execution, authority and environment boundaries

Reuse [managed development](development-workflow-detail.md) for worktree
preparation, repository locks, checks bound to the tested tree and PR identity.
Keep one task/branch/worktree/PR through revisions. A changed candidate, base,
verifier or relevant environment invalidates its affected validation. Worktrees
share Git data and are not OS sandboxes; see [Git worktrees](https://git-scm.com/docs/git-worktree).

Before unattended edits, prove a native/environment profile can enforce its
allowed workspace, tools, credentials and network capabilities. A concrete
offline profile should use a disposable worker environment with the task
checkout writable, unrelated Host homes inaccessible, no ambient publishing or
EDA credentials, and only the network needed for model/dependency access.
Publisher credentials remain in the Host-side adapter. Verify this with attempted
out-of-scope writes/publication and cancellation tests. Prompt wording and an
ACP session mode are insufficient proof. If the selected ACP bridge cannot meet
these controls, block editing and report it; do not silently fall back to a
different provider or uncontrolled execution.

The current channel `bypassPermissions` behavior must not be presented as this
restricted managed profile. Prototype continuity with a controlled read-only
fixture first. The actual ACP/process isolation capability is an implementation
feasibility gate, not something this research has demonstrated.

The proposed charter separately grants local edits, issue updates, push/PR,
merge, live EDA use and release publication. Once approved, routine in-scope
actions do not ask again. Only authentic new requirements/decisions change
scope. Default pilot delivery stops at the agreed review point. Future low-risk
auto-merge needs explicit standing authority, branch protection, current-head
checks and independent review; passing tests alone is not permission.

For easyeda-agent:

- Explicitly adopt managed branches from the selected `dev` baseline; its current
  direct-dev convention otherwise remains applicable. Do not automatically merge
  local-only development into the pilot base or treat `main` as current `dev`.
- Bind a real EDA capability to the exact authorized host/window/project/document
  and tool versions. Keep one writer per window, including human activity. A lost
  connection moves the item to waiting-environment, not success or automatic replay.
- Follow its typed CLI/actions and real save → reload → fresh readback rules.
  No GUI editing or arbitrary JS fallback for a missing engineering operation.
- Respect current layout confirmation before full-board routing. A request for
  general repository management does not waive that version-specific decision.
- Separate offline tests, connector tests, live EDA acceptance and physical
  board validation. Software checks cannot certify fabrication or power-on.
- Release approval remains specific to the proposed version and its required
  evidence; repository maintenance authority does not publish a release.

## 6. Research and cost control

Each experiment records question, hypothesis, baseline/candidate revision,
input hashes, environment, random seeds where relevant, fixed resource budget,
metrics, verifier, output artifacts and conclusion. Preserve unsuccessful runs.
Use held-out cases and an independently executed checker to reduce overfitting.
Do not let the candidate algorithm redefine the validity rules used to score it.

For #273, first check whether current code still reproduces the frozen failure.
If it does, investigate a bounded pruning/ordering change under the same candidate
and time budget. Compare feasible output rate, elapsed time, search steps and
validity. A counterexample or evidence that a proposed fix breaks constraints is
a valid research result. Exhausting finite search proves only that budget was
exhausted. If current code already resolves it, gather closure evidence rather
than forcing a new patch.

Each run has time, tool/action and experiment limits. A proposed pilot default
is two failed repair attempts on unchanged inputs before changing hypothesis or
surfacing a blocker. Continuing requires new evidence or a distinct justified
experiment; merely restating the prompt is not progress. A healthy idle mission
can remain active without doing model work.

Reserve mission budget before dispatch and settle observed usage afterward.
Track model, tool/compute and environment costs separately. Hard monetary caps
require provider/harness enforcement or reliable reservation bounds; when that
is unavailable, report the limitation and enforce observable time/run/action
limits. Do not claim an exact dollar ceiling based on post-run estimates.
Preserve partial results on limits and distinguish exhausted task budget,
provider billing refusal, timeout and execution failure. Do not automatically
replay on another provider.

## 7. Incremental implementation and acceptance

Keep the coordinator private to Host, initially in a small mission package
alongside the proposed development coordinator. Reuse routing, assembly,
projectruntime, state, harness and taskfeedback. Do not create a custom tool loop.
The workbench can initially use a simple mission list and detail view with existing
run links; a new visual workflow builder is unnecessary.

| Stage | Required proof before advancing |
| --- | --- |
| Read-only continuity | Objective and accepted scope survive restart; source changes wake once; unchanged blocked state makes no model call; pause works |
| Local offline work | Verified execution boundary; original checkout preserved; one writer; targeted test evidence matches the candidate; negative research results retained |
| GitHub delivery | Explicit grants; existing issues reused; one PR per task; uncertain creates reconciled; new head invalidates checks; no unauthorized merge |
| Live EDA | Exact environment binding, exclusive access, required layout decision, typed operations and save/reload/fresh evidence |

Minimum failure scenarios: duplicate trigger; process dies after commit; PR
created but receipt lost; lease expires while worker is alive; owner edits source
requirements; checks fail after conflict repair; no budget; read API unavailable;
EDA disconnect after partial write; issue closed without acceptance; incompatible
native session; publisher rejects an obsolete head. Test controllable failures
locally, then perform a separately authorized GitHub pilot. A documentation check
or passing Host test suite does not demonstrate these proposed capabilities.

Measure accepted results, owner interventions, reproducibility, reopen/rejection
rate, useful research conclusions, time/cost, idle wakes and duplicate external
writes. Do not set throughput promises before observing the pilot. Compare the
same class of work against the current manually driven workflow.

## 8. Investigation provenance

A bounded read-only offload worker inspected Host foundations. Status `ok`,
exit code `0`, profile `cc`, model `claude-sonnet-5[1m]`, 27 tool turns,
session `30e71d03-963b-466e-b9c3-3bafe71a7ea6`, measured elapsed 108.852 seconds,
estimated worker cost **USD 0.783372**. This is not verified billing or total
research cost. The main agent checked the central project receipt, recovery,
feedback queue and design references; notably, the source-channel override
resolves the worker's uncertainty about receipts being CLI-only.
