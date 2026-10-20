# Web workspace implementation

[User guide and scope](web-workspace.md).

The Python workbench serves `index.html`, `workspace.js` and `workspace.css` as
the new entry. The former root execution inspector remains at `executions.html`.
Legacy `/?key=...` links route to that inspector inside the workspace.

The canvas consumes `/api/records`. It groups by source ID/kind, Agent, audience
and Host session ID, falling back to native session ID and then run ID. Each
card links to its most recent loaded execution. Source and partition identities
keep unrelated sessions separate. The existing API's record limits still apply.
The canvas refreshes on entry and on explicit refresh; it does not submit tasks.

Layout coordinates and draft text use `agent-host.workspace.v1` in same-origin
localStorage. Corrupt values and unavailable storage do not block the workspace;
save failures are visible. This is browser-local convenience state, not Host
memory or an execution receipt. It is not synchronized across devices or ports.
Cards use pointer capture for dragging and arrow keys for keyboard movement.

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
composes memory, journal, settings and Chat layouts over their existing content
renderers. Each standalone page loads these same assets; embedded panels hide
duplicate navigation while retaining filters and editing state. The memory
review queue follows the graph and library in both document and visual order.

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
