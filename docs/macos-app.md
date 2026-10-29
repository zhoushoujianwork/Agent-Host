# macOS workspace app

Agent-Host now has a source-built AppKit shell with a persistent menu bar entry,
a WKWebView workspace window, a local inbox and actionable macOS notifications.
It embeds the existing workspace and starts its own loopback viewer, without
starting or restarting channel services. Browser inspection remains available.
[Build, configuration and acceptance details](macos-app-detail.md).

Build on macOS 13+ with Xcode Command Line Tools, Go, and Python 3.10+:

```sh
make macos-app APP_ARGS='--home /absolute/development/cli-state --chat-config /absolute/development/task-handoff.json'
```

Open `dist/Agent-Host.app` locally. Explicit state/config paths are required;
the build never adopts or modifies a separately deployed production runtime.
For full settings and memory access, pass the existing viewer's `--backup-root`.
Mount other local sources with repeated `--channel-home NAME=/absolute/state`
instead of `--home`. Use the same sources and CLI destination as the intended
workspace; the app does not create a second task database or configure Agents.

The menu identifies active executions and their Agent. Select one to open its
exact record and attach its conversation through the existing workspace controls.
Stop is available only when that execution's Host publishes control; a request
remains pending until Host records its outcome. Closing a window keeps the menu
entry alive; Quit closes the app's viewer without stopping other Host services.

The inbox retains started, execution-completed, failed, blocked-input and stopped
events even when macOS denies notifications. Completion notifications default to
off and are quiet when enabled. Historical events populate the inbox without an
alert flood. System notification actions open the matching execution record.

Current identity is a source-qualified Host **run**, not a newly implemented
cross-turn business Task. Execution, acceptance and memory outcomes stay separate.
ACP permission requests that the Host cancels are shown as input-needed evidence
with the conversation destination; interactive permission approval and a paused
question/answer protocol are still unsupported. Ordinary Agent questions remain
in the original conversation; the app does not infer requests from progress prose.

Headless mapping, identity, deduplication, persistence and stop-state tests and
native API compilation are part of `make check` on macOS. Desktop smoke checks
(launch, popover, VoiceOver, workspace/task opening and real notification delivery)
remain pending; compilation does not establish those acceptance results.
