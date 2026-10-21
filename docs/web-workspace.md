# Web interface

AGH opens on the memory graph, with execution records and settings in the left
navigation and Chat on the right. The conversation canvas/workbench is temporarily
disabled. [Implementation notes](web-workspace-detail.md).

## Available now

- Explore the memory graph, source evidence, memory library and review queue.
- Inspect execution records, tool evidence and separate execution, acceptance
  and memory outcomes.
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
- If another CLI task owns the configured state directory, Chat explains that
  execution has not started. Wait for that task to finish, then send manually;
  no new conversation is required and nothing is automatically retried.
- Drafts save locally. The composer grows with the draft and shows its character
  count. Clear can be undone until the next edit. Escape collapses Chat.
- Stop requests cancellation of the active Host execution and waits for its exit.
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

There is no workbench entry, conversation canvas, card dragging or layout
management in the active interface. Existing browser layout data is retained for
a later return. Old workbench links open memory; old execution bookmarks retain
their selected record.

Chat uses the configured CLI destination within the selected Host root, separate
from running channel services. A standalone viewer can select an explicit private
`--chat-config`; see the [setup notes](web-workspace-detail.md#web-chat-execution).

Run `make viewer` for local inspection. The separately installed production
runtime is not updated by a checkout build.
