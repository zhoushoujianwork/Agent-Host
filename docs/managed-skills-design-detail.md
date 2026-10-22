# Host-managed Skills: implementation contract

Companion to the [main design](managed-skills-design.md). The contract below is implemented in source. Local validation and remaining
acceptance limits are recorded at the end; this is not a production deployment receipt.

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


## Implemented storage and operator commands

`internal/managedskills` owns a private `catalog.json`, serialized mutations and
atomic replacement. Each imported revision contains its full instruction tree,
metadata and content digest. Review and publication change audit state, never
pinned content. Re-import creates a new draft while the old selected publication
stays active. Review requires an actor and evidence note; every later lifecycle
command requires the exact inspected revision and digest. Conflicting enabled
names require explicit disablement before selecting the replacement.

Only authored content enters through import. Experience remains authoritative in
its existing reviewed memory source and is projected into the same effective
catalog; rejection, blacklist, invalidation and expiry are preserved. Adapter-owned
usage contracts are derived from the actual mounted definitions, never retrieved
pages or credential settings. `skills list` inspects authored history; the run's
catalog event includes authored, experience and plugin entries together.

Example adoption (replace paths and provenance deliberately):

```sh
agent-host skills import --root /runtime/managed-skills \
  --source /canonical/authored/sftp-delivery --id sftp-delivery \
  --actor operator --applicability 'Customer SFTP delivery; select actual region' \
  --agents default --audiences owner --scopes 'project:delivery' \
  --plugins dingtalk --tools mcp__dingtalk__read_ticket \
  --provenance 'Reviewed authored regional runbook'
agent-host skills list --root /runtime/managed-skills
agent-host skills review --root /runtime/managed-skills --id sftp-delivery \
  --revision 1 --digest INSPECTED_SHA256 --actor reviewer --note 'Reviewed both regional references'
agent-host skills publish --root /runtime/managed-skills --id sftp-delivery \
  --revision 1 --digest INSPECTED_SHA256 --actor operator
agent-host skills enable --root /runtime/managed-skills --id sftp-delivery \
  --revision 1 --digest INSPECTED_SHA256 --actor operator
```

`disable`, `withdraw` and `rollback` use the same identity/revision/digest/actor
arguments. Import accepts `--expires-at RFC3339` and optional `--contracts` JSON
mapping mounted plugin names to expected tool-contract hashes. A changed required
contract is unready even if its tool names remain present. The exact public
contract is pinned in the usage Skill's `references/contract.json`; its digest is
`managedskills.ContractDigest(contract)` (canonical JSON), also exposed as
`tool_contract_digest` in the catalog descriptor. Plugin settings enter only as opaque fingerprints.

`SKILL.md` requires `name` and `description`. Only these and `metadata` are adopted
from native frontmatter. References are copied completely; symlinks, executable
dynamic context and native hooks/permission overrides fail. Existing `scripts/`
require explicit `--helpers`; their content and owner execution bit are pinned.
This does not execute or generate helpers. Source
edits require another import and review. No native directory is silently adopted.

`--skills-root` selects one shared catalog across CLI/Web/channel runtimes. Unified
services default to `ROOT/managed-skills` and accept `execution.skills_root` in the
selected execution profile. File-memory runtimes default beside their shared
`memory_root`; standalone legacy homes default to `HOME/managed-skills`. An absent
catalog is empty, not an instruction to import global Skills. This does not create
a new per-channel publication authority when a runtime already shares memory.

Default-Agent assembly collects all eligible procedures, independent of retrieval
query, and adds authored and enabled-plugin entries. Other Agents keep their
existing selection. The native v2 manifest pins full reference/helper hashes;
limits are 512 entries, 256 KiB per file, 8 MiB total and 128 KiB of complete
descriptors, with explicit failure
rather than truncation. The legacy v1 renderer keeps its existing limits.
The always-loaded Host instruction guide carries every effective descriptor;
bodies load through native Skill and Read calls. No operational Skills mount in
tool-free or restricted-member runs.

Session compatibility includes the catalog/revision and plugin contract/configuration
snapshot. A final pre-dispatch check rejects changed catalogs or plugins; the next
turn assembles a compatible session. Already-dispatched runs retain their immutable
snapshot. Native loading remains native policy, not a new authorization gateway.
This increment does not make model compliance a mathematical guarantee.

The standard compact backup also retains `ROOT/managed-skills/catalog.json`,
including immutable authored revisions and audit history. Restore checks its
content integrity without re-adopting source directories or changing provenance.
Custom `--skills-root` locations require their own backup policy.

## Local validation and evidence

Automated tests cover adoption/review/publication, pinned regional references,
new revisions/rollback/withdrawal, corruption, unsafe imports, scope/audience/Agent
restrictions, expiry, dependencies and contract incompatibility, all-entry assembly
beyond the legacy eight-Skill limit, tool-free mode, plugin guidance readiness,
configuration privacy and disablement on continuation. Existing CLI/Web/channel
suites exercise the shared `refreshFileRuntime` / `composeHost` / `Host.run` path.

The opt-in `TestManagedNativeProactiveLoading` uses the installed Claude executable,
a disposable profile/workspace and a local read-only fixture MCP. Provider secrets
are read through the existing protected profile mechanism; their values are not
copied into catalog bodies, arguments or test logs. On 2026-10-22, US and China
cases both passed: the user prompt named no Skill; native execution loaded both
procedure and plugin guidance, read the selected regional reference and exact tool
contract, then called `read_ticket` for `US-FTP-001` or `CN-FTP-001` respectively.
The answer included its regional marker, the latest comment and current pending
task. The last recorded native pass took 29.51 seconds (US 15.05, China 14.46),
with successful Skill/reference results observed before the ticket tool call. These are synthetic tickets and environment settings, not live SFTP receipt
claims. No real approval, infrastructure write or outgoing message was performed.

Run the native acceptance separately from offline checks:

```sh
AGENT_HOST_MANAGED_SKILL_SMOKE_PROFILE=/approved/native/profile \
  go test ./internal/harness/claude -run '^TestManagedNativeProactiveLoading$' -v -count=1
```

`skills.catalog.provided` records exposure and dependency failures.
`skills.load.observed` and `skills.reference.observed` record native requested,
completed or failed calls, with source/file digests and read arguments. Existing
`tool.activity` retains actual results and ordering. Reference coverage requires
checking read ranges/results; a mount or successful partial read is not full
coverage. Lifecycle audit, native execution, task acceptance and memory publication
remain separate records. Real platform/channel rollout remains separate from this
local native fixture acceptance; production was not restarted or silently migrated.


Read-only code localization used offload session
`704b80b7-0897-47e0-b341-6fad688d567f`, profile `cc`, model
`claude-sonnet-5[1m]`. The first invocation exited 0 but reported BLOCKED by tool
availability (5 turns, measured 22.596 s, estimated USD 0.069264). Resuming that
same session with exact file paths completed localization with no edits (exit 0,
8 turns, measured 30.816 s, estimated USD 0.176601). Key findings were checked
against source by the implementing agent. These are reported worker estimates,
not verified billing, savings or total parent/worker cost.
