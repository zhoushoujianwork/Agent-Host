# Offload memory implementation details

[User-facing design](offload-memory-plugin.md).

## Configuration and lifecycle

The existing private version 1 Offload profile accepts:

```json
"memory": {
  "enabled": true,
  "processing_enabled": true,
  "provider": "typesafe",
  "model": "jev-1.13.0",
  "timeout": "30s"
}
```

Defaults when enabled are processing enabled, the official TypeSafe provider,
the existing Jev default model, and 30 seconds for closeout. `provider: custom`
requires an explicit `endpoint` (HTTPS or loopback HTTP). Only the selected
provider's existing credential variable is read from the service process:
`TYPESAFE_API_KEY` or `JEV_API_KEY`. Missing credentials fail during startup;
no fallback provider is selected. These credentials are separate from
Offload's native execution-provider credentials and are never put in prompts.

Use `processing_enabled: false` for local retrieval only, without Jev credentials.
Omitted memory or `enabled: false` loads neither component and preserves the old
configuration fingerprint. Enabled configuration enters the runtime/queue
fingerprint; changing it does not migrate existing native sessions. Records and
source evidence are preserved on rollback. A selected file-memory backend still
requires the existing explicit migration; it never silently falls back to SQLite.

## Retrieval and publication

The internal `MemoryPlugin` uses `Store.Retrieve` and `SaveContext`, with at most
16 unique records and a 16 KiB JSON context budget. Source IDs, revisions, authors
and exact text are retained. Oversized records are excluded rather than edited.
The native prompt frames memory as attributed data, never permission grants or
verified operational success. Personal preferences/constraints in group recall
are limited to their recorded author. Partition and scope filters precede search.

Processing reuses `memory.Decider` and `Store.FinalizeMemory`; it does not add a
model/tool loop or a public plugin loader. Only exact authenticated user input
can become a user statement. Quotes, supplements and missing sender attribution
are excluded. Assistant output is not classified or used as evidence. Existing
duplicate evidence, conflict quarantine and invalidation rules remain in force.
Comparison is bounded by the classifier's existing 100-record budget; overflow
excludes publication with a failure receipt rather than silently dropping checks.

A memory generation fingerprint is checked transactionally at publication for
both storage backends. Concurrent changes or expiry invalidate the decision; no
automatic retry or stale publication occurs. Disabled records participate in
comparison without becoming recall-eligible or being restored automatically.

## Outcomes and recovery

New runs record `offload_memory_mode` as `recall` or `recall_process`; only an
Offload run with an Offload runtime fingerprint can claim these modes. Existing
run fields and legacy resumption rules retain their previous meanings.
Experience extraction is explicitly skipped. Retrieval-only processing is an
audited skip; enabled processing records pending/saved/unchanged/failed separately
from native execution and acceptance.

The request-scoped execution-ready handler delivers the saved answer before
bounded closeout. Observer or classifier failures do not replay native work.
Native continuation remains available when Offload memory closeout is pending
or failed; legacy engines retain their existing memory gate.

To retry only memory after releasing the channel state's writer lock:

```sh
agent-host run retry-memory --offload-config /absolute/offload.json \
  --home /absolute/channel-state --agent default --audience owner RUN_ID
```

The command uses the original run's scope and source, and never starts Offload.
Successful finalization is idempotent. Use existing memory invalidation commands
for withdrawal; native history remains under its owning engine.

## Acceptance

Headless fixtures cover fresh-session recall with exact source attribution,
invalidation/conflict exclusion, duplicate publication, private/group/Agent/scope
isolation, retrieval-only and disabled modes, startup configuration errors,
answer-before-classification ordering, classifier failure, cancellation, stale
snapshots and explicit memory-only retry. Local HTTP fixtures exercise the real
Jev client/classifier contract without external provider calls. Live model quality
and account activation remain separate acceptance steps.
