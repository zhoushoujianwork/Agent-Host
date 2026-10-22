# Agent profile and loaded configuration

Status: the first read-only profile is implemented in Settings → Agents.
Web Chat Agent creation and editing are implemented: name, working folder,
installed user-skill selection and knowledge-source selection can be saved from
the Configuration tab. Saved Agents appear in the Chat selector. Role, project
and group-binding editing remain planned. Existing default-Agent edits remain
available. See [settings](settings-console.md).
[Implementation notes](agent-profile-design-detail.md).

The proposed [Platform Skills subsection](platform-skills-web-design.md) adds
a shared management entry and a Platform Skills subview under this profile's
Capabilities tab. The default Agent shows automatic effective-catalog discovery;
publication, plugin configuration and shared enablement remain in the platform
view. Existing native-user skill selections retain their current meaning.

## Goal

Open an Agent and understand its whole working setup: what it is responsible for,
which capabilities it can use, which context it receives, and which people and
groups it serves. Start with the existing default Agent rather than requiring
users to create another Agent to see this information.

Use the supplied work-partner screenshot as a layout reference: an identity
header, a few clear tabs, and capability cards. Its example skills and platform
features are illustrative, not features to install into Agent-Host.

## Entry and layout

Settings → Agents shows configured Agent catalogs and their profiles. Selecting one opens its profile. A
default badge explains routing: it handles requests without an explicit Agent
selection or group binding in that configuration. It does not imply that the
Agent sees every group or has additional permissions.

Illustrative layout; counts and names below are placeholders:

```text
Agents / General assistant                         [Open Chat]
Default for DingTalk · Uses service model · Configuration saved
Answers team questions and completes authorized work

Overview | Instructions | Capabilities | Work Context | Channels & Groups | Activity

Overview
  Responsibilities     Role instructions and their source
  Execution            Engine, model and inherited settings
  Capabilities         Skills, connectors and Host features
  Work context         Working folder, projects and memory scope
  Service audience     Explicit group bindings + default routing
  Configuration state  Saved / Loaded / Requires restart / Unknown
```

Open Chat must identify its actual execution destination. If Web Chat uses a
separate CLI Agent, explain that and do not label it as this channel Agent.

## What each tab answers

| Tab | User question | Contents |
| --- | --- | --- |
| Overview | What is this Agent's complete setup? | Responsibility, engine/model, capability summary, audience and configuration state; links to details. |
| Instructions | How should it work? | Role and instruction sources, including inherited Host guidance; show which content is editable. |
| Capabilities | What can it use? | Skills, connectors/data sources, and Host features in separate subviews, with descriptions and configuration origins. |
| Work Context | What information and environment does it receive? | Default working folder, eligible projects, context files and memory boundaries. Show that actual task selection can narrow this set. |
| Channels & Groups | Where does it serve, and why? | Channel/account, known group name or ID, explicit binding versus default routing, and last observed activity. |
| Activity | What has it actually done? | Related executions and outcomes, linked to existing evidence; keep execution, acceptance and reply delivery distinct. |

Capability cards explain the business use, configuration origin and available
evidence. Distinguish **configured**, **observed loaded**, **verified in a run**,
**unavailable** and **unknown**. A configured connector is not proof of a working
account; a published Skill is not proof of successful execution. Search filters
the visible inventory. Do not display guessed capability counts.

Group rows distinguish explicit bindings from observed groups routed through the
default Agent. An explicit binding can exist without activity, and a default
Agent can receive a previously unseen group. Show “no recorded groups” rather
than “serves no groups” when history is absent. Group conversations and memory
remain separate even when the same Agent serves them.

## First delivery and important choices

1. Deliver a read-only profile for every existing Agent, starting on Overview.
   Assemble current configuration and available runtime evidence; mark missing
   evidence honestly. Preserve existing name, folder and default edits.
2. Add checked editing of role instructions, then group bindings and project
   scope. Preview the affected Agent/groups before saving; service changes show
   that a restart is required. Do not claim hot reload.
3. Add capability management only where a real Agent-specific loading contract
   exists. Enabled datasource plugins are currently shared during Beta; their
   cards link to shared configuration and explain the cross-Agent effect.

The first delivery excludes a marketplace, speculative skill installation,
per-Agent datasource switches and invented proactive-work controls. It does not
change routing, execution permissions, memory isolation or production services.

Acceptance: an operator can explain what the default Agent does, where each
visible capability comes from, which groups explicitly select it, where default
routing applies, and whether displayed settings are known to be loaded—without
opening raw JSON. Unknown information remains visibly unknown.
