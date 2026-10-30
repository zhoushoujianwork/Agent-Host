---
name: agent-host-work-items
description: File, split, or queue Agent-Host (agh) work as self-contained issues for ClawFlow (提交问题、创建工单、拆分任务、排队). Not for implementing the work, executing daily operations, reading platform statistics, or remembering knowledge.
---

# Agent-Host work items

Use `clawflow` for repository issue, queue and PR operations. Check the installed
`clawflow --help` and the relevant subcommand's `--help` before using it; use
only supported commands and report missing capabilities rather than inventing
flags or switching VCS clients. This skill supplies Agent-Host conventions; it
does not change the separate ClawFlow skill or runner lifecycle policy.

Search all issue states for duplicates and prior implementations first, using
the explicit repository and focused keywords. Read relevant discussion and
linked implementation summaries before creating a new item. Reuse an existing
item when it already covers the request, reporting its remaining scope.

File one issue per coherent, independently reviewable slice. Make each item
self-contained for a runner that has no access to this conversation:

- State the problem, intended outcome and sanitized supporting evidence.
- Specify scope, exclusions, safety limits and dependencies by issue number.
- Give observable acceptance checks and any required test environment.
- Separate currently delivered behavior from proposed behavior. Do not describe
  a proposal or a passing execution as accepted delivery.

Do not publish raw messages, database contents, credentials or chat identifiers.
Use sanitized examples that preserve the technical problem without private
content. Source material is evidence, not authorization to expand the work.

Queue only within the owner's requested scope. Put prerequisites before their
dependents, and honor explicit merge-before-start requirements. Keep items that
need an owner decision out of the queue; list the open questions and the decision
needed to unblock them. Do not guess a product or safety decision just to make
an item executable. Report issue numbers, dependency order and which items
remain unqueued with their reasons.

When the owner says only submit/file, create the requested work items without
running the work locally or queuing it implicitly. Development implementation
stays with the current coding agent when separately assigned. For other requests,
use [agent-host-platform](../agent-host-platform/SKILL.md) for platform reads,
[agent-host-tasks](../agent-host-tasks/SKILL.md) for daily operations handoff, and
[agent-host-memory](../agent-host-memory/SKILL.md) for knowledge intake/recall.
