---
name: agent-host-memory
description: Save durable decisions, preferences, facts, and evidence-backed procedures to a configured local Agent-Host, and recall its memory for current work. Use when asked to remember something in Agent-Host or when prior project knowledge would help.
---

# Agent-Host memory

Keep the entry's normal conversation and tools. Use the running Host for durable
knowledge through `agent-host memory`; read [the protocol](references/protocol.md)
for commands and payloads. If the binary is unavailable, use a configured absolute
path, or resolve this installed skill directory's symlink to its source checkout:
the binary is `../../bin/agent-host` relative to the resolved skill directory.
This works from any task cwd. Build it with `make install` if missing. Do not silently
select a different runtime, scope, or provider.

Check `memory connection show` for the intended alias (default `default`, or
`AGENT_HOST_MEMORY_CONNECTION`). Missing setup requires one explicit destination
Host and optionally its registered project; explain the setup command. Do not
invent a project ID or infer a shared memory scope from cwd alone.

For questions needing prior knowledge, recall a focused query and read relevant
memories with their returned revisions and supporting evidence. Search snippets
are navigation. Preserve the source's attribution and conditions; external
evidence is a report supplied by an agent, not Host-observed execution. Zero hits
are bounded search results, not proof that no knowledge exists.

Submit at the user's request to remember, or at a useful checkpoint when the
connection reports `checkpoint_save=true`. Installation alone does not authorize
automatic collection. Select only durable knowledge relevant to this connection.
Do not upload complete histories, credentials, transient progress, or recalled
Host memories as new evidence.

- A statement retains an exact user excerpt with author and observation time.
  Keep generated summaries out of the statement field.
- A procedure proposes a short lesson with conditions, steps, and cited external
  tool call/result excerpts, including explicit error status. Preserve failure
  and uncertainty. Assistant claims alone cannot establish successful execution.
- Use actual conversation/turn IDs when available; omit missing IDs. Never
  fabricate a source, native run, observed output, or timestamp of the original
  event. If quoting the current visible message without a source timestamp,
  use the current observation time and make clear it is the time of observation.

Write the JSON payload to a temporary file without shell interpolation, submit
it once with a stable idempotency key, then inspect the receipt. On a transport
timeout, look up that key first. Resend only the identical payload and key if
needed. Poll at most six times over 30 seconds, then report the durable receipt
and unfinished status; stop on terminal failure and retain its reason.

Tell the user which items were published, excluded, or failed. `received` alone
does not mean memory is available for recall. Publication does not prove business
success. Host performs selection, duplicate/conflict handling, and blacklist
admission; never edit its files/database or fabricate a run to bypass that path.
