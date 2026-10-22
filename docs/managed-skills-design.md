# Host-managed Skills and plugin guidance

Status: proposed architecture direction, 2026-10-22. This document changes the
target design, not the running service. The [technical details](managed-skills-design-detail.md)
define assembly, lifecycle and acceptance. See the [architecture](architecture.md)
for the delivered foundation.
The [Platform Skills Web design](platform-skills-web-design.md) adds the shared
Settings subsection and the Agent profile's effective-catalog view.

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

## Scope and implementation order

1. Add the agh-managed catalog, publication and disablement lifecycle, with
   explicit registration of existing operator-authored Skills. Retain source
   files and review evidence; do not silently adopt or overwrite native Skills.
2. Assemble the effective catalog and selection guide for the default Agent
   across supported entries. Preserve identities, session compatibility and
   actual load observations. Load method bodies before related execution.
3. Give each enabled plugin a versioned usage Skill bound to its real tool
   contract. Verify enabled, disabled, unavailable and changed configurations,
   then accept the complete cross-entry workflow with real native execution.

Existing reviewed procedures already render as scoped native Skills; installed
operator catalogs can also be inherited by native profiles. Unified agh
registration, default proactive loading and plugin guidance readiness are the
new work. This design does not claim a public plugin SDK, marketplace, new
execution engine or automatic execution of generated scripts.
