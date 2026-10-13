# Execution experience as native skills

Status: proposed on 2026-10-13 for [issue #1](https://github.com/zhoushoujianwork/Agent-Host/issues/1).
No generated skills or native skill mounting are delivered. See the
[implementation notes](experience-skills-detail.md) and the
[delivery order](release-follow-up.md#issue-1-delivery-plan).

## Goal and usage

Let the native assistant reuse an observed procedure when a similar task arrives.
For example, a saved repair-and-test procedure becomes an available skill in a
fresh conversation for the same project, Agent and audience. The user can inspect
its source memory and disable it through the existing memory controls.

## Core approach

SQLite memory remains authoritative. Generate one skill per eligible execution
procedure, preserving its conditions, steps, evidence references and evidence
strength. Facts and reply preferences remain ordinary memory. Start with direct
rendering of existing procedures; no second model rewrites or combines lessons.

Only skills allowed for the current task are made available to the native harness.
Keep generated files outside shared project skill folders and shared native user
profiles. Hand-written skills keep their own files and names. A generated skill
does not grant tools or permissions, and the native harness owns its invocation.

Disabling, expiry or conflict removes a procedure from future skill sets. A change
to that set starts a fresh native conversation with an appropriate continuity
notice; removing a file cannot erase instructions already loaded into history.

## First scope and order

1. Complete the [automatic publication and blacklist increment](experience-memory.md),
   including protection against equivalent disabled lessons returning.
2. Generate a bounded set of procedure-only skills from eligible memory, with
   inspectable provenance and rebuildable files.
3. Mount that exact set for Claude tasks and verify real native discovery and use.
   Keep extraction and tool-free tasks free of generated skills.
4. Validate save, fresh-task use, update, disable and recovery as one loop.

Historical manually accepted experience can qualify if it passes current
eligibility checks. Its original human-review evidence stays unchanged.
Topic grouping, generated scripts, public publication and Codex mounting follow
only when needed; the Codex adapter is still planned.

## Acceptance

- A published procedure is discovered and used by a fresh native task in its
  authorized scope, with inspectable memory and evidence links.
- Other projects, Agents and audiences receive no unauthorized generated skill;
  an owner's private procedure never enters a group skill set.
- Updates and disabling affect the next task, including resumed conversations;
  equivalent re-extraction cannot restore a disabled lesson.
- Export failures remain visible and independently recoverable without replaying
  the business task. Automated cases and native acceptance are reported separately.
