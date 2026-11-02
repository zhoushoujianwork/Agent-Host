# Offload execution boundary — implementation notes

[Main direction and scope](offload-execution-boundary.md).

## Package selection

`internal/offloadexec.Library` is the only production execution client. It imports
`github.com/zhoushoujianwork/offload/pkg/execution`, pinned to v0.7.0. Default
selection reads the owner-private XDG Offload config (normally
`~/.config/offload/config.json`) and resolves its explicit `default` profile.
Version 2 selector files name `config_path`, `profile`, optional `model`,
`timeout` and optional lightweight memory settings. Version 1/`command` and
`execution_config`/`--execution-config` are rejected, with no fallback.
Agent catalogs use `offload_config` for per-Agent selection.

The selected profile is snapshotted before execution. Configuration, package
version and execution policy participate in compatibility fingerprints; secrets
only enter the private digest input. Profile changes require a new compatible
native conversation. Missing capabilities, credentials or launch failures never
select a weaker backend or replay the request.

## Host adapters and lifecycle

The lightweight `harness/offload.Adapter` delegates through the same library.
The full Host's `harness/nativepolicy.Adapter` retains Host validation, project
instruction assembly, skill digest checks, native event decoding and completion
gates. `Request.Native` maps executable/profile paths, fresh/fork session IDs,
permissions, tool rules, MCP servers, Skills, structured extraction and images.
Only metadata probes (`--version`, `--help`) remain Host-owned subprocesses.
All actual model execution and native process cleanup belongs to Offload.

The package's caller-context mode keeps task stdin separate from private Host
instruction files, without adding a delegation role. Credentials always come
from the selected Offload profile. Restricted reads exclude ambient instructions
and hooks and verify observed tool inventories. Structured extraction remains
fresh, tool-free, safe-mode and nonpersistent. Unsupported Codex native controls
fail before launch; ordinary Codex requests retain package behavior.

Package event callbacks feed the existing bounded protocol decoder. A matching
initialized session, successful terminal result, complete stream and actual
successful process exit are all required. Image-bearing echoes are redacted
before Host observations are journaled. Slow observers do not truncate buffered
output. Held stdout is bounded and remains an unknown outcome; process exit and
callback errors preserve their original Go error chains. No partial output is
promoted to completion.

## Attribution and durable evidence

All business calls carry `Caller=agh` and `CallerRunID=agh:<Host run ID>`.
Internal extraction/classification calls receive `agh:internal:<UUID>` when no
business ID is supplied. Attribution never comes from untrusted source content.
`RecordHistory` is always enabled. SDK run IDs, canonical profile/backend,
usage, raw result and history-write outcome are retained in private Host
`offload.result` evidence; public `offload.history` only reports status and IDs.
History failure is independent of execution and never triggers replay. Full Host
auxiliary runners use the same boundary; configured Agent runners pin their
selected library. Global discovery uses the explicit Offload default.

## Validation and rollout

Headless fake-native checks cover default selection, caller/history correlation,
fresh/continued conversations, channels, memory, MCP, Skills, images, structured
extraction, permissions, forks, protocol failures, cancellation/descendant cleanup,
slow observers and native exit causes. The producer package must also build from
an independent consumer module. `make check` is required before committing.
Historical readers remain for previously recorded ACP events; the ACP executable
adapter and CLI compatibility client are removed. Production rollout, real
channel sends, actual model acceptance and desktop acceptance require their own
scope; fixture checks do not establish those outcomes.

### Bounded native package canary (2026-11-02)

An independent consumer of the published v0.7.0 module ran the configured Claude
Sonnet default once in a disposable workspace, with tools disabled. It returned
the exact acknowledgement, native exit 0 and status `ok`. Run
`00a889ac9f98089aa6344415e5d2a204` had exactly one Offload history record with
`caller=agh` and `caller_run_id=agh:validation:1793611015735159000`.
This proves the selected profile's package invocation, attribution and terminal
history append. It does not establish full Host MCP/Skills live behavior, channel
sends, desktop rendering or production migration.
