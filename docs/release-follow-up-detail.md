# Release follow-up: notes

Companion to the [follow-up plan](release-follow-up.md). Completed behavior and
historical test results stay in the [release record](release-daily-use-detail.md).

## Execution and Mac evidence

Source review date: 2026-11-05, revision `9f40091`. The current
[architecture](architecture.md) and [execution boundary](offload-execution-boundary.md)
define Offload Go-package-only execution. Migration `0553949`, Codex owner
integration `cca5647`, isolated runtimes `06432ed` and explicit Skill selection
`623b52d` supersede the old ACP/print execution plan. Unsupported requests fail;
historical sessions are not silently adopted or replayed.

The [2026-10-30 canary](offload-canary-acceptance-2026-10-30.md) records bounded
real Web execution, continuation, accepted-request deduplication, memory write,
fresh-session recall and invalidation on `54d8497` with Offload 0.4.0. It does
not establish current-package production, desktop, live cancellation or general
memory quality acceptance. Timing and context fixes have additional canary
receipts recorded in issues #54 and #56; their original CLI transport contracts
have since been replaced by the package interface.

[ACP setup validation](acp-execution.md) and the
[owner-private channel rollout](acp-channel-rollout.md) are historical evidence
for their recorded revisions only. They do not validate current Offload channel
deployment or justify retaining an ACP fallback.

Mac source consolidation `2970717` reached main through PR #63 (`6396351`) and
PR #64 (`9f40091`). Earlier PR #37 (`eb46212`) desktop reports and local ACP
reports on `d1354ca`/`9c000d3` remain revision-specific. Current boundaries are
documented in [Mac acceptance](macos-app-detail.md#source-and-acceptance-boundary)
and [updater checks and recovery](mac-app-updates-detail.md). Headless fixtures
and native compilation do not replace current desktop, notification,
accessibility or real replacement/relaunch/rollback acceptance. Installed app
and separately deployed runtime adoption must be evidenced independently.

## Issue review evidence

The 2026-11-05 review started with 16 open issues. The subsequent authorized
cleanup closed #45/#48/#50/#52/#54/#56, updated the six acceptance checklists,
and narrowed #1/#9; 10 issues remain open. This was not a new live acceptance run. Commits below are present in the reviewed
source; PR references preserve the original delivery trail. Earlier issue
comments describing only local merges do not override current ancestry.

| Issues | Evidence and remaining boundary |
| --- | --- |
| #45 | `e23d4f1`, PR #46; `internal/offloadchannel/memory_test.go` covers sourced recall/invalidation, isolation and memory failure without replay. The canary adds bounded real memory evidence; general quality and production channel adoption remain separate. |
| #48 | `fd1046d`, PR #49; `internal/offloadexec/library.go` now carries and validates caller/run identity through the package. Old CLI argv expectations are historical. |
| #50 | `b40c1c1`, PR #51; `internal/cli/offload_chat_test.go` covers one-shot/resume and locks. Interactive CLI was subsequently delivered; old unsupported-mode wording needs updating. |
| #52 | `54d8497`, PR #53; `internal/cli/offload_web_test.go` and `internal/offloadchannel/rooms_test.go` cover the real CLI/Web bridge with fake native execution, deduplication and concurrency. The historical canary adds bounded live evidence, not current desktop/production acceptance. |
| #54 | `e2da514`, PR #55; `TestOffloadJournalElapsedUsesRecordedRunClock` covers elapsed time and backward clocks. The issue records canary run `95ff6762-31bd-4b40-a952-bc5e7e8afdf5`, with persisted/Web `elapsed_ms=51098`. |
| #56 | `debbe38`, PR #57; current `internal/offloadexec/library_test.go` checks visible Task identity separately from Instructions, and `internal/offloadchannel/memory_test.go` checks empty-context omission. The issue records canary AGH run `204306ef-a290-4590-8dfe-7536203463d1`; historical native instruction wrapping is not proof of current backend role semantics. |
| #36, #38, #39, #40, #41 | `2970717`, PRs #63/#64; source and headless coverage exist under `tools/macos-app/`. Update acceptance for the current Offload-based app; old desktop/ACP evidence cannot complete it. Real updater build/replacement/relaunch/rollback remains pending. |
| #42 | `82d63d7`, PR #47, followed by `dcf6fe3`; `tools/run-viewer/chat_test.cjs` covers streaming boundaries, restored replies and copying. Current Mac reproduction of the original smoke reply remains unverified. |
| #1 | Skill publication is covered by `TestFileHostAutomaticSkillPublicationFreshUseAndBlacklist`; rescope to the [learned preference lifecycle](person-profiles.md). The database-first proposal is historical. |
| #9 | Child issues #3–#7 were closed at review, but the parent checklist was synchronized; #8 remains the outstanding operational follow-up. Historical health counts are not current production measurements. |
| #8 | The service log writer in `tools/service/run.py` still appends. This review found no evidence meeting the requested rotation/retention acceptance; production growth and savings were not measured. |
| #13 | Latest issue comments defer canonical Skill delivery to ClawFlow #488, followed by a fresh unbranded GitLab CI/job-log request and actual Skill-loading evidence. External delivery and live acceptance were not established by this repository review. |

The cleanup preserved original issue requests and historical evidence in their
bodies. It did not rerun historical live checks. Execution completion, delivery, business acceptance and
memory persistence remain independently evidenced outcomes.

## P0 acceptance

Status: P0 implemented. Scope is defined in the
[personal-assistant guide](personal-assistant.md#current-p0);
[implementation and validation](personal-assistant-detail.md#p0-implementation)
record the read path, source/action boundaries and bounded acceptance.

Use one owner, one registered project and selected DWS conversations. Preserve
existing topic export, history access and memory publication semantics. The
release acceptance gates are:

1. **Evolving state.** Related evidence updates the same matter across collection
   and restart; interleaved issues remain separate. Partial coverage and silence
   do not establish an unresolved blocker or a successful outcome.
2. **AI access.** In a fresh native conversation, an ordinary question retrieves
   an authorized current matter and applicable existing memory, then reads their
   supporting evidence without the user supplying IDs. The answer distinguishes
   current status, reusable knowledge and source reports. Read tools cannot
   publish memory, alter a matter or start a task.
3. **Useful action.** A private suggestion explains why it is relevant and cites
   its evidence. The owner's selection starts exactly the requested preparation
   task and produces a source-linked result; restart/retry does not replay it.
4. **Freshness and control.** New progress refreshes or withdraws the suggestion;
   duplicate evidence does not repeat it. Postpone, dismiss and resolve have
   separate effects and do not silently change durable memory.
5. **Access and eligibility.** Search, direct reads, citations and context assembly
   exclude out-of-scope, expired, invalidated or conflicted memory as applicable.
   Detected source withdrawal blocks dependent reads and suggestions. Existing
   project/group boundaries are not broadened by the new owner-only surface.

Record deterministic replay results, a real native fresh-conversation retrieval
and action loop, and a bounded selected-source DWS pilot separately. Include
citations, coverage, stale/duplicate outcomes, usefulness judgments and failures;
do not invent a numerical quality target before the pilot. Run `make check` and
review the implementation diff before accepting the increment. Documentation
checks alone do not satisfy these runtime gates.

The [P0 acceptance record](../experiments/assistance/README.md) now records
passing fresh-native retrieval/preparation and a bounded real selected-source
pilot separately. It also retains the failed wide-window probe and unmeasured
general quality/scale boundaries; production rollout is not claimed.

P0 does not require a new personal memory domain, automatic conversation-derived
publication, stronger equivalent-re-admission protection, group recommendations,
proactive delivery or specialist memory stores. The separate automatic-experience
increment is delivered in migrated file runtimes; P1 must preserve that
publication/blacklist policy, while P0 only consumes eligible existing records.

## Issue #1 planning baseline

The 2026-10-13 issue #1 plan proposed automatic publication, native Skills and
per-person preferences. File-memory publication and native Skill integration
have since shipped, with bounded evidence in [file-memory details](file-memory-design-detail.md)
and [procedural learning](procedural-learning-detail.md). Configured private
profiles and verified identity initialization are also implemented; learned
preference observations and their inspect/correct/disable lifecycle remain proposed.

The original database-first sequence is retired. Use the
[current development order](release-follow-up.md#proposed-development-order).
This record makes no claim about current GitHub issue or PR state.

## Real non-owner group acceptance

Only the owner's DWS profile was available; production had no authenticated
non-owner group request at the latest audit. A second same-enterprise account
is needed for this check. Synthetic senders and self-declared owner text cannot
replace that evidence.

Verify restricted group mention execution, owner approval for a separate run,
current-sender reply addressing and
two members' distinct statements followed by a question about who said what.
Inspect authenticated source attribution, native context and exact platform
source/reply reads. Keep owner-private context separate. Existing local tests
cover metadata and partitions; real model/platform behavior still needs this
bounded check. Use synthetic content, retaining raw evidence outside Git.

## Observation and diagnosis

Use existing state and the viewer for the week of owner use; no new reporting
feature, fixed failure-rate threshold or memory-command quota is required.
Record execution failures separately from saving and delivery failures.

Investigate the original five-second `phase.progress` event-write wait using
source-linked timing and database/process evidence. Optional hints no longer
cancel execution. Do not treat that fix as proof of a SQLite lock, pool, I/O or
other root cause, or suppress failures of mandatory evidence writes.

## Deferred capability boundaries

- Linked retry must preserve original evidence and expose possible partial
  effects; never silently repeat a business task.
- Explicit memory must retain attribution and duplicate/conflict handling.
- DingTalk review must preserve provenance and distinguish task acceptance
  from execution completion and memory publication.
- Usage reporting should read existing records rather than add a second log.
- Automatic experience publication and blacklist behavior use the existing
  [implementation and transition plan](experience-memory-detail.md); they
  require separate save, reuse, disable and equivalent re-admission validation.

Current sender permissions are defined by the [authorization guide](dangerous-operations-design.md).
Additional chat commands and release dates are not decided by this backlog.
