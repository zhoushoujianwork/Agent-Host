# Progress and next steps

Source status reviewed on 2026-10-16. This is the current backlog, not a release
commitment. [Architecture](architecture.md) explains the technical route;
[validation notes](release-follow-up-detail.md) retain evidence and open checks.

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

1. **Real member and owner approval flow.** Use a second authenticated account
   for restricted group requests, approval/rejection, expiry and separate approved
   execution. Verify that member sessions remain restricted and owner-private
   data stays private. Current members cannot build, edit or run shell commands
   directly under the restricted profile.
2. **Multi-user continuity.** Verify distinct private users, alternating group
   members, profile selection and source grants on real platforms. Feishu group,
   multi-turn and reaction acceptance remain separately tracked.
3. **Daily reliability and memory usefulness.** Observe sustained use, including
   fresh-session reuse, corrected procedures, disabling and failures. Preserve
   bounded evidence; general accuracy, scale and production latency are unmeasured.
4. **Open diagnoses.** Investigate the historical Jev admission mismatch and
   transient event-write wait. Later retrieval fixes and cancellation decoupling
   do not by themselves resolve their original causes.

## Proposed development order

1. **Durable engineering delivery.** Build a feature task spanning runs, owned
   worktree, validation and commit receipts, then authorized PR delivery. Local
   checkout discovery is already done. Clones/new repositories and GitLab MR
   delivery remain proposed. [Workflow](development-workflow.md).
2. **Learned per-person preferences.** Add explicit inspect/correct/disable/opt-out
   behavior on top of scoped memory and configured private profiles. Do not infer
   that identity initialization completes the preference product. [Profile scope](person-profiles.md).
3. **Later expansion.** Personal memory domains, automatic conversation learning,
   proactive notifications, Codex execution and meeting audio need their own
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
delivered; Web task submission and a general plugin marketplace remain future work.
