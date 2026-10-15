# Learning from real operations

Status: proposal based on local execution evidence inspected on 2026-10-15.
This change documents a design; it does not change runtime learning behavior.
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

The existing system already preserves tool evidence, extracts procedures and
avoidance notes, evaluates them, automatically publishes selected file-backed
memory, and supplies scoped native Skills. The missing part is a reliable loop
from a specific correction to successful reuse.

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

## Proposed approach

1. **Learn small corrections.** Link a failed call, the changed command and its
   observed result. Keep a working command shape and a separate pitfall, with
   evidence for each claim. When a larger draft fails review, allow one bounded
   attempt to remove unsupported claims and review the smaller lesson.
2. **Find lessons when choosing tools.** Combine the current request with scoped
   task context. Before an unfamiliar operation, use the existing memory access
   path to search by tool, service and action. A short follow-up should not lose
   the relevant context. Native adapters retain ownership of tool execution.
3. **Distinguish durable rules from changing conditions.** CLI syntax is a
   procedural lesson. A profile's observed availability is a dated environment
   observation. A resource's state belongs to the current task. Recheck the
   latter two; a previous successful profile is not universal authorization.
4. **Close the loop with observed outcomes.** Record which memory revision was
   offered and used, whether the same failure recurred, and whether the intended
   check passed. Repeated incompatible results should trigger revision or
   suspension of the affected applicability range, with evidence preserved.

For the observed CBS case, retain the narrow correction “this invocation used
`--profile <profile>` successfully; `--cli-profile` was rejected.” Do not add
credential inspection, account switching, or resource deletion to that syntax
lesson. Build a separate full procedure only when its steps are supported.

## Scope and implementation order

**First increment:** sanitized correction episodes, small supported lessons,
one bounded repair pass, and reasoned learning receipts. Reprocess selected
historical evidence without replaying cloud operations.

**Second increment:** same-scope follow-up context and tool/action retrieval,
using existing memory access and Skill infrastructure. Verify both a continued
conversation and a fresh session.

**Third increment:** revision-specific usage/outcome records, conflict repair,
and repeated-failure feedback. Evaluate on the retained cases before expanding
to other tools or larger history.

Success means a comparable new task uses the supported syntax before repeating
the old error, produces a valid observed result, and records the linkage. It
does not mean zero future exploration: versions, accounts and resource states
can change. No new vector database, custom tool loop, automatic cloud replay,
or model-weight training is required for this first scope.

This proposal extends the [delivered experience pipeline](experience-memory.md)
and [file memory/Skill backend](file-memory-design.md).
