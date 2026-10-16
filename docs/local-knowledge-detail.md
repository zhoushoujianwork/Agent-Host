# Local knowledge implementation details

Current datasource policy: enabled `datasource.plugins.*.json` plugins are global
across channels, Agents, projects and owner/group conversations. Old plugin
`access`, source project/audience fields and Agent datasource selectors are
ignored. Standalone legacy configurations and historical scope-validation
records below retain their original meaning. See the
[datasource design](datasource-plugins-design.md) for current behavior.

The [main guide](local-knowledge.md) defines user behavior and scope. Local
Markdown uses the same source selection, MCP execution and evidence boundary
as [remote knowledge](knowledge-sources-detail.md).

## Configuration and selection

Add a source to the private version-1 knowledge configuration. Identifiers and
paths below are synthetic:

```json
{
  "version": 1,
  "sources": [
    {
      "name": "project-notes",
      "provider": "local_markdown",
      "root": "/absolute/private/knowledge/project-notes-v1",
      "paths": ["knowledge/overview.md", "procedures/service-check.md"],
      "principal": "local-owner",
      "description": "Project terminology and retained operating procedures",
      "projects": ["default", "service-a"],
      "audiences": ["owner"]
    }
  ]
}
```

`root` selects the source directory; `paths` explicitly selects Markdown files
or directories relative to it. Directory entries permit bounded Markdown
scanning within those entries; there is no implicit whole-root import. Coverage
reports truncation when scan limits are reached, so zero hits do not establish
global absence. A search scans at most 1,024 Markdown files, 4,096 directory
entries and 32 MiB, and returns at most 20 matches. Individual UTF-8 files are
limited to 2 MiB. Hidden segments, traversal and symlinks are rejected or excluded;
rooted opens and file identity checks guard path changes during access.
The provider does not
use an endpoint or provider token. Local checks reject invalid declarations,
unavailable selected files and paths that escape the configured root. Source
content is treated as task evidence, never permission or trusted instructions.

`projects` retains the existing explicit grant model. `default` authorizes
unregistered/default-workspace execution; a registered project needs its own
ID. Joint tasks use the deduplicated union of grants for their participating
projects; a neutral cwd does not implicitly add `default` access.

Optional `audiences` further restricts every provider, including existing remote
sources. Omission preserves existing audience behavior. Use `["owner"]` for
private snapshots. A source must satisfy project and audience selection before
resolution; an Agent's `knowledge_sources` subset can narrow eligible sources
but cannot expand either grant. Group audiences never fall back to `owner`.

Set the service's `knowledge_config` to this private file and include the source
in the default Agent's `knowledge_sources` selection, or use its documented
inheritance behavior. For unattended execution, authorize the exact named
native MCP tools according to the configured execution policy. Run
`knowledge check` and `service check` before restarting with `make install`.
Source declarations do not register projects automatically. Registry changes
use the existing stopped-service procedure in the
[installation guide](service-install.md#register-production-projects).

## Runtime tools and evidence

The local source exposes `search_local_markdown` and `read_local_markdown`
through its named `mcp__knowledge_<source-name>__` mount. Assembly passes only
short navigation hints and tool names. Tool listing and initialization do not
search documents or put their bodies into task context. The native Agent
chooses which source to search and which matching file to read.

Search returns bounded matches and excerpts with source-relative references.
Reads return a bounded Unicode-character window, content digest and continuation
metadata through the common knowledge receipt. Continuing a read supplies the
previous `expected_content_sha256`; an edited document fails that continuation
instead of silently combining versions. Absolute local source paths are not
business citations. The configured source identity plus relative path identifies
the document in internal retrieval and evidence. Ordinary answers do not expose
local memory filenames, paths, internal source identifiers, or citation entries.
If a local copy records an original external article URL relevant to the answer,
append a clickable title link after the supported passage or at the end.
Otherwise omit the internal citation silently, without a link-unavailable notice
or an external lookup solely for citation purposes. External searches remain
available when needed to answer the question. Never fabricate a link.

Effective source configuration and audience grants participate in native session
compatibility. Changing or removing a grant rotates the binding; per-call checks
reject stale configuration. Document content digests identify retrieved versions
rather than replacing the source access fingerprint. Configuration revocation
does not erase previously read native history or cancel a read already in flight.

The source root is independent of the business cwd, native additional directories
and stable memory scope. Registering a source does not add native filesystem
permissions. `--no-tools` and independent experience extraction mount no source
tools. Broader native tools may still access files allowed by native/OS policy;
this connector's audience filtering is not a confidentiality sandbox.

Saved source receipts and paired tool events remain operational evidence.
Knowledge-reference tools are excluded from execution-experience extraction and
candidate support. A search result does not become learned Host memory, and
the source author's text is not attributed to the current message sender.

## Retained older notes

Selected legacy notes use an immutable private snapshot outside the repository.
Keep a source manifest beside the snapshot, outside the configured Markdown
allowlist, recording original source location, selected relative path, copy time,
original revision when available, and SHA-256 digest. Retain original files and
existing versions; a later correction creates a new snapshot rather than
rewriting previous evidence. Verify copied bytes against their originals before
enabling the source. The manifest is provenance evidence, not an execution run.

This is a bounded reference migration. It does not create user-declared facts,
reviewed experience, fake completed runs or a full conversation import. Private
conversation-derived notes are mounted for `owner` only. Their wording retains
the difference between a participant's report and an observed successful test.
Real source manifests, notes, test inputs and platform evidence stay outside the
repository.

## Validation status and limits

`make check` passed on 2026-10-11. Automated coverage includes
explicit path selection, traversal/symlink rejection, project/Agent/audience
intersection, stale configuration, edited-document continuation, bounded
responses, source attribution, tool-free exclusion and memory-publication
exclusion. The existing remote-source tests remain green. Independent review
and focused race tests found no blockers.

Three real Claude checks passed using isolated synthetic documents and fresh
native conversations: owner searched and read both shared and private sources;
two different group-like audiences searched and read only the shared source,
with no private source in their native tool catalog. Host and native mount
records agreed. These source-only checks created no memories or candidates.
They verify native assembly and retrieval, not DingTalk group delivery or OS
filesystem isolation.

The managed installation reconnected, and local knowledge/service configuration
checks passed. Registration and source setup preserved all existing durable
records. A real owner-private question after `/new` completed in a fresh native
conversation: it searched and read the retained source, kept its date and
participant attribution, and distinguished historical reports from current state.
The channel callback and accepted reply are recorded outside the repository;
an independent client-delivery event was not observed by the bounded listener.

A separate live service-status check without a directory hint successfully
searched and read project knowledge from the default workspace, then attempted
explicit read-only target-environment diagnostics. The full investigation reached
the existing ten-minute turn deadline and was canceled with an explicit channel
failure. Knowledge discovery is verified; complete live investigation acceptance
remains outstanding. No failed task was silently replayed or published as memory.

Real inputs, source manifests and platform evidence remain outside the repository.
Connection readiness alone does not establish source use, delivery or answer quality.
