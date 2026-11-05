# Clean operations Agent

Source behavior for clean operations Agents; runtime adoption has separate evidence.
See the [implementation contract](clean-ops-agent-detail.md).

## Behavior

CLI chat, DingTalk and Feishu use the same Host execution path. Normal business
turns follow the current Offload default; explicit selections remain available
for diagnosed compatibility needs. The observed profile, model and purpose are
retained as evidence.

Each stable Host conversation gets its own persistent native environment. Its
native cwd is a fresh, empty Host-owned runtime workspace; the business workspace
and any additional directories are explicit task roots. Continuation reuses the
original environment and native identity or fails. A routed Agent, including the
default owner Agent, does not import the operator's native configuration,
conversation history, plugins, user instructions, or ambient Skills. Host
explicitly supplies its authorization, reviewed context and exact selected
Skills. Omitted, null, and empty Agent Skill selections all mean zero native
Skills. CLI and lite channel execution without an Agent catalog also uses a
private Host-owned zero-Skill profile; it does not fall back to the operator's
ambient native profile. This is configuration isolation, not an OS filesystem
or network sandbox.

The operations skill set comes from an explicit inventory of the existing global
skills. Hidden backups, duplicates, development/media skills and credential caches
are excluded. Managed profile roots and digest-verified task packages are the only
native Skill sources. Source references and a manifest make the mounted set
reviewable. Loading a skill does not execute it or grant account permissions.

## Explicit business runtime

An Agent may opt into `business_runtime`: selected credential files are copied
into its private HOME, and an operator-reviewed tools directory can be added to
shell PATH. Omitted policy shares nothing. Selecting a global Skill alone does
not share business authentication or shell initialization.
The exact lowercase `.claude/build-hub/token` business credential is supported;
all other `.claude` binding targets remain excluded.

Copies refresh before every business turn, including continuation; extraction
receives none. Source files stay unchanged. Binding policy changes select a new
profile; rotating file contents does not change policy identity. Tools remain
subject to the original task authorization, with no account fallback or automatic
relogin/package installation. Native HOME/XDG, settings and Skill selection stay
isolated. [Schema, limits and shell support](clean-ops-agent-detail.md#business-runtime-bindings)
describe source behavior verified with synthetic files and isolated shell/fake
native fixtures. A bounded local Codex check also verified configured POP,
Tencent and Build Hub authentication with private HOME and 26 selected Skills;
channel rollout and broader business acceptance remain separate.

## Model responsibilities

The default Codex model handles supported business execution and native tool
loops through Offload's explicitly enabled native app-server adapter. It owns its
native thread, and its observed model is retained separately from the request.

Codex currently cannot prove complete native tool disablement. Required
tool-free memory and experience extraction therefore use a separately configured,
explicit compatible Offload profile. This is a declared internal component,
not a retry or fallback for a failed business turn. Existing memory acceptance
and source-evidence gates remain in force. Unsupported strict tool restrictions
continue to fail before dispatch.

Native forks are currently unsupported for both Codex and Claude: the isolated
child conversation cannot yet bind the original parent's private environment.

## Acceptance and rollout

The source is covered by controlled fixtures and an isolated live
Skills/continuation check. Production rollout remains separate: back up the local
binary and configuration, preserve existing identities and evidence, start new
compatible conversations, then verify CLI chat and each configured channel with
marked harmless tasks. Live channel destinations must be identified before any
message is sent.

Existing channel authorization policy is preserved; this upgrade does not claim
new non-owner restrictions, change `shared-execution-v2`, or permit arbitrary
operations during acceptance. Consult runtime receipts for deployment and channel
acceptance outcomes.
