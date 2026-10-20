# Web workspace

The local workbench now opens a shared workspace inspired by OpenFilm's studio
and side Chat layout. Use the left navigation to move between conversations,
Chat drafts, memory and settings. [Implementation notes](web-workspace-detail.md).

## Available now

- Arrange conversation cards on a dotted canvas. Drag a card's header or focus
  it and use arrow keys. Search by request, Agent or project; reset the layout
  with **整理画布**. Positions stay in this browser.
- Open the latest execution from a conversation, or add its request and run ID
  to a Chat draft. Drafts save locally and survive page reloads.
- Explore the existing memory graph, source evidence and memory library.
- Edit Agents, models, providers, channels, instructions, data sources, people,
  backups and CLI handoff through the existing checked settings console.

Switching panels retains settings edits. Saving settings preserves the existing
validation, conflict detection, secret masking and private backup behavior.
Service changes still require a deliberate restart.

## This increment

This is the outer workspace and settings integration. Chat is explicitly a draft
entry: it does not send requests or execute work. Use the configured CLI or
channels for execution, then refresh the canvas. The canvas shows conversations
represented in the loaded records, rather than claiming a complete chat archive.
Execution, acceptance and memory outcomes remain separate in execution details.

Run `make viewer` for local inspection. A checkout build does not deploy the
separately installed production runtime. Web task submission, streaming replies
and a full conversation transcript are follow-up work.
