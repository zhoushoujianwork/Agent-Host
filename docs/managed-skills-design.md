# Host-managed Skills and plugin guidance

Status: implemented in source, 2026-10-22; local native acceptance uses isolated
read-only fixtures. Production rollout and operator adoption are separate.
The [technical details](managed-skills-design-detail.md) define assembly, lifecycle
and acceptance. See the [architecture](architecture.md)
for the delivered foundation.
The [Platform Skills Web design](platform-skills-web-design.md) now provides a read-only
Settings catalog, pinned authored-Skill export and the Agent profile's historical
effective-catalog view. The platform owns lifecycle management; the Web page
focuses on inspecting and exporting its results. Cross-source export and current
readiness remain planned.

## Goal and usage

Make **Skill the final reusable form of every handling procedure**, managed by
Agent-Host (`agh`). A completed operation can produce a reusable method; after
the appropriate review and publication, the default Agent can discover and use
that Skill on the next relevant task without the user naming or installing it.

An enabled plugin must supply both its available tools and guidance explaining
when and how to use them. The default Agent should know which plugins are ready,
which operations they support, and how to load their complete instructions.
Enabling a plugin makes its capability available; it does not authorize a
particular approval, outgoing message or infrastructure change.

For example, one SFTP delivery Skill covers the common workflow and selects the
China or US reference from the actual ticket. It retains separate environment
settings, validation and delivery steps. A later task reuses the method while
freshly resolving the customer, resource IDs, accounts and allowed changes.

The platform manages admission, publication and selection. Users can inspect
and export a version without manually registering or assigning every learned
Skill. Export is an independent snapshot; changes to the copy do not update the
platform. Automatic consolidation of related methods remains a separate planned
capability, with evidence and source-specific admission preserved.

## Core design

- **One managed catalog.** agh registers authored operational Skills, published
  experience Skills and enabled-plugin usage Skills. Each has an identity,
  version, owner, provenance, enablement and review state. A directory link alone
  does not establish managed publication or readiness.
- **Active loading by the default Agent.** Every execution receives the effective
  catalog and a small always-loaded guide to selection. The Agent proactively
  loads the relevant Skill and references before using its procedure or plugin.
  Full bodies are loaded progressively; all active Skills are discoverable,
  rather than depending solely on a top-k memory search.
- **Plugins explain their capabilities.** A ready enabled plugin contributes its
  usage Skill and the tools it describes as one consistent capability. Disabled
  plugins contribute neither active usage instructions nor tools. Missing or
  inconsistent guidance is an explicit readiness failure.
- **Methods and evidence remain distinct.** Facts, preferences, decisions,
  source notes and execution evidence keep their existing roles. Reusable
  handling steps ultimately live in Skills; a chat answer, saved file, memory
  receipt or rejected experience is not automatically a published method.

## Delivered scope

1. The agh-managed catalog provides explicit adoption, review, publication,
   enablement, withdrawal and rollback. It pins source files and review evidence
   without silently adopting or overwriting native Skills.
2. The shared execution path assembles the complete effective catalog and
   selection guide for the default Agent. It preserves identities, session
   compatibility and observations of actual Skill/reference loads.
3. Ready Email, DingTalk workspace, knowledge, owner-context and conversation-history
   adapters supply usage Skills bound to their actual public tool definitions.
   Configuration changes participate in readiness and session compatibility.

Use `skills export` for an exact pinned authored-Skill snapshot. Existing
`skills import` remains an explicit adoption/maintenance tool; `chat --skills-root`
selects the managed store. Native profiles keep their existing mounts during
adoption. See
the [operator commands and validation](managed-skills-design-detail.md#implemented-storage-and-operator-commands).
This increment does not add a public plugin SDK, marketplace, execution engine
or automatic execution of generated scripts.
