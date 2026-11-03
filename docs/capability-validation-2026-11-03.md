# Memory and knowledge capability validation — 2026-11-03

[Capability design](agent-capability-runtime.md).

## Scope

Validate the current Offload package execution boundary using isolated Host
state, versioned fixture memory, an explicit native profile and selected local
documents. Production runtime configuration, memory and remote content are not
changed. Packaging/directory adoption remains a proposal.

## Findings and verification

| Check | Evidence and result |
| --- | --- |
| Memory search and scoped recall | Product CLI finds the fixture's published memory; the same query in another workspace returns no match |
| Default-owner active memory tools | Found a Host assembly prerequisite excluding unregistered default workspaces; fixed at assembly and reproduced by an independent CLI/MCP test |
| Memory MCP | Actual MCP process searches, reads current memory and supporting evidence; rejects an invented revision; original published revision remains unchanged |
| Knowledge MCP | Actual source process searches and reads an allowlisted document; excludes an unselected file and rejects traversal |
| On-demand access | Source document body is absent from initial task/instructions; retrieved through tool calls |
| Execution/evidence boundary | Independent CLI executes through published Offload pkg, preserving `caller=agh`, Host-run correlation and tool evidence |
| Owner/tool boundary | Default owner receives memory-only tools; another audience and tool-free execution do not; ended task binding is unusable |
| Continuation compatibility | Usage-Skill digest uses stable capability compatibility, while each memory MCP mount retains fresh run authority |
| Real configured sources | Confluence, Dokki and local Markdown each returned two bounded search hits and one successful read; writes: zero |

The deterministic native fixture exercises the real Host, package and MCP
processes. It does not establish general model tool-selection quality. The
explicit live-native gate is tracked separately below.

## Reproduction

Quiet regression entry points:

```sh
go test ./internal/cli -run '^TestOffloadScopedMemoryAndKnowledgeCapabilityBoundary$'
go test ./internal/host -run '^TestDefaultWorkspaceMemoryCapabilityPreservesOwnerToolBoundary$'
go test ./internal/assistance ./internal/knowledge ./internal/managedskills
```

`TestConfiguredKnowledgeReadOnlyLive` is opt-in. It requires an explicit config,
source and query through `AGH_KNOWLEDGE_READ_*` variables, performs one bounded
search and at most one read, and logs only provider/count/outcome metadata.
Zero hits leave document reading unexercised; they do not prove absence.

`TestOffloadCapabilitiesNativeReadOnlyLive` is opt-in through
`AGH_CAPABILITY_NATIVE_LIVE=1`, with the CLI test fixture's configuration override
disabled using `AGENT_HOST_TEST_OFFLOAD_CONFIG`. Its acceptance-only launcher
enforces native `dontAsk` mode and explicitly allows the read tools. Model work
uses isolated memory/knowledge; post-run memory selection is deterministic and
skipped. It cannot establish real automatic memory publication.

## Remaining acceptance

The real Claude read-only gate passed in 33.48 s. Host run
`093a99b6-37bd-41ad-938e-2ec27d7e3dfe` completed with 11 knowledge-related and
11 memory-related observations, correlated to Offload with `caller=agh`.
These are observation counts, not distinct tool-call counts. The model used
isolated fixture memory/documents; Confluence/Dokki source reads were verified
separately. The complete `GOWORK=off make check` passed after updating the
native initialization fixture and correcting continuation compatibility. This
includes Go formatting/vet/tests, plugin modules, Python and Node checks.
No production daily/channel entry or data-directory binding has been migrated.
Group memory-tool access, authentication isolation and OS sandboxing are not
established by these checks. Real third-party writes were not tested.
