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

## Settings modal

The workspace opens the existing settings iframe in a native modal dialog.
The underlying route, selected record and Chat remain mounted and inert while
it is open. The frame is retained on dismissal, so unsaved edits survive
close/reopen within the same page; page reload retains the existing unload guard.
The close button, backdrop, Escape (including inside the iframe) and Cmd+W
return to the underlying route. Cmd+W dismisses settings before closing Chat.
Header/navigation and the save bar remain visible while the form content scrolls
without a visible scrollbar. Direct `#settings` links open over the memory page;
the standalone settings document remains available.

## Web Agent editing and capability selection

The settings API supplies selectable installed user-skill directory names and
knowledge `plugin:source` identifiers from the actual Web launch configuration.
It does not expose source credentials or infer a second native home. Creation
adds a stable generated ID to that launcher's existing editable Agent catalog;
the existing revision check, private backup and checked-save path is reused.
External catalogs outside the settings root remain read-only. Channel catalogs
retain their existing editor and restart lifecycle.

`native_skills: null` inherits the explicit base native profile's user skills;
a list selects only those installed directories, and `[]` mounts none. Selected
skills use an immutable selection-specific Agent/audience profile with symlinks
to installed skills. The profile identity changes when the selection changes.
Built-in native commands, native plugin skills, project-local skills and Host-selected procedure memory
are independent of this user-skill selector. Unknown skills and traversal names
fail before mounting. Native skill selection works for print and ACP because it
is prepared before either adapter runs.

`knowledge_sources: null` retains default enabled-source sharing; explicit lists
(including `[]`) narrow the Agent's actual knowledge MCP mounts before provider
credentials are opened. User grants, where active, still intersect the selection.
The normal configuration fingerprint causes a new native execution session when
capabilities or workspace change. Web Chat history remains visible across that
renewal. Saving does not execute a task or restart a channel service.

## Simple Agent choices

The primary choice is the actual Web default Agent when available, followed by
explicit non-default project Agents. Entries are deduplicated by catalog and
Agent ID. Web/CLI aliases of the same configuration appear once; channel default
profiles stay in their existing configuration but do not become separate choices
in the simple settings view. Selection still opens the chosen destination's own
profile and edits its original catalog. This presentation does not merge channel
identities, native sessions, knowledge policies or group bindings. Additional Web
Agent creation remains an optional disclosure. Demo Agents are not required.
Chat hides its Agent selector when its own local catalog has only one Agent;
independent channel project Agents are not silently imported into that catalog.
