# Platform Skills Web: interaction contract

Companion to the [main page design](platform-skills-web-design.md) and
[managed Skills contract](managed-skills-design-detail.md). All UI additions are
proposed. Current Settings → Agents already provides native-skill selection and
bounded capability history; it is not yet the managed platform catalog.

## Navigation and view ownership

Add a Settings category, `平台技能`, beside `智能体`. Retain the existing modal
close/save behavior, appearance and shared configuration selection. The selected
Host/runtime must remain visible; a standalone preview has no live registry and
cannot claim to save platform changes. Web/CLI and channel destinations remain
distinct, even if their default Agents have similar names.

Platform Skills owns registry and publication operations. Agent → Capabilities
adds a platform-Skill subview with effective availability, selection-policy origin
and links to the same stable Skill detail. Keep installed native-user Skills and
historical memory mounts distinguishable during migration. Do not reinterpret
the existing `native_skills` custom-selection field as a platform-policy override.

Default Agent copy: “自动发现已启用且就绪的平台技能，处理相关任务前主动装载。”
Show inherited catalog availability without a checkbox for every platform Skill.
For another Agent, display the actual policy or unknown state; this UI proposal
does not silently enroll every Agent or change existing datasource-sharing rules.

## List and detail information

Default to managed entries. Awaiting-registration candidates stay in a separate
tab. Search by name/purpose; filter by source and state. Use stable IDs rather
than names for routing and selection. Counts come from the complete filtered
query, with paging and coverage; partial results cannot claim catalog completeness.

Each row shows title/purpose, source kind, effective revision, platform enablement,
readiness reason and default-Agent availability. Keep the common row compact:
for example “已启用 · 缺少插件” is more useful than one misleading “已启用” badge.
Show loading evidence and timestamps in detail rather than mixing them with
configuration status. Missing evidence is unknown, not a fabricated zero.

| Detail section | Information and actions |
| --- | --- |
| Description / 使用说明 | Purpose, applicability, full `SKILL.md`, reference tree and dependency requirements; region variants remain distinct. |
| Version and source / 版本与来源 | Stable ID, source, owner, review outcome, current/pending revision, validated changes and explicit publication actions. |
| Loading history / 装载记录 | Agent and actual execution destination, timestamp, catalog/version snapshot, observed Skill/reference reads, tool activity and links to authorized run evidence. |

Reference reading must support complete content with explicit paging and revision
binding. A shortened preview is labeled and offers full reading; never imply a
truncated procedure is complete. Apply existing content/path authorization and
secret redaction. Do not display credential files, tokens or private-message
bodies as operational “examples.” Scope runtime evidence to the viewer's access.

## Registration and version changes

Discover candidates only from explicitly configured installed catalogs or local
sources. Scan is read-only. Import previews files, source identity, conflicts,
dependencies and validation findings before explicit registration/publication.
Source changes create a pending revision; they do not replace active content.
An operator can compare and publish an eligible revision or retain the active one.

Experience-derived entries display their actual memory review. A rejected
experience links to its reason and evidence; there is no “force enable” button
that changes it into an admitted procedure. Operator-authored registration uses
its own explicit review path and never claims that experience review passed.

Checked writes supply the expected catalog/revision and reject concurrent edits.
On conflicts, refresh and preserve the user's draft; on an unknown write result,
read the registry before offering another attempt. Platform actions preserve
immutable publication and audit history; no deletion of original execution evidence.

## Plugin and Agent behavior

Provider-owned usage instructions and tools share a plugin/configuration snapshot.
Ready enabled plugins appear in the effective catalog. Disabled plugins remain
visible as unavailable administration records, but expose no active instructions
or tools to execution. An enabled plugin can be unready due to authentication,
missing guidance or incompatible tools; show the actual reason and configuration
link. Never silently enable a plugin to satisfy a procedure dependency.

Disabling a procedure previews its affected Agent catalog entries. Disabling a
plugin previews its usage Skill and dependent procedures. Route plugin changes
to the supported shared configuration path; if configuration is file-only, show
that limitation rather than inventing a save control. Plugin enablement does not
grant approval, infrastructure mutation or outgoing-message authorization.

After a save, distinguish desired configuration, effective runtime revision and
last observed loaded revision. Explain whether the next turn will rotate native
context or an explicit service restart is required. Existing active runs retain
their recorded snapshots; saving never stops, replays or launches business work.

## Acceptance and rollout

First ship the managed list/detail and full references, then checked registration
and procedure lifecycle actions, then plugin readiness and Agent effective-view
integration using the same registry. Surface unsupported backend actions honestly;
do not build a client-only parallel registry or simulate successful mutations.

Verify navigation and Chat preservation, no-Host/empty/partial/error states,
read-only evidence access, source-version conflicts, enable/disable on continuation,
plugin dependency propagation, and visibility in the actual selected Agent/runtime.
Use China and US SFTP fixtures to prove correct reference selection and full reads,
and a rejected memory fixture to prove installation is not publication. Native
acceptance must separately demonstrate proactive Skill loading, not merely cards
appearing in the Web page. No operational approvals or credential sends are needed
to test this UI.
