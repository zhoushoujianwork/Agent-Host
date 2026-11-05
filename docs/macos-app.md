# macOS workspace app

Agent-Host now has a source-built AppKit shell with a persistent menu bar entry,
a WKWebView workspace window, a local inbox and actionable macOS notifications.
It embeds the existing workspace and starts its own loopback viewer, without
starting or restarting channel services. Browser inspection remains available.
[Build, configuration and acceptance details](macos-app-detail.md).

Build on macOS 13+ with Xcode Command Line Tools, Go, and Python 3.10+:

```sh
make macos-app
```

Open `dist/Agent-Host.app`. On first launch the app creates its own owner-private
runtime in `~/Library/Application Support/Agent-Host/runtime` and starts the
bundled backend; there is no Host to connect to. It never adopts a production or
global configuration. The runtime seeds a "Local assistant" Agent with an exact
empty `native_skills` allowlist and a private Host workspace. Its handoff omits
`--offload-config`, so execution inherits the current Offload default. It creates
no ACP configuration, native Claude/runtime profile, model override or provider
credential.

Chat is ready when the private Host paths, Agent catalog and bundled executable
validate. **Local Agent Settings…** opens the existing Settings page: Agent edits
retain all current fields, while *本地执行* documents that backend/model selection
belongs to Offload and is not synthesized by the app. Saving is refused while a
task or closeout is active, never replays work and keeps drafts. The workspace
lists tasks from all Agents in the app's own local state.

**Import Existing Configuration…** (advanced) selects an existing
`task-handoff.json`. Building with explicit `APP_ARGS='--home … --chat-config …'`
remains an advanced fixture mode and bypasses the app runtime.
[Setup and runtime details](macos-app-detail.md#app-owned-runtime).

The File and Window menus reuse the status popover's actions: Workspace (Cmd-1),
Inbox (Cmd-2), New Task (Cmd-N), Settings (Cmd-,) and Show Status (Cmd-Shift-M).
New Task is disabled until local execution prerequisites are configured; it never
starts work silently. App Preferences holds setup and completion-alert options.
The Edit menu routes copy, paste and undo to the workspace.

Importing validates the file before you confirm its Agent and task destination. The
app reconnects its own idle viewer, retains the draft and mounts the chosen CLI
source; other Host services keep running. Configuration errors have one recovery
action, with technical details folded away.

[Source updates](mac-app-updates.md) share the native App Preferences surface.
Stable builds update automatically when idle; dev builds require Update.

Unsent drafts have an owner-private backup per configured source set, so reopening
the app or changing its local viewer port retains them. A connection switch carries
the current draft forward without sending it.

The compact popover (at most 360×400) shows status with the attention count, the
selected task's title, Agent and brief progress, Open task, Open Workspace,
New Task and Inbox. Stop appears only for an active execution and is enabled only
when that execution's Host publishes control; a request remains pending until Host
records its outcome. Closing a window keeps the menu entry alive; Quit closes the
app's viewer without stopping other Host services.

The inbox retains started, execution-completed, failed, blocked-input, unknown and
stopped events even when macOS denies notifications. It lists one row per task
(latest event, relative time, unread marker) with an All / Needs attention filter
and search. Opening a task marks its current events read. Acknowledge clears the
task's current attention events (failed, input-needed, unknown) without marking
them read; a later event on the same task is new again. The attention count in the
status counts tasks with unacknowledged attention events, not old failed records.
Notifications are quiet: failed and input-needed by default, completions opt-in,
never started or stopped, and none for the task already open in a visible, key
Workspace window. Historical events populate the inbox without an alert flood.
System notification actions open the matching execution record.

Current identity is a source-qualified Host **run**, not a newly implemented
cross-turn business Task. Execution, acceptance and memory outcomes stay separate.
Historical protocol evidence remains viewable in existing records, but the app
does not configure ACP or add protocol-specific execution behavior. Ordinary
Agent questions remain in the original conversation; the app does not infer
requests from progress prose.

Headless mapping, identity, deduplication, persistence, read/acknowledge, notification policy and stop-state tests and
native API compilation are part of `make check` on macOS. Desktop smoke checks
(launch, menus, popover, inbox search/filter, VoiceOver, workspace/task opening and real notification delivery)
remain pending; compilation does not establish those acceptance results.

This document describes source delivered in this checkout. It does not establish
that a separately installed app has adopted it. Visual/desktop acceptance remains
pending until the smoke procedure above is run on macOS; headless checks and native
compilation are source evidence only.
