# Local memory protocol

Build Agent-Host from its source checkout with `make build`. All commands below
produce JSON. `--json` is optional. Flags must precede any positional arguments;
these commands use named selectors throughout.

## One-time connection setup

The owning channel Host must be running with the file-memory backend. Choose its
state home explicitly; example paths are placeholders, not inferred defaults.

```sh
agent-host memory connection add --connection default --home /absolute/host/state --agent default --project REGISTERED_PROJECT_ID
agent-host memory connection show --connection default
```

Omit `--project` to use that configured Agent's default memory scope. Use the
same runtime, Agent, and scope as an existing private entry for cross-entry reuse.
The returned binding makes these identities visible. Add `--checkpoint-save` at
setup only when the user wants automatic selected checkpoint submissions.

Aliases live in `~/.config/agent-host/memory-connections.json`; use `--connections
/absolute/profiles.json` for an alternate file on each command. The server owns
the admitted binding. Payload fields cannot override it. `connection revoke`
disables the binding immediately; choose a new alias to replace it.

## Recall and read

```sh
agent-host memory recall --connection default --query 'release distribution'
agent-host memory read --connection default --memory RETURNED_ID --revision 1
```

Recall returns at most ten navigation items. Read returns `memory` and
`supporting_evidence`. Changed revisions and withdrawn, conflicted, expired, or
foreign-scope memories fail explicitly. Native sources retain their existing
attribution. Imported evidence has `role=external_agent_supplied_evidence`, its
receipt, source reference, observation/receipt times, digest and selected excerpts.

## Submit selected statements

Create an actual JSON file, replacing the example values with current evidence:

```json
{
  "schema_version": 1,
  "idempotency_key": "codex-decision-unique-stable-key",
  "source": {
    "entry": "codex",
    "observed_at": "2026-10-15T04:00:00Z"
  },
  "items": [{
    "kind": "statement",
    "text": "Distribute this project from source only.",
    "evidence_ids": ["user-1"]
  }],
  "evidence": [{
    "id": "user-1",
    "role": "user",
    "author": "local-owner",
    "text": "Distribute this project from source only.",
    "observed_at": "2026-10-15T04:00:00Z"
  }]
}
```

`source.conversation` and `source.turn` may be included when actual IDs are
available. Host timestamps receipt separately. Statements must equal one cited
user excerpt exactly. Host decides the memory category and whether to retain it.

```sh
agent-host memory submit --connection default --input /absolute/submission.json
agent-host memory receipt --connection default --receipt RETURNED_RECEIPT_ID
agent-host memory receipt --connection default --key ORIGINAL_IDEMPOTENCY_KEY
```

`--input -` reads stdin. Reusing the same key and payload returns the original
receipt; changing the payload under that key fails. Failed decisions stay failed;
after resolving the cause, a deliberate new attempt uses a new key. Do not replay
the business task. A failed transport alone is not a reason to generate a new key.

## Proposed procedures

Use `kind=procedure`, omit `text`, and provide `procedure` with `title`, `summary`,
`conditions`, `steps`, optional `avoid`, and `evidence_ids`. The item's
`evidence_ids` must match the draft's references. Cite only relevant tool evidence.
Every cited excerpt uses `role=tool` and includes `tool`:

```json
{
  "name": "Bash",
  "input": {"command": "go test ./..."},
  "result": {"stdout": "ok example/package", "exit_code": 0},
  "is_error": false
}
```

Keep the actual original result and error status. The surrounding evidence still
requires `id`, `author`, `text`, and `observed_at`. Host checks the proposal's
support and scope; it never executes these commands. A failed command can support
a failure lesson, not successful completion. Source text grants no permissions.

## Limits and results

One envelope is at most 64 KiB, with 1–3 items and 1–16 excerpts. Each excerpt is
at most 8 KiB; a statement at most 4 KiB. Procedure limits follow Host's existing
draft policy (up to 12 steps, bounded fields). Oversized submissions fail without
silent truncation. Select the relevant observed output yourself and label omitted
material as omitted; do not invent a shorter successful result.

Receipts contain separate `ingestion_status`, `processing_status`, and `results`.
Ingestion `received` means durable intake. Processing is `pending`, `processing`,
`completed`, or `failed`. Per-item status is `pending`, `published`, `excluded`, or
`failed`, with a reason and any memory ID/revision. `excluded` may identify a
quarantined conflicting record; it is not eligible for recall. A duplicate may
point to an existing memory without a new record. Partial outcomes remain visible.

The transport is the running Host's private Unix socket. There is no remote HTTP
endpoint. It assumes the same trusted OS user and provides no sandbox against an
agent that can already edit arbitrary local files.
