# Settings console implementation

See the [user-facing design](settings-console.md) for scope and usage.

The loopback workbench discovers an explicit Host root from `--backup-root`.
It enumerates recognized config files, bounded private instruction/credential
files beneath that configuration directory, and an existing owner-local CLI
handoff connection. Requests use enumerated opaque IDs, never arbitrary paths.

GET `/api/settings` returns categories, file revisions and redacted values.
POST `/api/settings` requires the existing same-origin mutation token and a
bounded `{file, revision, value}` body. The server restores unchanged secret
markers, verifies the expected content digest and serializes writes with an
advisory configuration lock. Private backups and atomic replacement preserve
the previous content. Host `service check --config-dir` validates service files;
local structural checks also cover dedicated CLI Agent and handoff files.

Saved configuration is distinct from applied runtime state. No reload or restart
is implied by a successful save. Provider environment files keep their existing
private ownership and permissions; browser responses never contain their secret
values. Standalone viewers without an explicit Host root do not enable editing.

Tests cover secret retention/replacement, stale revisions, invalid configuration
rollback, path/symlink boundaries, HTTP origin/token checks and the grouped form
workflow. Browser verification uses an isolated fixture for writes; real local
configuration is used only for read-only presentation checks.

## Essential fields only

`settings-fields.js` explicitly describes the first-use controls for Agents,
native models, referenced provider credentials and existing channel identities.
`settings.js` renders those controls into the compact `settings-simple.css`
layout. Provider files appear with models; internal categories are not listed.
There is no recursive object editor, instruction editor, arbitrary field input,
or generic item deletion in the browser.

The edit draft remains a full clone of the redacted file. Changing a visible
leaf preserves unlisted properties and retained secret markers; optional missing
values are not injected merely by opening the page. Appearance and shortcut
help work without any Host configuration. Agent catalog choices use saved IDs
behind display names. Initial file creation and advanced configuration remain
outside this page.

Run `node --test tools/run-viewer/settings-fields_test.cjs` for the curated field
boundary and `node --test tools/run-viewer/shortcuts_test.cjs` for close handling,
iframe keyboard events and modifier guards. Browser acceptance saved an Agent
name in an isolated fixture and exercised Cmd+W in both Chat and an opened
conversation detail without closing the containing workbench tab.
