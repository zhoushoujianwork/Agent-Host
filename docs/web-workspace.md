# Web interface

AGH opens on the memory graph, with execution records and settings in the left
navigation and Chat on the right. The conversation canvas/workbench is temporarily
disabled. [Implementation notes](web-workspace-detail.md).

## Available now

- Explore the memory graph, source evidence, memory library and review queue.
- Inspect execution records, tool evidence and separate execution, acceptance
  and memory outcomes.
- Click a Session heading or a message in execution records to open its history
  in the right Chat panel and continue from its native context. CLI, DingTalk
  and Feishu sessions retain their original Agent, project, model and audience.
  Active sessions synchronize reply text and tool activity automatically and
  expose **Stop execution** when the owning Host provides run control.
  Web replies stay in Web; later messages in the same channel room can inherit
  the completed Web turn. Busy, unavailable or incompatible contexts show an
  explicit error instead of silently starting over.
- Set essential Agent, model connection and existing channel choices in the
  compact [settings page](settings-console.md). Advanced options stay in config.
- Open or close the right Chat panel from the top bar. Switching modules keeps
  its current state. Enter sends to the configured Host Agent; Shift+Enter adds
  a line. Claude replies render Markdown as they stream. Expand the activity summary
  to inspect actual tool calls, parameters, results and earlier commentary in order;
  the final reply stays visible below it. The summary counts tool calls, not Host phases.
- Select message, reply or tool text with the mouse and copy with Ctrl/Cmd+C.
  Copy buttons are available for prompts, completed replies (Markdown) and code
  blocks, with success or failure feedback. Managed installation includes the
  Markdown renderer and parser needed by Chat.
- Independent Web and CLI conversations execute concurrently in the configured
  CLI state directory. The same conversation has one execution owner; exclusive
  maintenance or an older CLI process can still block startup. See
  [concurrent CLI tasks](cli-concurrency.md). Nothing is automatically retried.
- Drafts save locally. The composer grows with the draft and shows its character
  count. Clear can be undone until the next edit. Escape collapses Chat.
- Stop requests cancellation of the selected Host execution and waits for its
  recorded outcome. Other concurrent executions continue. Older running Hosts
  without run control remain observable and must be stopped at their original entry.
  Partial text and native output remain visible. After failure or interruption, choose
  New conversation before sending again; no task is automatically replayed.
- Chat reconnects to the current viewer conversation after refresh. New conversation
  clears that Web view while durable Host execution records remain available.
- Choose light, dark or system appearance across all pages.

Press **Cmd+W** to close the right Chat panel and stay on the current page.
With Chat already closed, the shortcut closes a selected execution detail back
to execution records. The close button does the same. Neither action deletes
data or stops a run.

Settings remain loaded across module switches. Checked saves preserve private
previous versions, protect secrets and reject conflicting edits. Service changes
still require a deliberate restart.

## Temporary scope

The proposed [Platform Skills Settings subsection](platform-skills-web-design.md)
provides the Web entry for agh-managed methods, plugin usage guidance and
default-Agent availability. It fits inside the existing Settings surface rather
than adding another workspace module. It is not delivered by this design update.

There is no workbench entry, conversation canvas, card dragging or layout
management in the active interface. Existing browser layout data is retained for
a later return. Old workbench links open memory; old execution bookmarks retain
their selected record.

New conversations use the configured CLI destination within the selected Host
root. Selecting a channel Session connects to that channel's running Host through
its private local control socket. A standalone viewer can select an explicit private
`--chat-config`; see the [setup notes](web-workspace-detail.md#web-chat-execution).

Run `make viewer` for local inspection. The separately installed production
runtime is not updated by a checkout build.
