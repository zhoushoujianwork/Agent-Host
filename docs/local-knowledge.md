# Local project knowledge and retained notes

Status: local Markdown sources are implemented. Enabled datasource plugins are
global at catalog level; optional user/group grants and sender execution profiles
restrict actual tool access. Automated,
native and private-channel acceptance are tracked separately in the
[details](local-knowledge-detail.md). The
[knowledge-source guide](knowledge-sources.md) explains the shared tool boundary.

## Goal and usage

Let the assistant find existing project documentation and selected older notes
when answering an ordinary question. The user describes the problem; they do
not need to supply a directory or switch projects first.

An operator configures the local document source and enables its datasource
plugin. No project registration or task-scope selection is needed to expose
these documents to the assistant. Startup supplies descriptions; AI reads
relevant material on demand.

## Core approach

- **Read existing knowledge when needed.** A `local_markdown` source names a
  local root and an explicit list of relative files or directories. Claude searches
  and reads bounded excerpts through the existing read-only knowledge tools.
  Startup supplies a short source description rather than document bodies.
  Directory searches report bounded coverage rather than assuming every file
  was examined.
- **Share the configured catalog.** Enabled sources are globally configured;
  optional `users.json` requires explicit non-owner/shared grants. Restricted member
  profiles currently omit business MCP tools. Old source project/audience fields
  and Agent source lists no longer restrict unified datasource plugins.
- **Preserve old material as evidence.** Copy selected notes into a private,
  versioned snapshot outside the repository and retain a manifest linking them
  to their originals and content digests. Original files remain intact.
  Statements and reported outcomes retain their original attribution.
- **Keep documents separate from learned memory.** A retrieved note is a
  reference, not a new user statement or accepted execution lesson. Existing
  Host memory recall, review and retry continue independently.

Local knowledge roots do not become the execution cwd or additional native
directories merely by being configured. Knowledge tools are disabled in
tool-free runs. Native tools with broader directory access remain a separate
capability; plugin mounting is not a filesystem isolation boundary.

## Scope and rollout

1. Add the local provider and explicit file selection.
2. Configure and enable the local datasource plugin.
3. Retain selected older notes as snapshots with provenance.
4. Verify retrieval in a fresh native session, then repeat an ordinary private
   question without a directory hint. Keep real inputs and evidence outside
   the repository.

This increment does not collect new DWS history, import all old conversations,
publish notes into shared group memory or generate accepted memories from
source documents. See the [setup and validation details](local-knowledge-detail.md).
