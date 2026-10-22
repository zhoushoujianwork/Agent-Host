# Simple settings

AGH settings follow OpenFilm's compact layout: a short navigation on the left,
only the choices needed to get started on the right. Settings open in a modal
over the current module; close with the × button, backdrop, Escape or Cmd+W.
Closing keeps unsaved form edits available when reopened in the same page.
The background module and Chat keep their state. Most users should be able
to keep the defaults. [Implementation details](settings-console-detail.md).

## What to set

- **General / 通用:** light, dark or system appearance. Changes apply immediately
  to the workspace and stay in this browser.
- **Agents / 智能体:** start with one default Agent; keep explicit project
  assistants, such as GTM, separate. Web/CLI aliases and channel defaults do not
  create extra Agent choices in the simple view. Additional Web Agents are
  optional under More Agent options. Use the Configuration tab to
  edit its name, working folder, installed user skills and knowledge sources.
  Keep default capabilities or select a custom set; an empty custom set mounts
  none from that group. Save, close settings and select the Agent at the top of
  Chat. Each Agent keeps its own conversation while the viewer is running.
  Overview, instructions, observed capabilities, work context and activity remain
  available for inspecting configured Agents.
- **Models and connections / 模型与连接:** choose a native model and, when using
  a custom service, its address and API key. Existing keys remain hidden.
- **Channels / 消息渠道:** enable an already configured channel and fill in its
  required account or application identities.
- **Shortcuts / 快捷键:** find the key for closing Chat or a selected record.

Save Agent, model and channel changes explicitly. Local task settings apply to
the next task; service settings apply after restarting Host. Saving checks the
configuration, preserves a private previous version and rejects conflicting
edits. A failed check restores the previous file. Saving does not execute tasks.

## Defaults and scope

Proposed addition: [Platform Skills / 平台技能](platform-skills-web-design.md)
becomes a shared Settings category alongside Agents. It manages agh-registered
procedures and explains enabled-plugin usage/readiness. Agent profiles show
their effective catalogs and link to the same registry. This is planned UI;
the current installed native-skill selector is not that platform catalog.

Advanced edits to execution profiles, timeouts, permissions, instruction sources,
memory policy, source connection setup, backups and CLI handoff parameters stay in
configuration files. Agent profiles show applicable sources read-only.
The simple page does not expose a generic JSON editor or
arbitrary field additions and removals. Saving a visible field preserves the
other saved values.

The [Agent profile](agent-profile-design.md) shows saved configuration and
bounded historical evidence. Explicit group bindings and observed default-routed
groups are distinguished; unknown loading state is visible. Web Agent capability
selection is supported; channel group-binding editing remains planned. CLI and channel destinations stay separate.

The page edits existing owner-local Host configuration. Initial installation and
native account sign-in remain in their existing tools. Standalone previews can
change appearance without a configured Host. Web Chat submits tasks through
its configured local Agent connection.
