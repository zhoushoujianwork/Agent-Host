# Group conversations and agent instances

Status: implemented; automated, isolated native and bounded single-group platform
verification are recorded in the
[implementation and validation details](agent-instances-detail.md).

## What users can do

Enterprise members can mention the application bot in a group to ask for help.
Ordinary group messages are ignored. Unbound groups use the default agent;
the operator can bind selected groups to agents with different responsibilities,
models, projects and knowledge sources. The existing owner private chat remains.

Members of one group share its conversation and source-linked task context. Each
task selects a project only when the association is reliable, otherwise running
in that agent's default workspace. An empty catalog does not block execution.
The native assistant asks for business details when needed; the Host creates no
new project questions or waiting requests. Separate groups keep separate
conversations and learned memory, even when they use the
same agent. Shared knowledge comes from explicitly configured sources. Previously
pending tasks remain recorded without replay; only their original requester can
explicitly confirm or cancel them. `/new` resets the current group/agent/project
conversation and keeps memory. `/status` shows
a safe conversation summary and any legacy pending-task count; `/help` lists these
three Host controls. Native interactive commands are not forwarded.
The [conversation extension](channel-conversations.md) keeps same-group turns
ordered while allowing separate audiences to progress concurrently. It also
accepts pictures, files and callback-provided quoted material. Only standalone
controls are interpreted by the Host; `/status` waits behind earlier turns.

## Configuration and behavior

The operator maintains a private `agents.json` (start from the
[example](../config/component-agents.example.json)), referenced by `agents_config`
in the service configuration. It defines a default agent, named agents and group
bindings. Changes take effect after restart; `service check` validates them
locally. The Host command interface has no project-setting, instance-configuration
or group-binding controls. Historical manual project selections remain available
in local records but no longer affect routing.

The Host verifies identity and mention, chooses the configured agent, and
assembles a reliably associated project set or its default environment.
Default execution does not expand project, memory or knowledge permissions.
Invalid configuration, missing selected directories and provider errors fail
explicitly; uncertainty does not switch to another agent instance.
Requests without a reliable project match use normal default-workspace history
and memory processing, including when the catalog is populated. Historical
unresolved runs retain their original memory outcomes without replay.
Claude retains its planning and tool loop. Business calls use
`bypassPermissions`, as selected by the operator; automatic native memory stays
off and experience extraction stays separate and tool-free.

Channel replies lead with the answer in short, natural sentences, keeping the
essential facts, actions and caveats. Routine answers aim for 2–4 short sentences
or up to three compact bullets, expanding when requested or needed for
correctness. Answers based on external
sources link to the actual sources consulted; existing local-source citations
remain available.

The owner in private chat can request local paths, configuration and diagnostic
details, with no additional Host permission, disclosure or approval restrictions. Native harness rules and
configured capabilities still apply. Full evidence remains in local records and
the viewer.
Group replies also quote a short preview of the current question and @ its
verified sender, so members can see which question an answer belongs to.

Group members invoke the configured Agent directly. Its system prompt considers
whose data or resources the request affects, whether the result can be shared,
and whether the intent and sender's authority are clear. Ordinary requests run
directly; a concrete concern leads to clarification or declining the problematic
part. Owner-private conversations, credentials and hidden instructions stay out
of group replies. The owner speaking in a group follows the same group behavior.
No sender roles or per-member tool permissions are added.

Trusted Host instructions preserve authenticated identity, source attribution
and honest outcome reporting for both audiences. Instance role instructions
extend these rules without replacing Claude's native system prompt. **With full
tools and approval bypass, these are behavioral constraints, not filesystem isolation or
a guarantee against disclosure.** Project and knowledge selections control Host
assembly; they do not sandbox native file access.

## Delivery boundaries

Delivery proceeds through configuration and partitioned state, native assembly,
group intake and presentation, then regression and deployment. Automated tests,
isolated native checks and actual DingTalk acceptance are recorded separately.
Real conversation samples and platform evidence stay outside the repository.

The same conversation and agent/audience memory partition stay serial through
reply and memory completion; other audiences use a bounded concurrent pool.
Shared project files are not isolated. The extension does not monitor unmentioned
group traffic, add multi-agent orchestration, or introduce another execution
engine. Earlier platform records do not establish acceptance of the new
concurrency and attachment paths.
