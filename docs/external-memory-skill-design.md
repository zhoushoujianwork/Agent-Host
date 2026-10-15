# External agent memory skill

Status: proposal, 2026-10-15. No external ingestion command or installable skill
is delivered by this document. [Implementation notes](external-memory-skill-design-detail.md).

## Goal and usage

An agent working in Codex, Claude Code, or another local entry can save useful
knowledge into Agent-Host and retrieve it in a later conversation. The external
agent continues to own its conversation and tool execution; Host owns durable
memory, its sources, publication decisions, and access scope.

For example, after this Codex conversation establishes a project decision, the
user can say “save this decision to Agent-Host.” The agent submits the decision
with the relevant user statement and conversation reference. Host returns a
receipt, then reports whether a memory became available. A fresh conversation
can retrieve the decision and inspect its source.

## Recommended design

Ship an `agent-host-memory` skill with a small CLI client backed by the running
Host. The skill teaches when to recall, what is worth retaining, how to attach
evidence, and how to interpret the receipt. Host performs validation, comparison,
blacklist checks, and publication. The skill never edits the memory files or
database directly.

One local connection profile identifies a running Host and a configured private
Agent/project memory scope. Bind to the same scope as an existing private entry
when cross-entry reuse is wanted. A new unrelated scope will not automatically
share that entry's memory. Multiple channel runtimes remain separate; the skill
does not aggregate them or silently choose a destination.

The normal workflow is:

1. Recall relevant memory when the task needs prior decisions or experience;
   read the selected record and its evidence before relying on it.
2. At a useful checkpoint, submit a small set of durable statements or lessons
   and the source excerpts supporting each one.
3. Check the receipt. Tell the user what was published, excluded, or failed.
   Receipt of material alone does not mean a memory has been saved for recall.

Explicit requests to remember are supported immediately. Automatic checkpoint
submission requires a user-configured standing preference for this destination;
installing the skill alone does not enable conversation collection. The skill
can be discovered normally without requiring explicit invocation every time.

## Memory rules that affect users

- Retain project decisions, explicit preferences, stable facts, and reusable
  procedures with conditions. Do not upload entire chat histories by default.
- Preserve who said what and when. External tool output remains evidence
  supplied by the external agent, not an execution observed by Host.
- A summary without supporting material cannot establish a successful procedure.
  Failed attempts can support bounded failure lessons.
- Submit only selected, necessary evidence, without credentials. A source link
  helps navigation; a retained excerpt keeps the basis readable later.
- Host-selected memories become effective automatically on the file backend.
  Existing disabling and blacklist behavior still applies. Repeated submission
  must not create duplicate memories.
- Local CLI access assumes the same trusted OS user. It is not a sandbox for an
  untrusted agent with arbitrary local filesystem access.

## First release and order

1. Add owner-private external ingestion, durable receipts, and restart-safe
   publication to one configured Host using the file-memory backend.
2. Expose scoped recall/read and receipt lookup through the same CLI connection.
3. Package the skill in this repository and document source-based installation;
   validate a real Codex save followed by recall in a fresh conversation.

The first release includes manual save and opt-in checkpoint save. It excludes
remote public HTTP, group sharing, full transcript synchronization, background
chat watching, native Codex harness integration, and a general plugin platform.
MCP can later expose the same Host operations if an entry lacks shell access;
it is not required to prove this first local workflow.

Existing native experience Skills are the output of learned procedures. This
integration skill is the means to submit and retrieve memory; the two have
different responsibilities. See the [existing Skill design](experience-skills.md)
and [architecture](architecture.md).
