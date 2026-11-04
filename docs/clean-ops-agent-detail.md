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
the request. Missing model or usage remains unavailable. The ordinary Offload
exec path is preserved for callers that do not opt into app-server controls.

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

## Skills and internal calls

Reuse Host authored/experience skill assembly and digest validation. Approved
global operations skills are inventoried separately from credentials and caches.
Preserve source directories, references and required resources, deduplicate by
logical identity, and record the final mounted digest. Native skill discovery
must be checked against approved roots before business execution.

An Agent may select an explicit `native_skills_root` plus a nonempty
`native_skills` list. This root supplies Skills only; its settings, credentials
and native state are never imported. Each selected Skill root is resolved and
validated, including explicit symlink targets. A task package is admitted only
after its original digest validates. Codex receives the resulting managed roots;
Claude receives a controlled temporary plugin bundle containing only those
sources. Existing Agents retain their prior source selection. The new operations
Agent does not inherit the full mixed global Claude Skills directory.

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
