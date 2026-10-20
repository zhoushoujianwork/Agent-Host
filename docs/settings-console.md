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
- **Agents / 智能体:** inspect each configured Agent's overview, instructions,
  capabilities, work context, channels/groups and activity; keep the existing
  default-Agent, display-name and working-folder edits.
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

Advanced edits to execution profiles, timeouts, permissions, instruction sources,
memory policy, datasources, backups and CLI handoff parameters stay in
configuration files. Agent profiles show applicable sources read-only.
The simple page does not expose a generic JSON editor or
arbitrary field additions and removals. Saving a visible field preserves the
other saved values.

The [Agent profile](agent-profile-design.md) shows saved configuration and
bounded historical evidence. Explicit group bindings and observed default-routed
groups are distinguished; unknown loading state is visible. Capability and
group-binding editing remain planned. CLI and channel destinations stay separate.

The page edits existing owner-local Host configuration. Initial installation and
native account sign-in remain in their existing tools. Standalone previews can
change appearance without a configured Host. Web Chat submits tasks through
its configured local Agent connection.
