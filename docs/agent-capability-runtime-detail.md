# Agent capability runtime: implementation notes

[Main design and scope](agent-capability-runtime.md).

## Existing seams

Host resolves Agent/audience/project identity before assembly. `host_context`
already exposes `search_context`, `read_context` and `read_context_evidence`.
The binding derives its partition and scope from the admitted persisted run;
the model does not choose another Agent, audience or memory home in tool input.
Default-owner conversations use the memory-only catalog. Project contexts can
add their currently authorized matter sources. Groups, tool-free runs,
unresolved attribution and joint-project limitations retain their current rules.

Knowledge already has source-specific MCP processes, explicit local-file
allowlists, source/configuration fingerprints, bounded results and provenance.
Source roots do not become native cwd/additional directories. Managed plugin
Skills already bind usage guidance to the concrete mounted tool contract.

The validation found an assembly gap: the memory-only binding supported a
default workspace, but Host mounted it only for a registered project or card
task. The fix removes that extra prerequisite while preserving the owner,
tool-availability, attribution and project-count checks. Binding resolution
still validates the admitted task and refuses terminal or inconsistent grants.
Usage-Skill compatibility uses the binding's stable session key rather than its
per-run authorization fingerprint. This preserves unchanged capability catalogs
across continuation while MCP arguments still carry the current run and grant.

## Proposed component contract

Start with the two concrete built-in capabilities, not a dynamic loader:

| Capability | Tools and data | Ownership |
| --- | --- | --- |
| Memory | Existing scoped search/read/evidence; automatic recall; published Skill references | Host admission, versioning and publication |
| Knowledge | Selected local/Dokki/Confluence source tools, usage Skill and pinned source binding | Connector retrieval; Host source authorization |

Each managed Agent references explicit capability IDs and deployment bindings.
Assembly resolves those references against ready configured services and the
current request's authority. Missing dependencies or withdrawn grants fail
explicitly. The effective capability set, usage-Skill digests and source policy
participate in session compatibility. Tools and guidance are withdrawn together.

Current unified datasource configurations have a globally enabled catalog during
Beta; the proposed managed-Agent binding must not be reported as delivered
isolation. Migration must record the current effective source set explicitly,
then compare it before admitting traffic. Adding a new configured datasource
must not silently expand every managed Agent's grants.

Provider credentials stay in owner-private deployment bindings. Capability
instructions and model-visible catalogs contain source descriptions and tool
names, not credential values or full source bodies. Connector reads retain
source receipts and coverage. Host memory publication remains a separate
post-execution outcome; a retrieved document is not execution evidence.

## Proposed directory policy

Reuse the existing layout instead of moving all storage into a new tree:

```text
<agh-root>/
  config/agents/<agent>/             # role and explicit capability selections
  config/capabilities/               # built-in service/deployment bindings
  config/users/                     # explicitly selected owner preferences
  memory/                           # authoritative entries, methods, manifests
  managed-skills/                   # existing managed publication catalog
  channels/<channel>/state/          # channel operational records
  cli-state/                        # separate CLI operational records
  cache/memory/                     # rebuildable retrieval indexes
  cache/workspaces/<run>/            # private assembled context and Skill views
  workspace/agents/<agent>/          # explicitly configured default workspaces
```

These are planned binding locations, not directories created by this change.
Owned relative paths resolve against the selected root, never the caller's cwd.
External repositories and knowledge roots remain explicit references. Keep
Agent-owned output/state separate from shared source data. Preserve existing
memory IDs, revisions, provenance and channel/CLI partitions during relocation.

The remaining implicit paths include Agent native-profile fallback to
`CLAUDE_CONFIG_DIR`/`~/.claude`, default native Skill inheritance and configuration
discovery defaults. Managed profiles should use explicit bindings and Skill
selections. Project instructions remain associated with authorized project cwd.
Do not implement isolation by changing global `HOME` or copying a user's whole
native profile. Direction A retains current native authentication/session
storage and introduces no Docker daemon, generic plugin service or new model loop.

## Lifecycle and acceptance

Use existing native-session MCP lifecycle and per-run managed context snapshots.
Closing a task tears down owned subprocesses through the existing Offload
cancellation boundary. Configuration/revocation checks run before dispatch and
source tools revalidate their pinned bindings on calls.

Before directory adoption, compare effective tools, sources, memory retrieval,
credential references and session compatibility at the old and proposed roots.
Exercise missing credentials, disabled sources, cache rebuild, cross-Agent and
cross-audience denial, local path escape and source revocation. Prepare rollback
bindings before switching one entry; do not replay business tasks or rewrite
old audit records to repair a memory or directory migration.

[Current verification and limitations](capability-validation-2026-11-03.md).
