# Host-managed Skills: implementation contract

Companion to the [main design](managed-skills-design.md). All additions below are
proposed unless identified as existing behavior; they are not a deployment receipt.

## Ownership and source of truth

agh owns registration, review/publication, enablement, source provenance and
effective selection. Native harnesses own Skill discovery/loading and their
model/tool loops. Adapters translate the approved snapshot into native format;
Host does not introduce another model/tool loop.

Use one logical catalog with three source kinds: operator-authored procedure,
reviewed experience, and plugin usage. Preserve each source's canonical files
and approval path rather than copying all sources into the memory store.
The Host-owned catalog pins approved content and referenced files as immutable
published revisions in its managed Skill store. Native home-directory links are
discovery/import inputs, not an alternative publication authority; editing a
source requires revalidation and a new managed revision before new use.
Experience publication continues to use its existing evidence assessment;
operator registration requires explicit adoption and review of the authored
content; plugin guidance is validated against its installed adapter contract.
None of these paths can fabricate another path's review result.

Keep facts/preferences/decisions and source notes as knowledge. Keep operation
results and original evidence as execution records. A reusable workflow's final
published procedure is a Skill, with linked evidence, not an additional divergent
copy of its steps in ordinary memory prompts. Rejected experience remains rejected;
creating a local Skill does not mark it admitted or silently register it as active.

## Catalog and publication contract

A managed entry records stable Skill ID, source kind, canonical reference,
version/digest, owner, applicability, permitted Agent/project/audience context,
dependencies, required plugins/tools, provenance and review/publication state.
Installation, registration, enablement, readiness, loading, invocation and business
acceptance are separate observations. An enabled entry can still be unready.

Lifecycle: draft/import → validate/review → published → enabled and ready →
task snapshot. Disabled, withdrawn, expired, rejected or dependency-incompatible
entries do not enter a new effective catalog. A correction creates a new revision;
rollback selects a prior eligible revision and preserves intervening audit history.
Resolve duplicate names by stable identity and explicit operator choice, not
directory order, fuzzy customer names or a model inventing aliases.

Instruction bodies and referenced files are versioned together. Environment
variants belong to references selected by declared applicability; region, profile,
data root, bucket and resource targets are never borrowed from a different variant.
Retain existing reviewed helpers only when explicitly registered and validated;
automatic generation/execution of new scripts or hooks is outside this increment.

## Default Agent assembly and active loading

At startup and before each turn, resolve the catalog using trusted runtime identity,
current enablement, dependency readiness and existing policy. The default Agent
gets all effective entry descriptors plus a small Host-owned selection guide.
Descriptors identify purpose, applicability, revision and the real native load entry.
No manual per-Skill binding is required for this default behavior. Explicit tool-free
requests get no operational Skill/tool mounts; other Agents retain their declared
configuration until their adoption policy is explicitly changed.

The native Agent must select and load the full applicable Skill, then the relevant
references, before procedural work or the first related plugin tool call. A plugin's
usage descriptor is always visible when it is ready and enabled; a relevant task
causes its guidance to load proactively, without a user naming the plugin Skill.
Catalog publication does not mean every full body is injected into every prompt.
Context limits require explicit paging, deferred loading or a stated limitation,
not silent truncation of procedures or silent loss of effective catalog entries.

Bind catalog, Skill revisions and plugin fingerprints to session compatibility.
Changes are re-evaluated on continuation; a disabled plugin cannot remain callable
merely because an old native session remembers it. Use a fresh compatible session
when required. An in-flight run retains its recorded snapshot; disabling a Skill
does not undo actions or imply cancellation. Existing cancellation rules still apply.

## Enabled-plugin usage contract

An installed plugin provides trusted usage instructions alongside its adapter:
purpose, target/identity resolution, supported actions and tool names, input/output
semantics, verification, partial/unknown outcomes and authorization boundaries.
Guidance references the actual mounted tools and operation settings; credentials
remain in adapters and never enter catalog descriptions or Skill bodies.

Compute tools and guidance from the same plugin/configuration snapshot. Omitted or
disabled plugins mount neither. Enabled but unavailable plugins surface the exact
readiness error; do not advertise a ready Skill, substitute another provider or
pretend the operation succeeded. Preserve existing startup/task failure policies.
An enabled dependent procedure is unready if its required plugin is disabled.
Upstream permissions, send switches and current Beta datasource sharing remain
effective; this proposal adds no new per-user restrictions or execution authority.
External pages, messages and arbitrary retrieved documents are data, not trusted
plugin instructions and cannot enable plugins or publish an active Skill.

## Migration and acceptance

Inventory current experience-derived, native-installed and plugin-provided Skills.
Offer explicit registration with source hashes and conflicts; keep existing mounts
available until the managed replacement is accepted. Do not rewrite operator files,
change memory admission, enable plugins or restart production as a side effect.

Verify authored and experience publication independently, rejected-entry exclusion,
full reference reads, China/US routing, exact original-ticket reuse, dependency
failures, enable/disable on continuation, tool-free mode, no secret exposure and
no action replay. Test the default Agent across supported CLI/Web/channel paths.
Record catalog exposure, actual Skill/reference loads, tool calls, business
acceptance and publication separately. Real native acceptance must show proactive
loading without a named Skill; file presence and offline checks alone are insufficient.
