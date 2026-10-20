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

Host emits NDJSON; the bridge projects `text.delta`, phase transitions and tool
activity. It excludes memory-extractor text, raw evidence and provider stderr.
The browser polls the current projection every 350 ms during execution, keeping
partial answers visible and preserving the user's expanded step controls and
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
turns, 512 Ki characters per answer and 200 projected steps per turn. Restarting
the viewer clears its conversation view, not the Host's durable journal. Native
execution and provider failures are surfaced without a fallback provider. Startup
stderr is not sent to the browser; inspect CLI configuration and Host records
when execution fails. Configured CLI state remains subject to its exclusive lock,
so another CLI process can cause an explicit launch failure.

Validation: `python3 -B tools/run-viewer/chat_runtime_test.py` exercises incremental
output, duplicate requests, real process cancellation, queued cancellation,
configuration changes and HTTP boundaries. `node --test tools/run-viewer/chat_test.cjs`
checks safe text rendering, response-loss retry identity and stop acknowledgement.

An isolated real-provider check on 2026-10-20 observed incremental answer text,
a running Bash tool and refresh reconnection. Clicking Stop during `sleep 90`
terminated the Host, tool shell and sleep process; the durable receipt recorded
`execution_status=canceled`, `acceptance_status=unverified` and skipped memory.
Native adapter regressions also exercise descendants in a separate session and
verify that an unrelated sibling process remains alive.
