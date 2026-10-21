# Web workspace implementation

[User guide and scope](web-workspace.md).

The Python workbench serves `index.html`, `workspace.js` and `workspace.css` as
the new entry. The former root execution inspector remains at `executions.html`.
Legacy `/?key=...` links route to that inspector inside the workspace.

The conversation canvas is temporarily disabled. The root shell has no canvas
markup, drag handlers, grouping, layout controls or `/api/records` fetch. Execution
records remain accessible through their existing inspector. The default route is
`#memory`; dormant `#workbench` and unknown routes are normalized there.

Draft text uses `agent-host.workspace.v1` in same-origin localStorage. Saving or
clearing a draft preserves dormant layout coordinates and unrelated saved keys.
Corrupt values and unavailable storage do not block the interface;
save failures are visible. This is browser-local convenience state, not Host
memory or an execution receipt. It is not synchronized across devices or ports.
The composer has a bounded automatic height and independent scroll. Clear keeps
one in-memory undo value until the next edit or task starter; it is not restored
after reload. Empty messages cannot be sent. Enter and Cmd/Ctrl+Enter send; Shift+Enter inserts
a newline. Sending ignores IME composition. Escape inside the panel and its own close
button collapse Chat and return focus to the top-bar toggle. Explicit reopening
focuses the draft. Save failures remain visible after undo or manual retries.

Memory, execution and settings panels load lazily into retained same-origin
frames. `embedded.css` removes duplicate navigation without changing those
pages' data or editor logic. Same-origin framing is allowed by CSP; external
framing remains prohibited. Existing loopback Host, mutation-token and save
validation boundaries remain in force. Frame links to known workbench pages
route through the outer navigation. Settings remain loaded when hidden, keeping
unsaved changes and their existing unload guard.

Validation includes the server HTTP boundary suite, JavaScript syntax checks,
`make check` and isolated-fixture browser inspection. No production restart or
configuration migration is part of this change.

## Shared page design and appearance

`theme.css` owns semantic light/dark colors and shared controls. `pages.css`
composes memory, journal and Chat layouts over their existing content renderers;
the simplified settings page uses `settings-simple.css` and curated controls.
Each standalone page shares the theme; embedded panels hide
duplicate navigation while retaining filters and editing state. The memory
review queue follows the graph and library in both document and visual order.
The shared theme hides scrollbar tracks and thumbs across pages, Chat, editors
and nested panels while preserving native scrolling and keyboard navigation.

`theme.js` applies the validated `agent-host.appearance.v1` localStorage value
before content renders. System mode listens for OS appearance changes. Storage
events synchronize retained frames and other same-origin tabs. Storage failure
keeps the page usable and reports that the selection is temporary. Appearance
does not use the Host mutation API or require a service restart.

Run `node --test tools/run-viewer/theme_test.cjs` to verify restoration, system
changes, cross-document synchronization and blocked storage. Node is needed for
this developer check only; the workbench still runs with Python and a browser.
HTTP checks cover the added assets. Existing settings tests also cover a real
CLI handoff checker with sibling imports and rollback on unexpected validation
failures. The checker runs with `--check` in a separate Python process and never
executes the configured Host task.

`shortcuts.js` handles unmodified Meta+W when the right Chat panel or a selected
`#executions?key=...` route is open, including focus in retained frames. The panel
takes priority: closing it leaves the route intact and focuses the Chat toggle.
With the panel already closed, a selected detail returns to `#executions` and
focuses that navigation entry. `showChat` emits `agh:chat-visibility` to synchronize the close button.
The handler neither deletes data nor stops a run.
Browser-reserved shortcuts may not be delivered to JavaScript in every host.

The standalone Chat view and rail entry have been removed. Old `#chat` links use
`replaceState` to open the right panel on memory without losing drafts. The
panel keeps its open/closed state across module navigation; compact screens start
with it closed. `workspace_test.cjs` covers the disabled canvas, absence of shell
record fetches, legacy routes, panel state and non-destructive draft persistence.

`logo.svg` is an original vector AGH monogram, used by the rail, standalone
headers, settings and favicon. It has no font or external asset dependency.


## Web Chat execution

`chat_runtime.py` launches the existing Host CLI as a child process using the
private task-handoff configuration format. `--chat-config` explicitly selects a
destination. Otherwise the viewer selects `config/web-chat.json` inside
`--backup-root`, or the existing user CLI handoff only when its state, Agent
catalog, workspace and native profile all belong to that root. A development
viewer never implicitly launches another root's CLI task. The existing launcher
validates configuration with `--check` before each launch. Provider credentials
are loaded server-side and never returned to the browser.

`GET /api/chat` returns bounded conversation projections and the owner action
token. POST `/api/chat`, `/api/chat/stop` and `/api/chat/new` share the existing
loopback Host, same-Origin, token and bounded-JSON checks. Messages are at most
16 KiB. Only one turn executes at a time. Request IDs are idempotent within the
current conversation; a lost HTTP response must not cause automatic replay.

Host emits NDJSON; Chat projects Claude text and native tool/status events in
arrival order. It does not display the Host phase summary or memory-extractor
output. Tool calls show their actual names and parameters; Bash shows its original
command with full parameters available inline. Tool results remain separate
entries in arrival order, so parallel calls do not reorder returned content.
Partial tool-input JSON and native `tool_progress`, system, hook and result
notifications stay tied to their source events. Full native assistant envelopes
backfill missing text deltas once without duplicating streamed answers.
Initialization supplies the model footer, not its configuration envelope.
Routine initialization, requesting and success notices do not occupy transcript
rows. Other native notices remain inside the activity disclosure.
Provider stderr and non-answer content blocks are not projected.

The right Chat follows OpenFilm desktop's separation of activity and answer:
text before the last tool output belongs to the expandable process; later text
is the answer. Counts include tool calls once, never their result envelopes or
Host phases. Duration uses Host's `run.finished.elapsed_ms`, with measured Web
turn time while active or when no final timing arrives. Completed historical
turns start folded. A watched turn keeps its expansion until the next turn, and
manual expansion survives polling. Interrupted work retains partial text.

Markdown uses the MIT-licensed OpenFilm shared parser, vendored as plain
JavaScript in `markdown-parser.js` (license retained in the file), and a small
DOM renderer in `markdown.js`. It supports headings, bold/italic, inline code,
lists, quotes, fenced code, tables and web links, including unfinished streamed
blocks. Raw HTML stays text. Only HTTP(S) links are clickable and open with
`noopener noreferrer`; local file references remain text pending a workspace
file viewer. Tool parameters and results remain literal native output. Stable
Markdown blocks and native row nodes are reused across updates.

This presentation does not require ACP. OpenFilm desktop's `src/agents.mjs`
uses ACP adapters, while its `agent-activity-runs.ts` and Markdown components
implement the presentation independently. AGH now supports either its existing
Claude stream-json harness or the shared pinned Claude ACP runner behind Host. The chosen
[shared ACP direction](architecture.md#shared-acp-execution) places ACP
at that harness boundary for Web, CLI and all chat channels. The
[execution profile](acp-execution-detail.md#configuration) selects it for the Web
CLI destination. ACP tool patches update the existing call, and native plans are
shown inside activity. Interactive approval UI and file-diff cards remain pending.

The browser polls the current projection every 350 ms during execution, keeping
partial answers visible and preserving the user's expanded parameter controls and
scroll position. It reconnects without resubmitting. Execution completion requires
a successful Host exit and a completed receipt; execution is separate from task
acceptance and memory persistence.

Cancel sends SIGTERM to the owned Host child. Its signal context cancels the
native adapter, which freezes and terminates its owned descendant tree, including
tool shells that create separate process groups, and persists the
interrupted result. The Web state remains stopping until process exit; closing
the panel does not cancel. Viewer shutdown also cancels and waits for its child.
A failed or interrupted conversation requires an explicit New conversation;
changing the configured destination cannot silently resume a prior session.

Conversation UI state is process-local, shared by owner tabs, with at most 20
turns, 512 Ki characters per answer and 200 native output entries per turn.
Individual parameter/result displays are capped at 64 Ki characters with an
explicit truncation notice; original evidence stays in the Host journal. Restarting
the viewer clears its conversation view, not the Host's durable journal. Native
execution and provider failures are surfaced without a fallback provider. Startup
stderr is not sent to the browser; inspect CLI configuration and Host records
when execution fails. Independent CLI/Web conversations share the configured
state home and execute concurrently. Session leases prevent duplicate native
continuation, while maintenance and older binaries retain exclusive ownership.
See [CLI concurrency details](cli-concurrency-detail.md).

### Existing Session takeover

`POST /api/chat/session` accepts a catalog record key after the same-origin
owner mutation check. The catalog resolves the exact configured source and
enforces its Agent/audience/project visibility before loading bounded room history
and native activity. Archive reports cannot be continued. Drafts and lost-response
request IDs are separate for each selected context; sends carry `context_id`, so
a concurrent selection cannot redirect an old draft to another conversation.

CLI sessions use the validated CLI destination with `--takeover-session` and an
explicit request ID. DingTalk/Feishu sessions use `/session-chat` on the selected
state home's existing `0600` Unix control socket. The owning channel service
reconstructs the original Agent, audience and projects from durable source receipts;
the browser cannot supply executable paths, credentials or replacement identities.
Provider and execution failures never trigger another backend or implicit replay.

Host forks the exact completed native session into an immutable Web message,
identifies its speaker as `local-owner`, and joins its original room/epoch. The
original receipt remains unchanged. Later channel intake can use that completed
checkpoint through ordinary room assembly. Web actions do not generate IM replies
or reuse expiring DingTalk webhooks. A source must have a completed, finalized run
and compatible runtime/model; missing native state fails through the adapter.
Duplicate request IDs recover the recorded turn and cannot execute tools twice.
Channel Web forks retain the source receipt's memory-retrieval query when
materializing preselected Skills. The new Web message remains the execution
input; it cannot silently change the native Skill package of the selected
runtime. Changes to published memory or execution configuration still require
a compatible new channel context before takeover.

Session projection and selection are local to the viewer process. After a viewer
restart, select the Session again from durable execution records. Closing a panel
does not stop execution. Stop cancels the viewer's child; for channel continuation,
closing the socket cancels the owning service's request context. The service
accepts up to four Web continuation requests at once, and each selected Web
context sends one at a time. Historical channel executions are read-only in Chat.

`session_chat_test.py`, the Chat/browser tests, Host takeover tests and CLI service
control tests cover history, exact fork parents, repeated continuation, duplicate
requests, source isolation, stale sends and original channel delivery preservation.
These are synthetic checks, not claims of live platform acceptance.

Validation: `python3 -B tools/run-viewer/chat_runtime_test.py` exercises incremental
output, duplicate requests, real process cancellation, queued cancellation,
configuration changes and HTTP boundaries. `node --test tools/run-viewer/chat_test.cjs`
checks Markdown/native payload separation, activity counts/folding, transcript ordering, response-loss
retry identity and stop acknowledgement. Native projection tests cover partial
parameters, text/full-message deduplication, original tool errors and omission of
Host phases and initialization configuration.
`node --test tools/run-viewer/markdown_test.cjs` checks streamed Markdown blocks,
stable-node reuse, HTML handling and link safety.

An isolated real-provider check on 2026-10-20 observed incremental answer text,
a running Bash tool and refresh reconnection. Clicking Stop during `sleep 90`
terminated the Host, tool shell and sleep process; the durable receipt recorded
`execution_status=canceled`, `acceptance_status=unverified` and skipped memory.
Native adapter regressions also exercise descendants in a separate session and
verify that an unrelated sibling process remains alive.

## File-memory verification signatures

The viewer verifies published files through the configured Host before trusting
the derived index. Revision records, document bytes, publication pointers,
backend markers and the verifier retain full metadata signatures, including
ctime, to detect same-size edits with restored mtime.

The advisory `publish.lock` file participates through device, inode and mode
only. Its content and timestamps are coordination metadata, not a publication
version; maintenance of an unchanged lock must not produce a publication-change
error on every verification. Lock replacement, unsafe permissions and symlink
paths still invalidate or reject the snapshot. A shared publication lock remains
held throughout projection, and genuine source changes during Host verification
remain visible failures rather than falling back to legacy memory rows.
