# Agent profile implementation notes

Status: proposed; [main design and scope](agent-profile-design.md) are authoritative.

## Existing foundations verified in source

| Source | Existing responsibility | Profile implication |
| --- | --- | --- |
| `tools/run-viewer/settings-fields.js` | Exposes default Agent, display name and workspace. | Existing edits can remain; the full profile is a new view. |
| `tools/run-viewer/settings_store.py` | Finds separate Agent files, validates saves and retains previous versions. | Preserve checked saves and distinguish CLI/channel configuration destinations. |
| `internal/cli/agents.go` | Agent definitions, inherited options, group bindings and channel Agent resolution. | Reuse runtime selection semantics rather than implementing a second resolver in the browser. |
| `internal/assembly/runtime.go` | Assembles runtime context and compatibility fingerprints. | Configuration alone cannot prove what a current or past execution loaded. |
| `docs/datasource-plugins-design.md` | Enabled datasource plugins are shared in Beta; older Agent selectors are obsolete. | Do not present `plugins` or `knowledge_sources` as working datasource isolation switches. |
| `docs/workspace-layout-detail.md` | Task-scoped derived native Skill views and separate context/profile ownership. | A global filesystem Skill scan cannot establish the effective Skill inventory of every Agent. |

The checkout already contains ACP work in progress. Use the execution engine
and model resolved for the selected destination; do not assume every Agent uses
the same backend or infer production activation from checkout support.

## Identity and evidence

Key a profile by configuration destination and Agent ID. The same `default` ID
in CLI, DingTalk and Feishu files does not establish one shared runtime identity.
Show destination and source on inherited values. Never join unrelated runtime
records merely because their Agent IDs match.

Keep saved configuration, service-loaded configuration and run-observed assembly
as separate evidence. Display timestamps/source references when available. An
old run proves its own configuration only. If no runtime version can be compared
with a saved version, show loaded state as unknown rather than up to date.

Build the first inventory from existing local configuration and attributed run
records. Do not start a task, query an external service, install tools or scan
arbitrary directories simply to populate the page. Missing discovery support is
an explicit inventory gap, not an empty set of capabilities.

Capability entries need a stable identity, type, description, origin, scope and
evidence state. A source can have a configuration state and a separate observed
health result. Native tools, scoped Skills and Host orchestration features must
retain their distinct ownership. Labels are descriptive, not new authorization.

## Group routing view

Resolve each channel/account through the actual service configuration and Agent
catalog. Join configured group bindings with locally observed conversation
metadata using channel, account/tenant and conversation identity. Use existing
channel provenance; never treat a conversation ID as globally unique.

Rows show explicit bindings even without messages. Add observed default-routed
groups only for the relevant destination, retaining their observation time. A
historical route is not necessarily the current route after a binding change;
show current selection separately from last activity. Explain the catch-all
default rule without enumerating unknown groups. Missing names fall back to a
safe identifier; do not invent names or imply membership discovery.

This is an operator-local view. Do not expose credentials, owner-private context
or raw native session content in summary cards. Group configuration does not
change per-audience history boundaries. Instruction previews treat attachment,
memory and source text as content, not authority to change settings.

## Delivery sequence and checks

First implement the destination-aware read-only projection and profile UI on the
existing workbench. Add editing incrementally through the existing save path;
retain conflict detection, validation and previous versions. A later change may
need a shared runtime inspection function if current receipts cannot expose
effective configuration safely. Define its concrete contract during development.

Focused acceptance cases: separate same-ID Agents across destinations; inherited
and overridden values; explicit groups without history; observed default groups;
changed historical routes; unavailable names; missing or stale loading evidence;
shared datasource cards; task-dependent Skill inventory; and Web Chat pointing
to a different CLI destination. Preview fixtures must be clearly illustrative.

Run appropriate UI/backend tests and `make check` for implementation. Verify live
service state separately only when a deployment is requested. This design adds
no runtime changes and provides no live acceptance evidence.
