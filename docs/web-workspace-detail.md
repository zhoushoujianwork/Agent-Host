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
