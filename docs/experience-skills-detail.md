# Execution experience as native skills: implementation notes

Companion to the [main guide](experience-skills.md) and
[file-memory details](file-memory-design-detail.md). Claude mounting is implemented
for migrated file-memory tasks; legacy SQLite tasks keep their historical behavior.

## Eligibility and rendering

Eligible records are active, unexpired procedures with source_type
reviewed_experience, conditions, steps, source run and evidence IDs. That source
type retains its historical name; review metadata distinguishes automatic Host
selection from original human-reviewed lessons. Pending/rejected candidates,
facts, conflicted or disabled records do not become native skills.

The renderer preserves the original conditional lesson and evidence strength.
It adds no second model rewrite, scripts, hooks, allowed-tools or MCP configuration.
The [Agent Skills specification](https://agentskills.io/specification) remains
the format reference: name/description frontmatter and string-valued metadata.
Text capable of native dynamic command substitution is rejected.

The source file publication owns the procedure. The private plugin is derived
from the exact retrieval-selected source record and revision. A malformed or
out-of-scope record fails instead of selecting a weaker replacement.

## Native packaging

internal/assembly/skills.go publishes this task-local structure atomically:

```text
cache/workspaces/<run-id>/native-skills/
├── .claude-plugin/plugin.json
├── manifest.json
└── skills/<host-owned-name>/SKILL.md
```

The namespace and names include source identity, Agent, audience, scope and memory
identity to prevent collisions. Manifest records revisions, renderer, source runs,
evidence IDs and content hashes. Files are private, immutable and checked before use.
Limits are eight skills, 16 KiB per body and 64 KiB total. Extraction and no-tools
requests do not receive the mount.

The Claude adapter explicitly supplies --plugin-dir, using the documented
[CLI mechanism](https://code.claude.com/docs/en/cli-reference) and
[namespaced skills](https://code.claude.com/docs/en/skills). Installed support is
checked explicitly; unsupported invocation fails. Generated packages do not
write shared .claude/skills or native profiles and do not add filesystem
permissions through --add-dir.

Scope checks derive from trusted runtime identity and selected project grants;
a mentioned project or shared cwd grants no access. Mounted procedure bodies are
not also duplicated as full steps in the ordinary automatic memory prompt.

## Session and observations

The selected Skill digest and published memory fingerprint join session compatibility.
Updating or disabling knowledge selects a fresh compatible session, retaining
authorized continuity data while excluding withdrawn procedure text.
A pre-launch snapshot check rejects a concurrent publication change.
Already running tasks retain the version they received.

The task context and native package have separate manifests. workspace.assembled
records the scoped view; native observations separate provision/discovery and
Skill invocation from business tool results. Package creation is not proof of use,
and a Skill invocation is not proof that its described business actions succeeded.

## Validation and remaining work

On 2026-10-13 an isolated real Claude task discovered and successfully invoked the
single eligible historical Skill. It summarized the retained lesson and sources,
with one Skill load, zero business tool calls, completed execution, unchanged
memory and empty experience. File-memory history validation, corrupted package
checks, scope exclusion and no-tools cases are recorded in the
[file-memory validation](file-memory-design-detail.md#已完成验证与边界).

This is bounded discovery/invocation acceptance. A general benefit comparison,
new automatic-learning native loop, large-corpus performance, handwritten
config/skills mounting, generated code and Codex mounting remain separate work.

## Issue #1 acceptance mapping (2026-10-20)

The original roadmap combines Skill export and learned personal profiles. These
have separate delivery status; completing Skill publication does not complete
the profile proposal. The original SQLite source-of-truth assumption was
superseded by the explicit file-memory migration.

| Skill requirement | Existing evidence |
| --- | --- |
| Published experience is available in a fresh task, and withdrawn after invalidation | `internal/host/file_memory_test.go`: `TestFileHostAutomaticSkillPublicationFreshUseAndBlacklist` |
| Exact revision, evidence, conditions and procedure survive rendering | `internal/assembly/skills_test.go`: `TestNativeSkillSnapshotIsDeterministicScopedAndImmutable` |
| Other Agents/audiences/projects, expired knowledge and invalid sources cannot mount | `internal/assembly/skills_test.go`: `TestNativeSkillEligibilityAndPathBoundaries` |
| Official Chat continuation retains unchanged skills and drops changed/withdrawn context | `internal/cli/file_resume_test.go` |
| Real native discovery/invocation | The bounded historical 2026-10-13 experiment described above; not rerun by the CLI workbench change |

Automated fixtures establish the lifecycle and isolation rules, not general
model usefulness. The remaining profile implementation and acceptance plan is
tracked in the [profile main proposal](person-profiles.md) and its
[implementation notes](person-profiles-detail.md).
