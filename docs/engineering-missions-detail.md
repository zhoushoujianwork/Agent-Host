# Persistent engineering missions: research and implementation notes

Status: revised proposal, 2026-10-23: ClawFlow owns engineering missions; agh
provides entry and execution services. The [main proposal](engineering-missions.md) owns
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
| [Development detail](development-workflow-detail.md) | Worktree ownership, revision-bound checks, publication intents and head-specific merge are already designed. Reuse its lifecycle invariants in ClawFlow; its original Host-owned coordinator is superseded for ClawFlow-managed work. |

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
contracts. Adapt the implementation Skill's push/PR steps to ClawFlow-controlled publication
authority; do not mount the current autonomous operator unchanged into a
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

## 2. Revised integration decision

The user's revised direction places persistent engineering management in
ClawFlow. This supersedes the earlier Host-owned Mission recommendation.
ClawFlow owns product goals and engineering scheduling; agh supplies unified
intake, execution, scoped context and delivery. Existing Host capabilities remain;
this proposal does not remove or relocate their delivered implementations.

| Component | Authoritative state and decisions |
| --- | --- |
| ClawFlow | Mission, capability claims, priorities, dependencies, budgets, worktree ownership, issue/PR mapping, verification and acceptance |
| agh | Authenticated intake/delivery, accepted execution requests, run lifecycle, event history, native session binding and observed usage |
| Native harness | Model/tool loop, native conversation and supported tool/permission behavior |
| GitHub | Actual repository, issue, PR, review and CI state; ClawFlow reconciles observations against its task state |

Use the existing ClawFlow Pilot/operator scheduling path and extend it with
mission continuity. Do not add a second independent scheduler alongside Pilot.
Within agh, capacity admission and one-owner-per-native-session locking are
execution mechanics, not business priority or backlog management.

The two directions are explicit:

- User intake: channel → agh business connector → ClawFlow mission operations.
- Execution: ClawFlow operator → agh execution gateway → native harness.

An execution request bypasses business-intent routing. It cannot create another
ClawFlow mission or recursively invoke a full ClawFlow scheduling pass. General
agh conversations outside managed engineering keep their existing behavior.
ClawFlow's own UI can submit decisions directly without requiring an IM round trip.

## 3. Small execution-backend contract

First implement an agh backend in ClawFlow's existing native-execution seam.
Inspect the exact call boundary during implementation: operators and Pilot both
currently invoke native execution, so both need the selected backend eventually.
Start with one operator, then route Pilot planning runs through the same contract.
This is not a request to invoke all of `clawflow run` inside an agh native session.

Proposed operations are submit, inspect, stream events, cancel and explicit
continuation. These names describe requirements, not delivered API endpoints.
Use a narrow authenticated local interface to an explicitly configured agh runtime;
a Unix socket is a reasonable first transport. Do not reuse private channel state
by writing its database, infer a runtime from cwd, or scrape rendered chat text.
An existing private control socket is not proof that this contract is available.

Submission includes:

- Caller identity and source authorization reference, owner/audience and result destination.
- Stable ClawFlow mission/task identifiers and a unique execution-attempt key.
- Bound repository, prepared absolute workspace and expected base/candidate identity.
- Bounded assignment, acceptance-input references, allowed actions/tools and artifact location.
- Selected Agent/execution profile, supported resource limits and optional compatible continuation reference.

agh responds with a durable run ID and request fingerprint before work can be
considered accepted. Re-submitting the same key and identical payload returns the
same run; a different payload with that key is rejected. A new attempt requires a
new key chosen by ClawFlow after reconciliation. Lost responses are resolved by
querying the original key, not by generating a new one.

Events have a run ID, ordered sequence/cursor and event type. Results include
execution status, exit/error information, artifact references and hashes,
observed workspace revision, native continuation reference and usage coverage.
Keep model output separate from observed command/Git/provider evidence. Reuse
agh event types where possible; do not invent a full second engine protocol.
Missing artifacts or unsupported limits are explicit capability errors.

Poll/stream from a stored cursor first; callback delivery is optional later.
Repeated event delivery is harmless after consumer deduplication. Execution
completion is not evidence of successful notification, acceptance or merge.
Do not label a canceled request stopped until process termination is observed.

## 4. State, continuity and recovery

ClawFlow persists its own mission state in its existing project/data ownership
area with a transactional update mechanism selected during implementation. agh
keeps execution records in its configured runtime. Link the two with task,
attempt and run identifiers; do not copy native transcripts into a second
canonical mission history. Existing agh project receipts remain per-run records.

ClawFlow's mission binds the objective, source revisions, adopted repository
rules, owner grants, budget and next wake condition. Each work item retains
acceptance requirements, dependencies, workspace, experiments and PR evidence.
Finite issue/research missions complete when accepted; repository stewardship
can remain idle until new work appears.

| Failure | Recovery owner and behavior |
| --- | --- |
| ClawFlow restarts while agh executes | ClawFlow queries its saved run ID/key and resumes event consumption; no replacement writer |
| agh accepted submission but response was lost | ClawFlow queries/re-submits the identical key; agh returns the same run |
| agh restarts with uncertain native execution | agh reports unknown until process state is reconciled; ClawFlow waits or investigates before deciding continuation |
| Model run fails or reaches a limit | agh records partial evidence; ClawFlow decides a new experiment, retry or blocker |
| PR/EDA write times out | ClawFlow reconciles the external target; agh must not blindly replay the business action |
| Native context is incompatible | agh rejects continuation; ClawFlow can explicitly request a new session using saved evidence |
| User pauses a mission | ClawFlow stops dispatch; stopping active work additionally calls cancel and waits for acknowledged termination |

ClawFlow owns one writer/worktree lease and exclusive EDA resource binding. agh
owns native-session/run concurrency. These locks protect different resources.
Lease expiry alone is insufficient to start a new writer while the old process
might still run. Start with one machine and reconcile complete process trees;
do not advertise local lockfiles as distributed exclusion.

ClawFlow records intent before publication or external mutation. Provider APIs
are not exactly-once transactions with local storage. Persist branch/head/target
and operation markers, then reconcile uncertain outcomes before retrying.
Changing execution backend never silently replays prior work or switches provider.

## 5. README-driven planning, permissions and knowledge

ClawFlow reads README and linked design/acceptance documents into a small
capability ledger: claim, source revision, implementation evidence, validation,
known limits and linked issues/PRs. Source changes invalidate only affected plans.
Search existing issues before creating new work; do not turn every README
sentence into an implementation task or invent requirements to keep the agent busy.

Research follows question → fixed baseline → bounded experiment → independent
verification → conclusion. Preserve negative results, input hashes, code/verifier
versions, resource limits and counterexamples. A supported negative result may
satisfy a research task; an unresolved blocker is waiting, not accepted completion.

ClawFlow decides business acceptance against recorded criteria. A separate
verification run may use agh, but the implementing worker cannot rewrite the
accepted criteria. A successful run, closed issue or `agent-implemented` label
does not establish acceptance. Parent milestones require integration evidence,
not merely closed sub-issues. Use non-closing PR references for partial fixes.

ClawFlow owns standing engineering authority and its revision. agh validates the
caller and enforces the effective intersection of requested capabilities, its
execution profile and native/environment controls. User identity and audience
must survive the channel → ClawFlow → execution round trip. Source documents,
comments, bot labels and memory cannot manufacture authority. Owner responses
bind to the concrete task/decision/revision; arbitrary chat cannot grant scope.

Keep publisher credentials with ClawFlow-controlled operations, outside coding
workers. Before unattended editing, validate workspace and credential isolation,
cancellation and forbidden-operation behavior. Current agh bypassPermissions
behavior is not proof of those boundaries. A disposable offline environment is
a candidate implementation; actual ACP bridge compatibility remains a feasibility
gate. Unsupported restrictions block that profile, without provider fallback.

ClawFlow supplies project-specific context; agh may add authorized shared Skills
and reusable knowledge. Mission state and acceptance stay in ClawFlow even when
knowledge is persisted through agh. Private channel history is not automatically
shared with project runs. Memory persistence failure does not erase a completed
run or change accepted engineering evidence.

## 6. Scheduling, cost and the easyeda pilot

ClawFlow alone observes README/repository changes, issues, PR reviews, CI,
dependency completion, owner decisions and available environments. Persist and
coalesce wakeups. Idle checks should not consume model calls when nothing
material changed. Prefer existing review fixes and blocked-dependency resolution
over continually creating more work. Keep a bounded review backlog.

For a same-machine pilot use modest conditional polling if no webhook endpoint
exists. Later verify webhook signatures and deduplicate deliveries, with periodic
reconciliation for missed events. GitHub documents both
[REST polling guidance](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api)
and [webhook handling](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks).
These are ClawFlow concerns, not an agh engineering timer.

ClawFlow reserves mission budgets before dispatch. agh enforces supported
per-run limits and returns measured usage plus unknown fields. Monetary ceilings
are only hard when the selected provider/harness supports enforcement or reliable
reservation bounds; otherwise expose that limit and use time/run/action bounds.
Distinguish budget exhaustion, provider refusal, timeout and genuine execution
failure. Do not claim exact cost from estimates or replay on a weaker provider.

First pilot: easyeda-agent #273. Pin the chosen current dev baseline and reproduce
the frozen schematic inputs. Use schematic-specific checks; PCB solver tests do
not certify this issue. Compare a bounded search change under equal resource
budgets and an unchanged verifier. If current code already fixes it, collect
closure evidence instead of manufacturing a patch. Search exhaustion does not
prove mathematical infeasibility.

ClawFlow prepares the task workspace, agh runs research/implementation/checks,
and ClawFlow reconciles evidence and handles authorized PR delivery. Its managed
branch policy must be explicitly adopted for this pilot because easyeda-agent
normally develops directly on dev. Preserve local-only changes and never confuse
remote main, remote dev and the newer local checkout.

Live EDA comes later: exact host/window/project/document binding, one writer,
typed actions, explicit save/reload/fresh readback, and current layout confirmation
before full-board routing. No GUI/JS engineering bypass for missing actions.
A lost connection blocks live work while eligible offline tasks can proceed.
Release publication and physical board acceptance remain separate decisions.

## 7. Delivery increments and verification

| Increment | ClawFlow change | agh change | Acceptance |
| --- | --- | --- | --- |
| Single read-only run | Add explicit agh execution backend for one operator | Authenticated submit/inspect/event/cancel boundary over existing execution | Correct identity/workspace; final receipt; cancellation; explicit errors |
| Persistent follow-up | Task/attempt/run mapping, durable checkpoints and restart reconciliation | Durable request deduplication and resumable event access | Either process can restart without duplicate execution |
| Offline mission | Mission/capability ledger, research loop, prepared worktree and evidence-based acceptance | Verified managed execution profile and scoped context | #273 baseline/result, preserved checkout, relevant tests, bounded cost |
| GitHub and live work | Authorized PR/review handling, source changes, EDA resource scheduling | Delivery integration and required engine/tool capabilities | No stale-head acceptance, no blind retry, correct live evidence |

Keep the existing direct native backend explicitly selectable during migration.
One attempt selects one backend and retains that identity. Do not disable Pilot
as recommended in the previous architecture: extend its existing scheduling
ownership. Prevent old and new paths from concurrently running the same task.

The first API is private and local, not a public plugin framework or distributed
worker platform. Do not require all future engines before proving one Claude
path. Engine model/tool loops stay native; agh remains a general execution
service and ClawFlow retains the engineering product UI and policies.

Test duplicate submissions, changed payload with reused key, lost acceptance
response, partial event stream, process-tree cancellation, both-side restart,
unknown external write, expired live writer lease, changed source criteria,
failed check, budget exhaustion and incompatible continuation. Then conduct a
separately authorized real repository pilot. Passing the documentation repository
checks does not establish this proposed integration.

The earlier Host development-workflow proposal remains useful for invariants
(worktree identity, checked revision, operation receipts and current-head merge),
but ClawFlow owns those policies for managed missions. Host should not implement
an additional PR or feature controller for the same work. ClawFlow's current
expected-head merge interface gap from section 1 still needs resolution before
that guarantee is advertised; see [GitHub CLI merge](https://cli.github.com/manual/gh_pr_merge).

## 8. Investigation provenance

A bounded read-only offload worker inspected Host foundations. Status `ok`,
exit code `0`, profile `cc`, model `claude-sonnet-5[1m]`, 27 tool turns,
session `30e71d03-963b-466e-b9c3-3bafe71a7ea6`, measured elapsed 108.852 seconds,
estimated worker cost **USD 0.783372**. This is not verified billing or total
research cost. The main agent checked the central project receipt, recovery,
feedback queue and design references; notably, the source-channel override
resolves the worker's uncertainty about receipts being CLI-only.
