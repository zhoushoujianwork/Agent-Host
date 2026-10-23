# Platform Skills Web: interaction contract

Companion to the [main page design](platform-skills-web-design.md) and
[managed Skills contract](managed-skills-design-detail.md).

## Platform ownership and navigation

Settings → Platform Skills exposes inspection and export. Agent-Host owns the
Skill lifecycle through the source's existing admission and versioning rules.
Manual import/review/publish buttons are not the main product workflow. Existing
CLI commands remain maintenance tools, with their current authorization checks.
Automatic cross-procedure consolidation is still planned; platform ownership
does not imply that arbitrary generated content is already admitted.

Keep the selected configured runtime visible. Web/CLI and channel destinations
remain distinct. Agent → Capabilities links to the shared catalog and shows
historical effective snapshots. It does not reinterpret native Skills selection
or silently enroll other Agents into the runtime `default` Agent's policy.

## Pinned export implemented on 2026-10-23

`skills export --root ABSOLUTE_PATH --id ID --revision N --digest SHA256`
writes a ZIP to stdout. The caller can redirect it to a chosen local file. The
command requires all revision selectors and uses the integrity-checked catalog
loader. It never rereads source directories, updates the catalog, changes
selection, installs Skills or runs helpers. Drafts and historical revisions can
be exported for inspection; their actual state remains explicit in the manifest.

The archive contains:

- `skill/SKILL.md` and all pinned `references/` files;
- adopted `agents/openai.yaml` and explicitly adopted `scripts/`, when present;
- `manifest.json` with format `agent-host-skill-export-v1`, catalog digest,
  revision metadata, original content digest, per-file byte counts/SHA-256,
  provenance, review, publication/withdrawal state, selected revision, enablement
  and audit entries for the exported revision.

The `skill/` directory is the portable instruction tree. The manifest is a
snapshot receipt, not authority to publish on another Host or evidence that a
business operation succeeded. External tool/plugin dependencies remain declared
requirements; export does not embed credentials from runtime configuration or
bundle plugin implementations. Original private content already in the adopted
files or provenance is preserved, and the download copy explicitly says so.
Preview redaction must not silently rewrite the exported procedure or its digest.

Archive paths are clean relative paths under `skill/` with the adoption allowlist.
Files retain exact bytes; file order is stable for reproducible exports of an
unchanged catalog. Files have private modes, with owner-execute only for adopted
executable helpers. Size budgets apply before output; stale digests, missing
revisions, corrupt catalogs and unsafe paths produce no archive.

`GET /api/platform-skills/export` requires exactly `destination`, `id`, `revision`
and `digest`, once each and nonempty. The destination must exist in server
configuration. The backend invokes the configured binary, validates the returned
archive manifest against the requested revision, and serves an attachment with
`application/zip` and `Cache-Control: no-store`. Invalid requests, unknown
runtimes, old binaries and failed exports remain errors without raw subprocess
diagnostics. The UI downloads only a successful ZIP response and ignores a
response if the user has left the selected version.

## Source coverage and next implementation

Authored catalog revisions have a complete pinned file tree and are exportable
now. Experience publication continues to belong to the memory store; plugin
instructions continue to belong to their provider. Neither historical event
metadata nor an unrelated native directory is an acceptable fallback export.

Next, read admitted experience from its authoritative publication with explicit
Agent/audience/scope and revision binding. Then derive plugin exports from the
same instruction/tool snapshot used for execution. Each source must retain its
own provenance and admission state. Exporting a rejected candidate must never
masquerade as an admitted procedure or grant a force-enable path.

Method consolidation should produce a new platform-owned revision after its
source-specific admission checks. Retain source evidence, prior revisions and
regional conditions. Conflicts and unsupported automatic merges need explicit
outcomes, not silent replacement or a new manual registration queue.

## Export validation

Tests cover complete US/China references after the original directory disappears,
exact private content and adopted-helper preservation, manifest checksums and
attribution, draft versus active state, deterministic archives, stale/corrupt
rejection, unsafe paths, unchanged catalog bytes, configured HTTP destinations,
attachment/no-cache headers, error handling and stale UI download suppression.
An isolated compiled-CLI → HTTP check verified the ZIP attachment, both regional
references, every file checksum and unchanged catalog bytes. Browser acceptance
confirmed the version export button and completed-download feedback using the
same synthetic fixture. Production was not updated or restarted.

Export validation receipt (2026-10-23): `GOFLAGS=-p=1 make check` passed,
including Go formatting/vet/tests and the Python/JavaScript suites. No tests
were skipped.

## Read-only increment delivered on 2026-10-23

`skills inspect --root ABSOLUTE_PATH` validates the catalog through the same Go
loader as execution, then returns revision metadata, audit history and file
inventories without instruction bodies. `--id`, `--revision`, `--digest` and
`--file` together read a complete pinned `SKILL.md` or `references/` file. Reads
do not follow source directories, publish drafts, change selection or execute
helpers. A changed source does not alter a pinned reference. Corruption, missing
files and stale digests fail explicitly.

`GET /api/platform-skills` selects a destination from configured Web/CLI handoff
arguments or channel execution profiles. Client paths and arbitrary roots are
not accepted. CLI defaults follow `--skills-root`, the file-memory marker, then
`HOME/managed-skills`; unified channels default to `ROOT/managed-skills`. It uses
the configured Host executable's inspection command. Missing configuration, an
older executable without inspection support and invalid catalogs remain visible
errors; no fallback registry or native execution is started.

The authored list is complete and filterable, with explicit source coverage.
Full files retain revision/digest binding; previews mask recognized credential
fields, assignments, bearer tokens and private-key blocks. This is pattern-based
redaction, not proof that arbitrary prose contains no sensitive information.
Only explicitly adopted instruction/reference content is previewed; script and
credential paths, tool inputs and raw private messages are not read for this view.

Agent profiles reuse destination/account/Agent evidence boundaries. Within the
latest 20 associated runs they show the newest `skills.catalog.provided` snapshot,
including authored, reviewed-experience and plugin descriptors and unready reasons.
Up to 200 load/reference observations from that same snapshot are shown with an
explicit truncation indicator and a link to the execution record. Missing history
is unknown; an empty newer tool-free snapshot does not revive an older catalog.
The automatic policy is shown only for runtime Agent ID `default`, matching the
execution code; a different routing default does not inherit it.

Validation covers immutable complete regional reference reads, stale/corrupted
catalog rejection, configured destination separation, no-Host/error states,
credential masking, read-only HTTP behavior, historical snapshot identity,
empty tool-free snapshots, escaped UI content and late-response isolation.
`make check` includes the new CLI, Python and JavaScript checks. Local browser
acceptance uses synthetic SFTP fixtures and a disposable viewer; it does not
constitute production adoption, native loading or business verification.

The bounded read-only code lookup used offload session
`ebb81822-8b95-4fe7-8533-076f49f40350`: status `ok`, exit code 0, profile `cc`,
model `claude-sonnet-5[1m]`, 24 tool calls, measured elapsed time 84.576 seconds,
estimated worker cost USD 0.522489. References were checked locally; the worker
made no edits. Cost is an estimate, not verified billing or total parent cost.

Validation receipt: `GOFLAGS=-p=1 make check` passed on 2026-10-23, including
Go formatting/vet/tests and all Python/JavaScript suites. An earlier full run
hit a timeout in the existing feedback test while discovering an unrelated local
checkout; that test passed alone and in the complete serial-package rerun. No
tests were skipped. Browser checks confirmed catalog navigation, full US/China
reference selection and the Agent snapshot-to-catalog link using synthetic data.
Production was not restarted or adopted into the catalog.
