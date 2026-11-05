# Progress and next steps

Source status reviewed on 2026-11-05 at `9f40091`. Native execution uses the
Offload Go package only; ACP, direct Host-managed native execution and Offload
CLI compatibility are removed. Production adoption is separate. This is the
current backlog, not a release commitment.
[Architecture](architecture.md) explains the technical route;
[validation notes](release-follow-up-detail.md) retain evidence and open checks.

Reading aid / 阅读提示：see [key objects / 关键对象](architecture-detail.md#key-objects--关键对象)
and [technical terms / 技术名词](architecture-detail.md#technical-glossary--技术名词).
“Implemented / 已实现” means code exists; “live acceptance / 真实验收” means it
has been exercised with real accounts and platforms. These are separate milestones.
“已实现”说明代码能力已具备，“真实验收”说明已经过真实账号与平台验证，两者分别记录。

## Delivered since the daily-use baseline

| Increment | Current progress |
| --- | --- |
| File memory and native Skills | Explicit migration, automatic selected-experience publication, blacklist, scoped Skill packages and bounded native use implemented |
| Procedural learning and external memory | Complete command corrections, bounded refinement, source-note import, attributed external ingestion and memory skill installation implemented |
| Runtime-managed projects | Local checkout discovery, request-driven enrollment and shared execution receipts implemented |
| Knowledge operations | Global enabled datasource configuration, explicit user/group grants, Confluence page/blog writes and original-article citation guidance implemented |
| Runtime operations | macOS/Linux user services, independent production relocation and encrypted scheduled backups implemented |
| User identity and authorization | Verified private-user profiles, first-contact initialization, restricted member tools, one-use owner approvals and local member-debug mode implemented |
| Reply quality | Concise answer and reference guidance applied on every native turn; internal note attribution suppressed in ordinary replies |
| Contextual assistance (P0) | Current matters, scoped read access and private suggestion-to-preparation loop implemented with bounded acceptance |

Implementation does not close the live/quality checks below. File-memory
publication is delivered; legacy SQLite experience still uses `memory accept`.
Configured private profiles are delivered; the learned per-person preference
lifecycle in [the profile proposal](person-profiles.md) remains separate.

## Next checks

1. **Member and owner approval flow — suspended.** Current channel senders use
   Owner-equivalent native execution, as described in the
   [authorization guide](dangerous-operations-design.md). Restricted member and
   one-use owner approval code remains available but is not the active Beta
   workflow. If that policy is re-enabled, use a second authenticated account
   to verify restrictions, approval/rejection, expiry and separate approved
   execution. Private audience boundaries still need live verification.
2. **Multi-user continuity.** Verify distinct private users, alternating group
   members, profile selection and source grants on real platforms. Feishu group,
   multi-turn and reaction acceptance remain separately tracked.
3. **Daily reliability and memory usefulness.** Observe sustained use, including
   fresh-session reuse, corrected procedures, disabling and failures. Preserve
   bounded evidence; general accuracy, scale and production latency are unmeasured.
   The 2026-10-25 [subscriber lifetime fix](feishu-channel-detail.md#subscriber-lifetime)
   adds abrupt-Host-death lock-release coverage. Channel failure isolation,
   continuous readiness checks and enforced operational handoff remain open.
4. **Open diagnoses.** Investigate the historical Jev admission mismatch and
   transient event-write wait. Later retrieval fixes and cancellation decoupling
   do not by themselves resolve their original causes.

## Skill consolidation and integration track

This track continues the scope selected on 2026-10-23, with explicit capability
selection under the current execution contract.

1. **Delivered:** authored adoption/review/publication, eligible experience and
   plugin guidance in the effective catalog and bounded local native acceptance.
   Native Skills require explicit selection, including for the default Agent;
   omitted, null or empty `native_skills` selects none. Catalog availability and
   publication do not enable a Skill.
2. **Read-only Web inspection delivered:** authored catalog/revisions/full
   references and destination-scoped historical Agent snapshots. See the
   [Platform Skills design](platform-skills-web-design.md). Production rollout is
   separate from source validation.
3. **Export delivered:** a pinned authored revision can be downloaded from
   Settings or the CLI with complete files, checksums and provenance. The platform
   remains authoritative; export never changes publication or selection.
4. **Next:** extend inspection/export to admitted experience and provider-bound
   plugin guidance. Keep lifecycle ownership in the platform; manual Web import
   and publication controls are no longer the next product workflow.
5. **Then:** evidence-driven platform consolidation needs acceptance rules for
   duplicates, corrections and regional variants. Preserve source-specific
   admission and history. Automatic procedure merging remains unimplemented.

## Execution integration track

The [Offload package boundary](offload-execution-boundary.md) serves Claude and
Codex execution across CLI, Web and channels. Host owns routing, context,
authorization, evidence, acceptance and memory; native harnesses own model/tool
loops and conversation state. Unsupported capabilities fail explicitly, without
backend fallback or task replay.

- **Delivered:** interactive and one-shot CLI Chat, Web room/request IDs,
  recorded-result deduplication, compatible continuation and independent room
  concurrency. [Chat scope and limits](offload-chat.md).
- **Delivered:** optional scoped recall and memory processing, caller/run
  correlation, recorded-clock elapsed time, and separate user Task and Host
  Instructions. Answers are available before memory closeout; execution,
  delivery, acceptance, history persistence and memory outcomes remain distinct.
  [Memory scope](offload-memory-plugin.md).
- **Delivered:** explicit native Skill allowlists and private Claude/Codex
  conversation environments. Continuation requires the original compatible
  environment; isolated native forks remain unsupported.
  [Clean execution contract](clean-ops-agent-detail.md).
- **Pending:** production rollout and broader current channel/model acceptance.
  Historical ACP validation applies only to its recorded revisions, not this
  package contract. Bounded Offload canary evidence and remaining limits are in
  the [validation notes](release-follow-up-detail.md#execution-and-mac-evidence).

## Mac app source and acceptance

The [Mac app](macos-app.md) source now includes an app-owned local runtime,
shared readiness guidance, native menus, task inbox read/acknowledgement state,
notifications and exact-record navigation. [Source-only updates](mac-app-updates.md)
implement automatic stable updates and opt-in development updates with idle
gating and recovery. `2970717` was integrated through PRs #63 and #64.

Current desktop interaction, real notifications, accessibility and a complete
Offload-backed app task remain separate acceptance work. Real updater candidate
build/replacement/relaunch and rollback also remain pending. Headless checks,
compilation and older desktop reports do not prove current desktop acceptance
or adoption by an installed app or production service.

## Proposed runtime and memory work

The [capability runtime proposal](agent-capability-runtime.md) next calls for
explicit memory/knowledge bindings and a reversible AGH-root/profile-binding
migration while retaining native login. Capability validation and the narrow
default-owner memory-tool fix are delivered; broader packaging and directory
migration are proposed.

[Dream memory rotation](dream-memory-rotation.md) is design-only: preserve raw
handoff evidence, derive scoped TTL hot memory, then submit reusable procedures
through existing Skill review/publication and explicit enablement. Start with
read-only scanning/preview and fixtures proving stable sources, idempotency and
zero memory writes. A later bounded worker requires backend-specific proof of
tool-free execution; stop at read-only/pending review if the gate fails. No dream
scanner, scheduler or automatic procedure merger is delivered.

These technical proposals do not reorder the product proposals below.

## Proposed development order

1. **Owner Answering Beta / Owner 代答 BETA.** Observe selected private DingTalk
   chats, answer complete questions from an approved answer set, and wait silently
   for missing details or owner handling. Learn style and solutions separately.
   Owner selected private-chat pilots and a visible AI reply marker. Deliver
   capability verification, shadow evaluation and restricted live sending in that
   order. [Main design](owner-answering-beta.md); not yet implemented.
2. **Durable engineering delivery.** Build a feature task spanning runs, owned
   worktree, validation and commit receipts, then authorized PR delivery. Local
   checkout discovery is already done. Clones/new repositories and GitLab MR
   delivery remain proposed. [Workflow](development-workflow.md).
3. **Learned per-person preferences.** Add explicit inspect/correct/disable/opt-out
   behavior on top of scoped memory and configured private profiles. Do not infer
   that identity initialization completes the preference product. [Profile scope](person-profiles.md).
4. **Later expansion.** Personal memory domains, conversation learning beyond the
   owner-answering pilot, proactive notifications and meeting audio need their own
   implementation and acceptance. No delivery dates are assigned.

## Retired planning baseline

The old issue #1 delivery sequence and database-first memory plan have been
removed from the active backlog: automatic file-memory publication and native
Skill integration are delivered. [Historical notes](release-follow-up-detail.md#issue-1-planning-baseline)
retain the original planning context without claiming current GitHub issue state.
The [2026-10-12 release record](release-daily-use.md) remains historical; current
member permissions are defined by the [authorization guide](dangerous-operations-design.md).

Keep `/new`, `/status` and `/help` as current chat controls. Add retry/review/usage
features only when observed use justifies them. The existing local workbench is
delivered, including Web Chat submission through a separately configured Host CLI.
Claude and Codex use the common Offload package boundary. Richer channel controls,
additional backend capabilities and a general plugin marketplace remain future work.

## Issue disposition snapshot

The 2026-11-05 cleanup applied the following GitHub dispositions. These changes
do not establish production or live acceptance. [Evidence and caveats](release-follow-up-detail.md#issue-review-evidence)
remain tied to their source revisions.

| Review disposition | Issues |
| --- | --- |
| Closed as completed | #45, #48, #50, #52, #54, #56 |
| Open, remaining acceptance checklist updated | #36, #38, #39, #40, #41, #42 |
| Open, remaining scope updated | #1, #9 |
| Retain | #8, #13 |
