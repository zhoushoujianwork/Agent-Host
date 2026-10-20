# Progress and next steps

Source status reviewed on 2026-10-16; the shared ACP execution track was added
on 2026-10-20, with the first Claude ACP runner implemented that day. This is the current backlog, not a release
commitment. [Architecture](architecture.md) explains the technical route;
[validation notes](release-follow-up-detail.md) retain evidence and open checks.

Reading aid / 阅读提示：see [key objects / 关键对象](architecture.md#key-objects--关键对象)
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
4. **Open diagnoses.** Investigate the historical Jev admission mismatch and
   transient event-write wait. Later retrieval fixes and cancellation decoupling
   do not by themselves resolve their original causes.

## Execution integration track

Adopt [shared ACP execution](architecture.md#shared-acp-execution) for
CLI, Web, DingTalk, Feishu and future conversation entries. This is a Host
harness change shared by channels, not a Web-only Claude connection.

1. **Implemented:** configurable Claude ACP adapter, Host context/tool policy,
   streamed events, native continuation and execution cancellation. Bounded real
   Claude checks passed; [setup and limits](acp-execution.md).
2. **Pending platform acceptance:** exercise the shared adapter in DingTalk/Feishu,
   with channel-appropriate progress,
   final delivery and authenticated interaction/stop controls.
3. Connect a second engine to prove switching; retain explicit legacy print
   selection until required execution contracts pass. Never silently fall back
   or reuse native sessions across engines/transports.

The second engine, warm bridge reuse, interactive channel controls and real
platform acceptance remain open. The
[detailed boundary](architecture-detail.md#shared-acp-execution--planned) records
session isolation, capability limits and separate auxiliary extraction checks.
The product proposals below remain a separate track.

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
   owner-answering pilot, proactive notifications, Codex execution and meeting audio need their own
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
Claude ACP execution is selectable through the common Host boundary. Additional
engines, channel stop controls and a general plugin marketplace remain future work.
