# Web workspace

The local workbench now opens a shared workspace inspired by OpenFilm's studio
and side Chat layout. Use the left navigation for the canvas, memory, execution
records and settings. Chat lives only in the right panel, alongside those modules.
[Implementation notes](web-workspace-detail.md).

## Available now

- Arrange conversation cards on a dotted canvas. Drag a card's header or focus
  it and use arrow keys. Search by request, Agent or project; reset the layout
  with **整理画布**. Positions stay in this browser.
- Open the latest execution from a conversation, or add its request and run ID
  to a Chat draft. **打开 Chat** and the top-bar Chat toggle open the right panel;
  switching modules keeps its current open/closed state. Drafts save locally
  and survive page reloads. Task starters append to the draft without replacing it.
- Explore the existing memory graph, source evidence and memory library.
- Set essential Agent, model connection and existing channel choices in the
  compact [settings page](settings-console.md). Advanced options stay in config.
- Choose light, dark or system appearance in the top bar or Settings. All pages,
  including the graph, forms and detail dialogs, share the same theme. The
  preference stays in this browser and follows system changes in system mode.

Memory puts the relationship graph and recent recall first, followed by the
library and experience review queue. Execution records use a journal rail,
status summary and segmented detail tabs. Settings separates instant appearance
preferences from checked Host configuration saves.

Switching panels retains settings edits. Saving settings preserves the existing
validation, conflict detection, secret masking and private backup behavior.
Service changes still require a deliberate restart.

Press **Cmd+W** to close the right Chat panel and stay on the current module.
When Chat is already closed, the shortcut closes a conversation detail back to
the canvas. The top-bar close button does the same. Drafts and execution records
remain intact. With neither open, native close behavior is retained. The workspace
uses the shared AGH vector mark in navigation and icons.

## This increment

This is the outer workspace and settings integration. Chat is explicitly a draft
entry: it does not send requests or execute work. Use the configured CLI or
channels for execution, then refresh the canvas. The canvas shows conversations
represented in the loaded records, rather than claiming a complete chat archive.
Execution, acceptance and memory outcomes remain separate in execution details.

Run `make viewer` for local inspection. A checkout build does not deploy the
separately installed production runtime. Web task submission, streaming replies
and a full conversation transcript are follow-up work.
