# Learning from real operations

Status: a lightweight MVP is implemented on 2026-10-15. It adds bounded draft
refinement, complete command templates and recent-conversation recall hints.
General business effectiveness remains under observation.
See the [evidence and implementation detail](procedural-learning-detail.md).

## Goal and user experience

After solving a problem, retain the smallest supported lesson so the next task
can begin with a suitable working approach. Measure whether reuse avoids the
same error. This resembles learning through experience: observe, compare,
remember, apply, check, and revise.

Users continue submitting ordinary requests, including short follow-ups such as
“look into this resource” and “go ahead.” They should not need to name `tccli`,
ask the Agent to memorize a command, or approve each normal learning item.
The workbench should explain what was learned, what was excluded and why,
whether a later task used it, and what that use actually established.

## What the real cases show

The existing system preserves tool evidence, extracts procedures and avoidance
notes, evaluates them, automatically publishes selected file-backed memory, and
supplies scoped native Skills. The following historical cases motivated the MVP.

| Observed case | What happened to the lesson |
| --- | --- |
| October 14: CLB lookup by VIP using a JSON input file | A procedure was published and remains active in its own memory domain. |
| October 15: CBS lookup with an unusable profile, followed by a successful lookup | A profile-related procedure was published. Three subsequent related runs selected zero memories during initial recall. |
| October 15: `--cli-profile` failed; `--profile` returned disk data | The extracted lesson was withheld: support probability 0.72 was below the current 0.80 threshold. No smaller corrected lesson was automatically reconsidered. |
| October 15: audit lookup and DingTalk follow-ups | Three audit lessons were withheld. Three later DingTalk candidates were accepted into conflicted records, which are not active reusable Skills. |

These observations do not show that every withheld lesson should be published.
Some drafts generalize beyond the evidence or prescribe repeating failed work.
Publication, initial recall, actual use, and demonstrated benefit must be counted
separately. Native conversation context can also carry information without a
Host memory recall.

## MVP behavior

1. **Retain a complete successful command.** Extraction now requests a full
   command template, including quoting and target selectors. Concrete values
   become placeholders. Failed syntax belongs in the avoidance notes.
2. **Allow one smaller lesson.** When an evidence assessment withholds a draft
   citing both failure and later results, make at most one narrowing attempt per
   task, using only its original cited evidence. Apply the unchanged admission
   threshold and publication checks. Failures remain visible.
3. **Help short channel follow-ups.** When a request of at most 80 characters has
   no direct recall matches, retry retrieval using up to three active procedure
   titles learned in the same conversation's three most recent routed tasks.
   Recheck scope, current publication, expiry and applicability. Skill
   preselection uses exactly the same fallback as execution.
4. **Observe through existing evidence.** New learning and hint events explain
   the processing outcome. A small read-only report links paired native Skill
   invocations to mounted memory revisions and counts observed failures.

Normal use needs no additional configuration or approval. Known credential
fields are redacted from learning inputs; original operational evidence stays
intact. Syntax knowledge, dated account observations and current resource state
keep their different evidence strength. A previous successful account is not
general authorization to switch accounts.

## Observe before expanding

The original `--cli-profile` correction initially failed both evidence assessment
and comparison with an older credential-recovery lesson. Judgment policy v5
clarifies faithful command templates and compatible conditional procedures,
while retaining the 0.80 threshold. Literal duplicates of an active reviewed
procedure reuse its identity; conflicted records stay quarantined.

Submitting the identical active procedure again returns the existing record
without a fresh model vote or merging the incoming evidence. Changed or
withdrawn lessons do not qualify for this reuse path. The real-case repeat
completed successfully and returned the same revision, with one active console
record and unchanged supporting evidence.

The specific correction is now active after an explicit audited recovery, with
its original evidence and failed receipts preserved. Normal recall and the
local console projection were verified. This one historical backfill does not
establish automatic recovery of old runs or general business effectiveness.

Open the local workbench's Memory page and search `tccli`. The correction is
named `tccli cbs DescribeDisks: --profile command template`; active items show
**可供召回**. Scope/source filters still apply. Conflicted items can also appear,
but are not reusable. An excluded submission with no published record is not
an active memory merely because receipt processing completed.

An isolated real-model probe has demonstrated automatic learning, recall changing
from zero to one for a short request, and actual Skill invocation. A stricter
probe found that remembering only the changed flag could still omit `DiskIds`;
that result led to the complete-template requirement. The
[validation record](procedural-learning-detail.md#mvp-implementation-and-observation)
preserves passing probes, failures and remaining limits.

Inspect a bounded production sample without exporting task text or credentials:

```sh
python3 -B experiments/experience/observe.py \
  --home .agent-host/channels/dingtalk/state --limit 30
```

Check whether corrections become active memory, short follow-ups find them,
native tasks actually invoke the Skill, and known errors recur. Publication
counts are historical outcomes; a mounted or invoked Skill does not prove
business success or a causal productivity gain.

No new database schema, configuration switch or background learning worker is
needed. Tool-time interception, general entity understanding, automatic conflict
repair and outcome-driven revision remain later increments. Expand only after
the observed sample identifies the next concrete gap.

This proposal extends the [delivered experience pipeline](experience-memory.md)
and [file memory/Skill backend](file-memory-design.md).
