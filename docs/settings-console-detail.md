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
