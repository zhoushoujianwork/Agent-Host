# Clean operations Agent implementation contract

See the [user-facing plan](clean-ops-agent.md).

## Execution boundary

Offload retains admission, process ownership, deadlines, cancellation, native
account resolution, metrics and history. AGH retains routing, authorization,
context assembly, skill selection and task/memory acceptance. No Host model/tool
loop or recursive delegation is introduced.

The Codex app-server path is opt-in in native controls. It uses an owned stdio
process, native initialization, explicit Skills roots, thread start/resume,
turn start and turn interrupt. Raw notifications preserve their native method
and payload; AGH normalizes them without inventing native tool or model evidence.
Only matching completed/interrupted turns establish terminal execution status.

The selected model/account still comes from Offload configuration. Native
response/reroute evidence supplies the observed model; it is not inferred from
the request. Missing model or usage remains unavailable. Production CLI and lite
channel entries use the same strict native adapter with their selected Offload
Go library. The ordinary Offload adapter remains only as a low-level compatibility
contract for library consumers that do not configure a native profile.

## Environment ownership

An environment is bound to one Host conversation and one compatible execution
profile/account. Host passes its stable `HostSessionID` before the native thread
ID exists. After safe single-segment validation, persistent roots live at
`<NativeProfile>/environments/<HostSessionID>`, with a distinct directory per
conversation. Internal extraction uses disposable fresh environments. An
environment must not be shared by concurrent conversations.

Both native backends receive private HOME/XDG/native state and the existing
minimal environment policy. Only selected native authentication is seeded.
Host context is explicit native instruction input. The native cwd is a newly
created, empty workspace owned by the private environment. The business task
workspace and additional directories are explicit resource roots; extraction
receives none of those additional roots.
Source files cannot grant authority. Configuration isolation does not hide
the rest of the filesystem from an owner-granted native tool.

Resume requires the original environment and actual native thread ID. A missing
environment, changed profile/account, incompatible runtime or unknown previous
exit is an explicit error. No old Claude conversation becomes a Codex thread.
Calls without a Host session identity use a disposable environment, and cannot
resume. Native forks fail closed for both Claude and Codex because the current
Host request cannot bind the child's environment to the original parent's.

No-catalog CLI and lite channel profiles live under
`<HostHome>/native-clean/instances/<Agent>/audiences/<AudienceKey>`. Their fixed
settings and `skills` directory are empty and owner-private; any Skill residue,
symlink, nonprivate directory, or compatibility executor fails before dispatch.
Full Host CLI chat without an Agent catalog selects the same controlled empty
profile while preserving its explicitly configured projects, context, MCP,
knowledge, memory, permission and task-scoped Skill package assembly.
The canonical profile path plus `environment=v2;exact-skills=v1` participates in
runtime compatibility, so sessions created by the former ambient path cannot be
adopted. Serialized room continuations reuse the original room environment while
retaining their existing Host receipts, deduplication and native binding rules.

## Skills and internal calls

Reuse Host authored/experience skill assembly and digest validation. Approved
global operations skills are inventoried separately from credentials and caches.
Preserve source directories, references and required resources, deduplicate by
logical identity, and record the final mounted digest. Before a Codex thread is
created or resumed, Offload enables exactly each selected root's own `SKILL.md`,
disables unselected declarations, reloads discovery and compares canonical paths
against the complete manifest. Nested declarations require separate selection;
bundled, cloud, system and plugin Skills are not implicitly enabled. Any native
write failure or final inventory mismatch stops before a model turn.

An Agent may select `native_skills` from the configured native profile or pair
the list with an explicit `native_skills_root`. The root is validated even when
the list is empty and supplies Skills only; its settings, credentials and native
state are never imported. Omitted, null, and empty lists select zero Skills.
Every routed Agent receives empty controlled settings and an exact selection
mount, so neither the default owner nor another audience inherits ambient or
sibling Skills. Each selected Skill root is resolved and validated, including
explicit symlink targets. Selected names and content always participate in the
runtime fingerprint, including legacy explicit selections; the empty selection
has a stable strict-policy fingerprint. A task package is admitted only after
its original digest validates. Codex receives the resulting managed roots;
Claude receives a controlled temporary plugin bundle containing only those
sources. Its controlled plugin path accepts leaf directories and rejects nested
`SKILL.md` declarations until equivalent native enablement is verified; it does
not silently expand a bundle or drop declaration files.

The explicit internal extraction selector is resolved independently of the
business default and affects only experience/object extraction. It uses the
same Offload package boundary with native tool-free and structured-output
controls, a fresh session and no operations Skills/MCP. Missing or unsupported
internal configuration reports a real error; it never triggers automatic
selection of another provider. Business and internal model evidence remain
attributed to their own run/phase.
See [memory profile and Skill source configuration](clean-ops-memory-profile.md)
for the operator-facing fields.

The old Claude-specific execution profiles remain available rather than being
rewritten. A new operations profile uses the existing Host owner-equivalent
channel policy. Claude approval-rule strings are not presented as Codex tool
ACLs; requests that require unsupported strict controls remain rejected.
This candidate does not change `shared-execution-v2` authorization or claim a
completed production deployment.

## Acceptance evidence

Quiet fixtures cover clean environment creation/reuse, foreign bindings, native
Skills inventory, MCP requests, model evidence, cancellation, malformed RPC,
profile changes and rejected unsupported combinations. Host checks cover shared
CLI/channel composition, skill digests, per-session ownership, extraction routing,
and preservation of the existing authorization policy.

Live acceptance uses approved test destinations only. Check both directions of
each channel, exact marker replies, actual observed model and profile, tool/Skill
discovery, continuation, Host execution/acceptance outcomes and independent
memory outcomes. Preserve IDs and partial effects; no business command is replayed
to repair extraction or delivery. Unavailable channel access remains pending.
