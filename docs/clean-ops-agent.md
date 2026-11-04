# Clean operations Agent

Candidate source behavior for the local upgrade; this document does not claim production adoption.
See the [implementation contract](clean-ops-agent-detail.md).

## Intended behavior

CLI chat, DingTalk and Feishu use the same Host execution path. Normal business
turns follow the current Offload default; explicit selections remain available
for diagnosed compatibility needs. The observed profile, model and purpose are
retained as evidence.

Each stable Host conversation gets its own persistent native environment. Its
native cwd is a fresh, empty Host-owned runtime workspace; the business workspace
and any additional directories are explicit task roots. Continuation reuses the
original environment and native identity or fails. A fresh Agent does not import
the operator's native configuration, conversation history, plugins, or user
instructions. Host explicitly supplies its authorization, reviewed context and
operations Skills. This is configuration isolation, not an OS filesystem or
network sandbox.

The operations skill set comes from an explicit inventory of the existing global
skills. Hidden backups, duplicates, development/media skills and credential caches
are excluded. Managed profile roots and digest-verified task packages are the only
native Skill sources. Source references and a manifest make the mounted set
reviewable. Loading a skill does not execute it or grant account permissions.

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

The source candidate is covered by controlled fixtures and an isolated live
Skills/continuation check. Production rollout remains separate: back up the local
binary and configuration, preserve existing identities and evidence, start new
compatible conversations, then verify CLI chat and each configured channel with
marked harmless tasks. Live channel destinations must be identified before any
message is sent.

Existing channel authorization policy is preserved; this upgrade does not claim
new non-owner restrictions, change `shared-execution-v2`, or permit arbitrary
operations during acceptance. No production deployment is claimed complete.
