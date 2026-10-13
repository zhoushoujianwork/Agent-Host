# Agent conversation history and topic discovery

Status: both routes are implemented. Selected live DM checks passed on
2026-10-11–12: saved history was recovered after `/new` and a service restart;
DWS collection produced cited topics and completed a capped range. Their
acceptance evidence, setup and remaining checks are recorded in the
[implementation notes](conversation-history-detail.md).

## Goal and everyday use

Ask the Agent “What did we decide yesterday?” or “Find our earlier deployment
discussion,” including after `/new` or a restart. The Agent can search saved
messages and read a small surrounding window. Answers retain who said what,
when, and whether the saved reply was accepted for delivery.

| Route | What it records | How it is used |
| --- | --- | --- |
| Agent conversation history | Accepted owner-to-Agent private messages, admitted group mentions, and saved Host replies | Built-in read-only MCP search/read during a DingTalk task |
| DWS topic discovery | Bounded history from explicitly selected private/group conversations under one fixed personal profile | Independent system collection, local topic review and cited evidence export |

History needs no DWS login. It covers conversations addressed to Agent-Host,
including saved formatted replies and delivery failures. Unaddressed group
traffic and ordinary conversations with colleagues enter only the separately
configured DWS route. Attachment bodies are not searched or downloaded. A quoted DingTalk card ID
is not a saved Host history reference. Cards without callback text cannot be
reconstructed from Host history unless their original content was admitted;
provide the ticket link, number or text in that case.

## Core decisions

History tools are mounted automatically for DingTalk tasks with tools enabled
and a verified default or project scope. An uncertain project match still keeps
default-workspace history and normal memory processing. Historical unresolved
turns stay readable in their saved default scope without reprocessing memory.
The native Agent decides when to retrieve;
the Host does not preload general transcripts. For short follow-ups, the Agent
reviews available conversation context and searches eligible history before
asking the user to repeat the goal. Recovery from a failed native session also
supplies bounded context from its directly linked prior task, as described in
the [conversation details](channel-conversations-detail.md#recovery-context).
Other fresh sessions receive up to three same-scope prior user requests and
their supplied quote references, with separate attribution, without preloading
their replies or execution results.
Search stays inside the current
authenticated conversation, Agent, audience and task project grants.
A project switch does not grant every earlier project. Group history cannot
read owner-private messages. `/new` resets native continuation while retaining
eligible history.

Unknown attribution or unavailable evidence is stated explicitly. Search is
lexical and bounded; no result establishes only the searched Host window.
Retrieving an earlier reported success does not create a new execution result
or publish memory.

DWS uses its own selected sources, checkpoints and bounded cache. It runs
independently of task execution and is not a general chat-reading task MCP.
Complete collected windows may produce up to three cited, actionable topic
candidates per extraction; ordinary chatter should produce none. Unchanged
evidence is not re-extracted.

Review topics locally with `discovery topics` and `discovery show`. Accepting a
topic requires its explicit registered project and exports bounded
owner-private evidence. It does not start a task, send a message or publish
memory. Automatic task handoff, notifications and conversation-derived learning
remain later work.

The [personal-assistant proposal](personal-assistant.md) defines the next staged
use of this evidence: evolving matters, owner-private recommendations and later
conversation learning. It preserves the existing topic-export behavior and does
not treat a quiet conversation as proof that work remains blocked.

## Enable and rollout

1. Install the updated Host for automatic DingTalk history retrieval.
2. Select a DWS profile, specific private/group conversations and one registered
   project for each source. Copy the [example](../config/data-discovery.example.json)
   to an owner-private file; validate it with `discovery check`.
3. Run one bounded `discovery collect` and inspect coverage and topic evidence.
   Enable continuous polling with `discovery serve`, or an optional
   `discovery_config` in the managed service.
4. Evaluate real selected-source coverage and group noise before expanding.

DWS collection remains disabled without configuration. Partial ranges remain
visible and do not advance a complete checkpoint or produce topics. Busy
ranges may require higher configured collection limits; the current collector
does not invent a continuation from a provider timestamp.

CLI/cross-conversation history, a full DingTalk archive, historical media
retrieval and automatic transcript-to-memory learning remain outside this
increment. The [architecture](architecture.md) and
[external-source guide](knowledge-sources.md) preserve these boundaries.
