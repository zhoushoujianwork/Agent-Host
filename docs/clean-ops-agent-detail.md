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
Codex copies each validated task-scoped Skill package into the conversation
environment under its content digest. Equal published content therefore keeps
one canonical native Skill path across turns, while changed content retains a
different path and cannot silently resume the prior environment binding.
Load evidence retains both the original published package root and the verified
controlled mount root; reference attribution requires an exact path under one
of those roots and never relies on a matching suffix.
The canonical profile path plus `environment=v2;exact-skills=v1` participates in
runtime compatibility, so sessions created by the former ambient path cannot be
adopted. Serialized room continuations reuse the original room environment while
retaining their existing Host receipts, deduplication and native binding rules.

## Business runtime bindings

An Agent catalog can contain this additive, path-only policy:

```json
{
  "business_runtime": {
    "home_files": [
      {"source": "/operator/auth/tencent/default.credential", "target": ".tccli/default.credential"},
      {"source": "/operator/auth/tencent/default.configure", "target": ".tccli/default.configure"}
    ],
    "bin_directory": "/operator/reviewed-tools/bin"
  }
}
```

Paths above are examples, not discovered defaults. Omitted, null or empty policy
binds nothing and preserves the previous profile path/fingerprint. Source and bin
paths must be existing canonical absolute paths without symlink components; no
relative-path resolution or global credential discovery occurs. Sources must be
readable regular files: at most 16, 1 MiB each and 4 MiB total. The bin directory
is operator-reviewed; its contents are neither copied nor hashed. Select a stable
wrapper that pins a working executable/interpreter rather than inheriting a broken
Python installation or installing packages in each session. A colon is forbidden
in the bin path because PATH cannot represent it as one entry.

Targets are clean relative HOME file paths. Traversal, duplicates (including
case variants), overlapping file/parent targets and symlinks are rejected.
Native directories `.claude`, `.codex`, `.agents`, `skills`, instruction files
`CLAUDE.md`/`AGENTS.md`, and shell startup targets cannot be supplied as credential
bindings, with one exact lowercase exception: `.claude/build-hub/token` is a
Build Hub business credential leaf. Only that clean relative spelling is allowed;
case variants, alternate spellings, parents, children and sibling files remain
rejected. It copies only the selected source file, never `.claude` settings,
Skills or other native state, using the same validation and atomic 0600 writes.
The Web settings save preserves this object and validates its path
shape without reading credential contents; Host validation remains authoritative.

Host writes a private `business-runtime.json` path manifest in the selected native
profile. A nonempty policy adds `business-sets/<policy ID>` to the profile path;
the versioned ID hashes only sorted source/target paths and the bin directory,
never credential bytes or content digests. Policy changes therefore cannot
continue an old native environment. Removing/tampering with the selected policy
or encountering a different environment ownership record fails before dispatch.

Immediately before a business execution, Host reads bounded sources and checks
all destinations. It records the exact path policy in the private environment's
`business-runtime-policy.json`, then atomically copies each file with mode 0600
under private 0700 parents. Directory handles confine writes and reject symlink
parents. A pre-existing unowned destination is a conflict; the same recorded
binding may refresh on every turn, including resume. Credential rotation changes
the copies without changing the policy fingerprint or writing back to sources.
Multi-file refresh is not one transaction: a failure blocks dispatch and may
leave private copies from completed writes for a later explicit attempt.
Extraction skips manifest loading and binding entirely.

When a bin directory is selected, Host generates only PATH-prepending shell
initialization in `.zshenv`, `.profile`, `.bash_profile` and `.bashrc`. The path is
shell-quoted; no global dotfiles are sourced and HOME/XDG remain private.
Existing startup files must exactly match the generated content or preparation
fails. This supports zsh (including `-c`) and login bash; non-login bash can
require an absolute wrapper path. Host guidance names the exact tools directory
and excludes automatic relogin, package installation or account/provider fallback.
The binding adds no operation authority and does not alter native Skill selection.

There is no new Offload Env/Mount API: Host prepares its owned HOME before
passing the original isolated environment to the package. Model authentication
still follows Offload's selected profile/account, independently of business auth.
This is configuration isolation, not OS containment: owner-authorized tools can
read the copied files. The binding code never logs their contents, but does not
provide general redaction of arbitrary tool output. Operators must keep secrets
out of tool output and retain private environment storage according to policy.

Focused synthetic checks cover policy/profile identity, source rotation on fresh
and resumed execution, source/destination/manifest conflicts, no extraction
binding, unchanged Skill selection and isolated zsh/login-bash discovery. A fake
Claude executable exercises the actual package boundary without a provider.
These are source checks, not real POP/Tencent login, current production adoption
or live native-model acceptance. No credentials or business calls are needed.

On 2026-11-05, a separate bounded local CLI check used the configured `ops`
Agent, the existing CLI home and a candidate binary/catalog. Observed Codex
model `gpt-5.6-sol` verified POP login, Build Hub identity/inventory and a precise
Tencent instance read. The Host run was `7456f7fa-0b2e-494c-a041-b02c9f9b0665`,
session `6cb3b725-e4b2-4622-b68e-a9b09960b6f3`; private raw evidence is retained
under `~/.local/share/agent-host/handoffs/handoff-3rehqvip`. Independent checks
confirmed private HOME mode 0700, all nine selected copies matching their sources
at mode 0600, and 26 selected Skill directories. Execution completed, acceptance
was resolved and memory unchanged; no business resource writes or notifications
were sent. This validates the bounded authentication path, not general cloud
permissions, application health, channel adoption or successful VM restart.
Feedback remained `needs_recipient`, independently of execution acceptance.

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

## Direct execution policy

Both Claude and Codex receive the Host direct-execution instruction on every
business invocation, including continuation. The adapter advertises the policy
version in its execution identity; resumed calls receive current instructions. Offload
continues to own the outer model loop; Agents may not recursively invoke its CLI
or workers. Native Skill admission and final assembly reject an `offload`
directory or YAML declared name, including renamed directories. Other business
Skills remain available. These are behavioral/capability controls, not an OS
command sandbox: arbitrary shell execution retains its configured native policy.
