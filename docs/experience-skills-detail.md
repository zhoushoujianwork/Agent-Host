# Execution experience as native skills: implementation notes

Companion to the [main proposal](experience-skills.md). The
[new file memory details](file-memory-design-detail.md) own storage, publication,
migration and implementation order. Native rendering and mounting
notes below remain proposed; concrete commands need installed-version validation.

## Current baseline

`internal/state/types.go` stores memory conditions, steps, evidence IDs, source
run, revision and Agent/audience/project scope. `internal/state/experience.go`
implements the historical review path. `internal/assembly` supplies eligible
memory context, and `internal/harness/claude` owns native invocation.
`harness.Request` has no generated-skill field. The Codex package is a placeholder.

The [file memory transition](file-memory-design-detail.md) must establish file
publication, retrieval and blacklist exclusion before this becomes the normal
post-run path. Skill generation must not treat a pending candidate as published.

## Rendering and native mounting

The [Agent Skills specification](https://agentskills.io/specification) requires
`name` and `description` frontmatter and supports string-valued metadata. Use
stable Host-owned names and metadata for memory ID/revision, source type and
renderer version. The body carries conditions, observed steps, evidence strength
and references to the retained original evidence. Do not copy raw tool logs or
credentials into the artifact. Local references do not promise portability of
the underlying evidence outside this installation.

Use a deterministic renderer and validate generated YAML/Markdown. Source text
must not create extra frontmatter, dynamic command substitutions, tool grants,
hooks, scripts or MCP configuration. No `allowed-tools` declaration is generated.
The renderer preserves procedure meaning without upgrading its evidence strength.

Claude documents session-local plugin loading through
[`--plugin-dir`](https://code.claude.com/docs/en/cli-reference) and
[namespaced plugin skills](https://code.claude.com/docs/en/skills).
The preferred first integration is a Host-owned, skill-only local plugin containing
the exact eligible set, passed for that invocation. This is native packaging,
not a public Host plugin API. Verify discovery with the installed native version
before committing to this mount; documentation is not integration acceptance.
Do not install generated skills in shared `.claude/skills` or native profiles,
and do not use `--add-dir` merely to mount them, since that also adds filesystem
access. An unsupported native mount fails explicitly without a weaker substitute.

## Scope and lifecycle

Select through existing authorized project/Agent/audience and applicability
rules, including all explicit grants for a joint task. Do not infer access from
cwd or a procedure mentioning another project. Reuse the current recall budget
as the initial bound, accounting for descriptions and bodies; avoid simultaneously
injecting the full procedure and its skill without an explicit reason.

Materialization is a derived outcome separate from execution and memory save.
Publish files atomically under private Host state and retain a manifest of exact
memory revisions, scope, renderer and content hashes. Recheck eligibility before
launch; a stale native manifest cannot override the authoritative file publication
snapshot. Coordinate disable/update with active readers so future discovery
cannot use withdrawn artifacts. Interrupted
generation can be rebuilt idempotently; retries do not dispatch another task.

The skill-set digest joins runtime compatibility. When it changes, replace the
native session and preserve only authorized continuity context excluding withdrawn
procedure text. Already running tasks are reported as having received the older
snapshot; disabling cannot undo their earlier actions or erase native history.
Ordinary memory revision checks still apply. Extraction, `--no-tools` and
unsupported engines receive no generated skill mount.

## Implementation slices and validation

| Slice | Existing boundary | Required proof |
| --- | --- | --- |
| Eligibility and rendering | File publication/experience plus a small internal renderer | Active procedure exports; pending, fact, expired, conflicted and disabled records do not |
| Private artifact lifecycle | Host/state coordination | Atomic rebuild, stale revisions, failure receipt and independent retry |
| Native mount and compatibility | Assembly/harness/Claude | Exact set, unchanged handwritten skills, no added permission/configuration, fresh session after withdrawal |
| End-to-end use | Existing eval and experiment conventions | Native skill discovery and invocation; same-task baseline without skill; citation and scope checks |

Retain failures and exact tested binary/version, manifests and source receipts.
Compare native invocation and task outcome separately: a discovered skill is not
proof of use or benefit. Run `make check`, focused concurrency tests for lifecycle
changes and a bounded native save/use/disable loop. General performance and model
quality remain unmeasured until separately evaluated.
