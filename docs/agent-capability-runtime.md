# Agent capability runtime

Status: proposed packaging and directory policy, agreed direction A on
2026-11-03. This change validates existing capabilities and fixes default-owner
memory-tool assembly. It does not migrate production directories or credentials.

## Goal and usage

Give each managed Agent an explicit role, workspace and set of capabilities.
Enable memory or a knowledge source for that Agent; Host supplies the matching
usage guidance and tools when starting a compatible conversation. The Agent
searches and reads relevant material on demand.

Treat a capability like a mounted component: its instructions, tools, version
and authorized data access form one unit. Mounting it does not copy a whole
knowledge base into the prompt or authorize business writes.

## Core approach

- **Host owns memory.** Reuse scoped automatic recall and the existing read-only
  memory search/read/evidence tools. Published facts, methods and source evidence
  retain their identities, versions and audience/project boundaries.
- **Knowledge is a selected service.** Reuse source-specific MCP tools and usage
  Skills for local documents, Confluence and Dokki. Source changes invalidate
  incompatible bindings; reading a document does not publish learned memory.
- **Use an explicit AGH root.** Keep Agent configuration, authoritative data,
  operational records, rebuildable caches and workspaces under configured paths.
  Managed Agents should not discover capabilities through the shell user's
  instruction or Skill directories.
- **Reuse native authentication.** Keep the currently selected Claude/Codex
  login and native conversation storage. Reference them explicitly rather than
  moving credentials or changing the process-wide `HOME`.

This is a capability and lifecycle boundary. OS filesystem/network isolation is
a separate deployment choice.

## Scope and sequence

1. Verify memory and knowledge through Offload, including negative access cases.
2. Add explicit built-in memory/knowledge bindings to managed Agent assembly,
   reusing the current MCP and managed-Skill seams.
3. Prepare and verify a reversible directory/profile-binding migration, then
   adopt it for daily and channel entries separately.

Only step 1 and its narrow memory-tool fix are delivered here. Container images,
authentication migration and a generic plugin loader are outside this increment.

[Implementation notes](agent-capability-runtime-detail.md) ·
[Validation evidence](capability-validation-2026-11-03.md)
