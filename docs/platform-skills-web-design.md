# Platform Skills in the Web interface

Status: proposed UI, 2026-10-22. This document does not implement the page.
It adapts the [managed Skills architecture](managed-skills-design.md) to the
existing Settings and Agent profile. [Technical details](platform-skills-web-design-detail.md)
cover data, transitions and acceptance.

## Entry and purpose

Add **Platform Skills / 平台技能** to Settings, alongside Agents, models and
channels. It is the shared agh-managed catalog, not another Agent-specific skill
folder or memory-review queue. Keep the workspace's main navigation unchanged.
Opening or closing Settings preserves the current module and Chat state.

An operator should quickly answer: which reusable procedures are managed, which
ones the default Agent can use, why another Skill is unavailable, and what changed
between versions. The page also shows the usage instructions supplied by enabled
plugins, so tool availability and instructions are understandable together.

## Page layout

Illustrative text and names; counts must come from the selected Host:

```text
Settings
  General
  Agents
  Platform Skills  ← selected
  Models and connections
  Channels

Platform Skills                         [Import existing Skill]
Default Agent automatically discovers ready, enabled platform Skills

Managed | Awaiting registration          Search...  Source ▾  State ▾

Skill / purpose       Source              Platform state     Default Agent
SFTP delivery         Operator-authored   Enabled · Ready    Available
Confluence usage      Plugin · Confluence Unavailable        Plugin disabled

Select SFTP delivery → Description | Version and source | Loading history
```

The list prioritizes business purpose, source, readiness and default-Agent
availability. Open a detail panel to read the full instructions and references,
inspect review/version history and see actual loading evidence. A table can
collapse to cards on smaller screens without removing the reason for a blocker.

## Important interactions

- **Register an existing Skill:** select an installed/local candidate, preview
  its instructions, references and validation findings, then explicitly register
  and publish it. A folder being found is not publication. Changed source files
  appear as a new version awaiting review; the active version stays identifiable.
- **Manage a procedure:** enable, disable or publish an approved revision from
  its detail panel. Preview affected Agents and dependent capabilities before
  saving. Changes apply at the next compatible execution; active runs are not
  silently stopped or replayed.
- **Inspect plugin usage:** show the owning plugin, supported tools and the
  reason it is ready or unavailable. Link to its shared plugin configuration;
  plugin usage is provider-owned and follows plugin enablement, rather than
  offering an unrelated per-Skill toggle or arbitrary tool editor.
- **Understand the default Agent:** its profile gains a Platform Skills view
  under Capabilities, showing its effective catalog and links to this shared
  page. Explain proactive selection and loading; do not require selecting every
  platform Skill manually. Other Agents show their actual declared policy.

Keep “available,” “loaded in a recorded run,” “tools invoked” and “business
verification passed” separate. An empty activity history means no recorded
evidence, not an unusable Skill. Secrets and full private business messages never
appear in previews or loading history.

First delivery includes catalog/list/detail, explicit registration and checked
procedure enablement, plugin readiness explanations, and the Agent's effective
view. A marketplace, general code editor, automatic task execution and a new
plugin configuration product are outside this increment.
