# Release follow-up: notes

Companion to the [follow-up plan](release-follow-up.md). Completed behavior and
historical test results stay in the [release record](release-daily-use-detail.md).

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
