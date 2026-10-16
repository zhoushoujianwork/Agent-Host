# External agent memory skill

Status: implemented, 2026-10-15. Local connections, external intake, durable
receipts, scoped recall/read and the source skill are available. Validation and
operational limits are in the [implementation notes](external-memory-skill-design-detail.md).

## Goal and usage

An agent working in Codex, Claude Code, or another local entry can save useful
knowledge into Agent-Host and retrieve it in a later conversation. The external
agent continues to own its conversation and tool execution; Host owns durable
memory, its sources, publication decisions, and access scope.

For example, after this Codex conversation establishes a project decision, the
user can say “save this decision to Agent-Host.” The agent submits the decision
with the relevant user statement and conversation reference. Host returns a
durable submission confirmation. The reporting agent says "Submitted to
Agent-Host" and finishes; Host handles adoption and publication afterward.
A fresh conversation can retrieve an adopted decision and inspect its source.

## Recommended design

The [agent-host-memory skill](../skills/agent-host-memory/SKILL.md) uses a small
CLI client backed by the running Host. It teaches when to recall, what to retain, how to attach
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
3. Confirm successful submission and finish. Do not wait for adoption or poll
   processing results. Host handles them in the background; explicit diagnostics
   and verified document imports can still inspect the receipt.

Submission succeeds only after Host durably receives the material. Invalid input,
access failures and storage errors still fail submission. Successful submission
does not promise that a memory is already available for recall.

Explicit requests to remember are supported immediately. Automatic checkpoint
submission requires a user-configured standing preference for this destination;
installing the skill alone does not enable conversation collection. The skill
can be discovered normally without requiring explicit invocation every time.

## Memory rules that affect users

- Retain project decisions, explicit preferences, stable facts, and reusable
  procedures with conditions. Do not upload entire chat histories by default.
- Preserve who said what and when. External tool output remains evidence
  supplied by the external agent, not an execution observed by Host.
- Every new memory needs a supported object, retained separately from the quote.
  Host extracts missing subjects from user/tool evidence; unresolved subjects
  and extraction failures block publication explicitly.
  Recall/read expose its name and source; existing active records support an
  audited association correction. Object metadata never grants permissions.
- A summary without supporting material cannot establish a successful procedure.
  Failed attempts can support bounded failure lessons.
- An explicitly requested saved-document import preserves quotations as source
  notes, labeled “来源笔记 · 未复验” in the console. They can be recalled with their
  original references and dates. They do not establish current facts, permissions,
  or successful command execution. Admitted knowledge takes precedence over
  imported references when both apply. The memgov importer selects saved topic notes,
  redacts credential patterns, and verifies publication by reading them back.
- Submit only selected, necessary evidence, without credentials. A source link
  helps navigation; a retained excerpt keeps the basis readable later.
- Host-selected memories become effective automatically on the file backend.
  Existing disabling and blacklist behavior still applies. Repeated submission
  must not create duplicate memories.
- Local CLI access assumes the same trusted OS user. It is not a sandbox for an
  untrusted agent with arbitrary local filesystem access.

## Setup and first release

Run `make install` to build the CLI, link it into `~/.local/bin` and install this
skill for detected Codex, Claude Code, OpenCode, Gemini CLI, Cursor and GitHub
Copilot setups. Existing custom targets are preserved; each outcome is reported.
New sessions can discover the skill. Keep this checkout in place, since the
installed links point to its source and built binary.

With private channel configuration in place, `make install` also starts the Host
and enables automatic user-login startup on macOS or Linux with user systemd.
Use `INSTALL_ARGS=--no-autostart` for CLI/skills only. See the
[runtime guide](service-install.md) for foreground operation and service removal.
Then configure one connection:

```sh
bin/agent-host memory connection add --connection default --home /absolute/host/state --agent default
bin/agent-host memory connection show --connection default
```

Add `--project REGISTERED_PROJECT_ID` for an allowed registered project, or omit
it for the Agent's default scope. Add `--checkpoint-save` only when automatic
selected checkpoint submissions are wanted. Complete examples are in the
skill's [protocol reference](../skills/agent-host-memory/references/protocol.md).
For explicit clients or another CLI directory, use
`make install INSTALL_ARGS="--agents codex,claude --bin-dir /absolute/bin"`.

The first release includes manual save and opt-in checkpoint save. It excludes
remote public HTTP, group sharing, full transcript synchronization, background
chat watching, native Codex harness integration, and a general plugin platform.
MCP can later expose the same Host operations if an entry lacks shell access;
it is not required to prove this first local workflow.

Existing native experience Skills are the output of learned procedures. This
integration skill is the means to submit and retrieve memory; the two have
different responsibilities. See the [existing Skill design](experience-skills.md)
and [architecture](architecture.md).
