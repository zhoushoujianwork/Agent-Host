---
name: agent-host-memory
description: Save durable decisions, preferences, facts, evidence-backed procedures, and explicitly requested source-note imports to a configured local Agent-Host, and recall its memory for current work. Use when asked to remember or import knowledge into Agent-Host or when prior project knowledge would help.
---

# Agent-Host memory

For execution of a clear routine task, use the separate `agent-host-tasks`
skill when installed. This skill handles knowledge intake and recall; memory
connection aliases do not configure CLI chat destinations.

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
- Every published memory requires a source-grounded object. Supply the item's
  `objects` when the subject is established in context; cite the actual user/tool
  evidence. Host automatically extracts missing objects with its configured
  tool-free native model before publication. Systems, services, projects, tools,
  people and organizations are supported; a global personal preference identifies
  its attributed author. Do not guess from cwd, substitute generic labels, or
  invent a subject in the exact statement. An unresolved subject/provider failure
  stops publication explicitly. Recall/read show retained associations separately.
  At the user's request, `memory associate` can correct an existing active record's
  object context with a current revision and attributed user evidence; see the
  protocol. This changes metadata, not admission or the original statement.
  Host merges case variants and technical system/service/tool classifications,
  and displays non-person Latin names in uppercase. Submit source spellings;
  never uppercase quoted commands or source statements.
  `memory extract-objects` can repair an existing active record using only its
  retained evidence and expected revision, without replaying its source task.
- A procedure proposes a short lesson with conditions, steps, and cited external
  tool call/result excerpts, including explicit error status. Preserve failure
  and uncertainty. Assistant claims alone cannot establish successful execution.
- At an explicit request to import saved documents, use `source_note` for exact
  source quotations. Retain original dates and references, redact credentials,
  and identify the actual document read. An agent-written note is not a user
  statement or proof of the commands it describes. Source notes are unverified
  references and do not replace admitted facts or procedures. The protocol has
  a resumable memgov importer; review its prepared manifest before submission.
- Use actual conversation/turn IDs when available; omit missing IDs. Never
  fabricate a source, native run, observed output, or timestamp of the original
  event. If quoting the current visible message without a source timestamp,
  use the current observation time and make clear it is the time of observation.

Write the JSON payload to a temporary file without shell interpolation, submit
it once with a stable idempotency key. When `ingestion_status=received`, report
"Submitted to Agent-Host" and stop; retain the receipt ID. Do not poll for
selection/publication or report those outcomes during ordinary submissions.
On a transport timeout, look up that key first: a receipt with
`ingestion_status=received` confirms submission regardless of processing outcome.
Resend only the identical payload and key if the receipt is absent; other lookup
errors leave submission unconfirmed and must be reported. Intake errors remain
submission failures. Inspect processing only when explicitly requested for
diagnostics or when verifying a saved-document import as described above.

Submission success means Host durably received the material, not that memory is
already available for recall. Host owns subsequent selection and publication;
the reporting agent's work ends at intake confirmation. Publication does not
prove business success. Host performs selection, duplicate/conflict handling, and blacklist
admission; never edit its files/database or fabricate a run to bypass that path.
