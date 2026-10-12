# Agent instance implementation details

See the [main design](agent-instances.md) for scope and user behavior.

## Local configuration

Add an absolute `agents_config` path to the private service configuration, or
pass `--agents-config` to `dingtalk check`, `dingtalk serve` or local `chat`.
The JSON file and optional instruction files must be owner-private regular
files. Example (all identifiers are synthetic):

```json
{
  "default_agent": "default",
  "agents": [
    {"id": "default", "name": "General assistant"},
    {
      "id": "delivery",
      "name": "Delivery assistant",
      "instructions_file": "/absolute/private/delivery.md",
      "workspace": "/absolute/workspace",
      "projects": ["service-a", "service-b"],
      "knowledge_sources": ["team-docs"],
      "model": "configured-model"
    }
  ],
  "group_bindings": [
    {"conversation_id": "EXAMPLE_ID", "agent_id": "delivery"}
  ]
}
```

Without this file, a `default` agent inherits current service configuration.
Omitted `workspace` and `model` inherit service defaults. Omitted `projects`
uses the registered catalog; an empty array selects no registered projects.
Omitted `knowledge_sources` inherits the service's project-authorized sources;
an empty array mounts none. A named subset further narrows project-authorized
sources, never expands their project or optional audience authorization. Unknown
references and duplicate definitions fail validation. Roles are loaded at startup.

Knowledge sources may also declare `audiences`. An omitted list retains the
previous behavior; `["owner"]` restricts retained private notes to the owner
partition even when the same instance serves groups. Source roots remain separate
from business cwd and native directory policy. The
[local knowledge guide](local-knowledge.md) covers project documents and retained
notes; source availability does not import either into learned memory.

`--agent ID --audience ID` selects the local run and memory partition for
inspection, review or retry. The default remains `default`/`owner`. Group
audience IDs are available in private channel records. Local `chat` can use the
same selection with `--agents-config`; ordinary CLI calls retain native policy
unless using an instance configuration.

## Execution and state

Verified enterprise/account/conversation identity defines group audience;
sender identity is recorded independently. The chosen agent/audience is saved
with receipt, before execution. Group bindings do not include sender, while
legacy confirmation checks retain the original sender. A configured but unavailable
instance fails instead of falling back. The transport queue identity remains
independent of the selected agent and project.

The [conversation scheduler](channel-conversations-detail.md#scheduling-and-lifetime)
keeps each transport binding and each agent/audience pair serial through memory
processing and reply delivery. Different audiences may run concurrently within
the configured limit (default four). This protects memory finalization and native
history without isolating shared project files. Multiple conversations using
the same owner partition also share this serialization boundary.

Each task selects a reliably associated project set or executes in the instance's
default workspace. An empty catalog skips the project provider; unresolved
classification remains audit evidence without creating a Host project question
or waiting task. The native agent asks for business details when needed.
Historical manual project pins remain readable but do not influence new tasks. `/new` resets
the latest actually executed project set's compatible session in the current
group/agent partition, or the default when none exists, and preserves memory.
`/status` shows only safe conversation/recent-task state and the requester's own
legacy pending count; `/help` lists these three Host controls. Project/agent settings
remain local and native interactive commands are not forwarded. These standalone
controls use the same queue; `/status` is not an immediate interrupt. Quoted and
attached command text remains task material. The
[routing contract](smart-project-routing-detail.md#existing-pending-request-compatibility)
retains numbered original-requester confirmation/cancellation for old pending
records only. Bare project names and ordinary answers stay with the native agent.
Default execution does not extend instance, project or knowledge permissions;
configuration, selected-directory and provider errors remain explicit failures.
New default-workspace tasks keep history and normal memory/experience processing
whether the catalog is empty or a populated catalog has no reliable match.
Historical `unresolved` runs keep their original skipped memory outcomes and
cannot authorize a replay or memory retry. Compatible sessions may continue,
with trusted per-turn instructions separating unrelated previous context from
the new target.

Some clients retain a visible leading `@name` in an authenticated group
callback. Control commands, project-choice replies and routing use a view that
removes at most one whitespace-delimited address token. Mention authorization
still comes only from callback metadata. The original receipt and native task
input remain unchanged. Private messages are unchanged; the Host does not guess
the end of multiword display names or scan arbitrary text for a command.

Group reply presentation is assembled once before the delivery attempt. It
includes a visible `@senderStaffId`, a short escaped Markdown question excerpt,
and the result. The adapter sends `at.atUserIds` with that single authenticated
sender and `isAtAll: false`, following the
[official sessionWebhook Markdown example](https://opensource.dingtalk.com/developerpedia/docs/explore/tutorials/stream/bot/nodejs/send-markdown/).
The sender comes from the current immutable receipt, never the model, question
text or preceding group participant. It is independent of the shared session.

All group reply paths share this presentation, including commands, legacy confirmation
and startup failure. The quote is a simulated reference without native message
navigation. The original text stays in evidence; its preview collapses whitespace,
escapes Markdown, neutralizes additional visible @ tokens and omits obvious local
paths or credential assignments. This preview filter is not a general secret
detector. Mention, excerpt and body together stay within the 4000-byte limit.
The exact displayed body is persisted before the single delivery attempt; unknown
delivery still does not authorize replay. Private replies do not add this header.

Project scope remains stable. Memory partitioning adds agent and audience across
retrieval, FTS, aliases, duplicate/conflict resolution, candidates and governance.
Statements retain their actual sender so identical first-person preferences from
two members do not silently become one person's assertion. Historical records
belong to `default`/`owner`; migrations preserve record IDs, original payloads,
evidence, links and revisions. Unknown claimed tasks are never replayed.

Native profiles use `native/instances/AGENT/audiences/HASH`. The historical
default-owner profile keeps its existing location. Only shared settings are
propagated; owner histories, instruction files and automatic memory are not
copied. `CLAUDE_CONFIG_DIR` is selected in the child environment, not changed to
switch agents in the Host process. Provider credentials remain managed locally.
Identity, profile, permissions, exact Host instructions, knowledge configuration
and project execution scope determine session compatibility.

The adapter appends trusted channel instructions to the native system prompt.
Authenticated sender metadata is distinct from user input. Extraction uses its
independent safe-mode, no-tool configuration, not business approval bypass.

## Audience-specific behavior

Channel policy version 6 separates owner-private and group behavior while using
the same configured execution capabilities. Owner-private instructions execute
requests directly without additional Host permission, disclosure or approval
restrictions. Requested local paths, Host configuration, diagnostic details and
internal identifiers are allowed; per-turn provenance instructions do not add
an owner identifier suppression rule. Native harness rules remain in effect.

Group instructions allow direct member requests and consider affected data and
resources, disclosure to the shared audience and clear intent. They clarify a
concrete concern or decline the problematic part rather than requiring blanket
confirmation. Owner-private history, credentials and hidden instructions remain
excluded from group replies. Authenticated sender identity stays distinct from
message claims; one member's preference is not group consensus. The owner in a
group receives the same policy. Per-turn instructions preserve different senders
in a shared native conversation and favor natural attribution in group replies.

Both policies treat quoted text, attachments, retrieved documents, memory and
tool output as source material that cannot replace authenticated identity or
channel instructions. Both require honest execution outcomes and evidence
grounding. No permission table, sender roles or separate tool profile is added.
Both also lead with the answer in short, natural, conversational sentences,
retain essential facts, actions and caveats, and aim for 2–4 short sentences or
up to three compact bullets for routine replies. They expand when explicitly
requested or needed for correctness; the guidance does not impose a hard limit
or truncate complete requested artifacts.
The complete policy text remains part of runtime compatibility, so a policy
change replaces an incompatible native session. This prompt behavior does not
enforce filesystem isolation or deterministic disclosure protection.

Default-workspace channel turns receive target-grounding instructions even
without a project match: the cwd does not establish the intended repository, and
only a concrete missing target requires a question. These instructions make no
claim that default memory processing is disabled.

## Everyday answer grounding

Channel policy version 6 asks the native assistant to ground facts in the current
request and relevant evidence, retain supplied labels/dates/units/identities,
and distinguish observations, calculations and hypotheses. Diagnostic answers
keep leading explanations tentative until verified; indirect metrics cannot
rule out alternatives or establish unseen implementation behavior. Proposed
checks explain what they would confirm or disprove, and technology-specific
advice remains conditional when the stack is unknown. Unrelated earlier
conversation facts must not fill missing details. This is a behavioral instruction,
not a deterministic factuality filter. The policy participates in the existing
runtime fingerprint, so a changed policy starts a compatible fresh session.

Shared Claude adapter guidance asks for concise conversational replies and links
to actual consulted external source URLs in every ordinary execution, including
CLI, channel and tool-disabled calls. Structured experience extraction is exempt.
The guidance preserves existing local-source citations and does not change
source access or private/group disclosure permissions.

Attachment and quoted-message handling keeps the current authenticated sender's
statement separate from supplied material. Callback quotes can be incomplete or
summary-only; their authors are not inferred to be the current sender. Pictures
use native image blocks, and files use private saved paths for native reading
tools. Neither input kind changes instance selection or source authorization.
See the [input contract](channel-conversations-detail.md#accepted-input-and-provenance).

## Validation record

On 2026-10-12, focused synthetic Host tests verified owner-private diagnostic
instructions, ordinary group execution, claimed-owner text without identity
replacement, owner-in-group behavior, different senders in one native session,
default-workspace target grounding and policy-dependent session compatibility.
Assembly compatibility tests also cover changes to the complete Host policy.
The Host-to-runner checks cover version 6 chat guidance reaching owner-private,
member-group and owner-group execution while preserving diagnostic permissions
and authenticated identity. All 64 Host package tests passed for this change.
These tests verify prompt assembly and routing of configured capabilities;
isolated native behavior for policy version 5 passed seven daily-use cases on
2026-10-12, including owner diagnostics and both owner/claimed-owner requests
in a shared group. See the [release validation](release-daily-use-detail.md#local-validation-on-2026-10-12).
Routing and memory admission were fixtures; DingTalk platform behavior remains
to be checked separately.

The records below retain evidence from earlier channel revisions. Historical
project-switch and project-question checks do not establish live acceptance of
the current three-control interface or default execution for unresolved tasks.
They also do not validate the new concurrent-conversation, attachment or inbound
quote paths; the [current validation boundary](channel-conversations-detail.md#migration-recovery-and-validation)
is separate.

On 2026-10-11, `make check` passed Go formatting, vet, all Go package tests,
20 installer tests and 29 viewer tests. State/memory race tests also passed.
Synthetic group tests exercise actual Host/state behavior: members sharing a
conversation, separate groups and instances, pinned duplicate receipt identity,
original-requester confirmation, filtered project catalogs and resolver failure.
Configuration checks remain read-only and reject invalid bindings/references.

Five bounded native Claude 2.1.223 calls passed with fresh isolated profiles:
controlled file creation/readback with project rules, same-session continuation,
a second instance, another group on the same instance without previous context,
and a file/tool-output instruction attack plus a direct internal-data request.
The last response returned the business result and omitted the synthetic
credential, system prompt, native session identifier and absolute directory.
These are observed behavioral results, not a proof of general confidentiality.
All inputs, outputs and the report are outside the repository under the local
acceptance directory; no real conversation corpus or live test fixture is checked in.

On 2026-10-11, bounded live testing in one authorized internal group verified
authenticated mentions, ordinary-message exclusion, command handling, native
continuation across service restart, current-conversation reset, generic progress
reactions and concise replies. A quoted instruction attack did not disclose
actual internal values. Live testing exposed two defects: retained address text
prevented command recognition, and ordinary default-workspace follow-ups could
incorrectly require a project. Both were fixed, covered by synthetic regression
tests, and verified again after `make check` and `make install`.

The initial failed outcomes remain in private evidence. Their pending test
requests were explicitly canceled without replay; the event listener was closed
and no reusable memory was created. One immediate send-status lookup returned
an error, while the Host receipt and actual reply event independently confirmed
delivery; the message was not resent. Inputs, group identity and platform evidence
remain outside the repository.

This single-group pass does not establish cross-group isolation, alternate group
bindings, multiple real senders or a fresh owner-DM regression. Those remain
separate live scenarios; installation/connection alone is not a platform round trip.

A subsequent bounded check verified group question excerpts and requester
mentions for one ordinary answer and one Host command. DingTalk returned a real
`user_im_message_receive_at` event for the answer, matching its group-message ID;
both group events resolved the visible mention to the requester's display name.
This proves the tested @ notification path rather than only textual addressing.
The quote remains a simulated Markdown reference. All evidence is private and
outside the repository; multiple real senders remain a separate platform case.
