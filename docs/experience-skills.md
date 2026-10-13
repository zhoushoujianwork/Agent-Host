# Execution experience as native skills

Status: revised planning on 2026-10-13 for [issue #1](https://github.com/zhoushoujianwork/Agent-Host/issues/1).
No generated skills or native skill mounting are delivered. See the
[implementation notes](experience-skills-detail.md) and the
[delivery order](release-follow-up.md#issue-1-delivery-plan).

## Goal and usage

Let the native assistant reuse an observed procedure when a similar task arrives.
For example, a saved repair-and-test procedure becomes an available skill in a
fresh conversation for the same project, Agent and audience. The user can inspect
its source memory and disable it through the existing memory controls.

## Core approach

The [file memory proposal](file-memory-design.md) owns the new storage and
delivery sequence. Published Skill files become authoritative procedural
knowledge; native skill packages and retrieval indexes are derived. This replaces
the previous SQLite-authoritative export proposal. Facts and reply preferences
remain separate knowledge files, with their own lifecycle.

Preserve conditions, steps, evidence references and evidence strength. Start with
direct rendering of eligible procedures; skill packaging adds no second model
rewrite and does not combine unrelated lessons.

Only skills allowed for the current task are made available to the native harness.
Keep generated files outside shared project skill folders and shared native user
profiles. Hand-written skills keep their own files and names. A generated skill
does not grant tools or permissions, and the native harness owns its invocation.

Disabling, expiry or conflict removes a procedure from future skill sets. A change
to that set starts a fresh native conversation with an appropriate continuity
notice; removing a file cannot erase instructions already loaded into history.

## First scope and order

1. Establish versioned file storage, reversible migration and scoped retrieval
   under the [new memory design](file-memory-design.md).
2. Complete automatic publication and blacklist protection, then publish
   procedure-only Skill files and build bounded native packages with provenance.
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
