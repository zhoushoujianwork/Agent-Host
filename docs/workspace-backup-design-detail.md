# Lightweight workspace backup: implementation details

Status: implemented on 2026-10-15. Product scope and defaults are defined in the
[main design](workspace-backup-design.md).

## Existing constraints

The file backend's `memory/sources/<source-key>/current.json` is the publication
authority. Domain `current.json` files and retrieval indexes are derived.
`fileManifest.Entries` includes revision references, hashes and evidence-run IDs;
records contain source identity, state-home location and scoped Memory metadata.
See `internal/state/file_memory.go` and the
[file-memory details](file-memory-design-detail.md).

Consequently, copying only the latest revision directories beside the original
manifest produces an invalid backup. Copying live SQLite database files without
their transaction state also does not produce a reliable snapshot.

## Export contract

`internal/state/recovery.go` exports a versioned logical checkpoint, separate
from an operational directory copy. `internal/backup/` owns encrypted tar archives,
verification and scheduling; `internal/cli/backup.go` exposes `backup init`,
`create`, `verify`, `list`, `status` and `restore`.

The export contains:

- Configuration files and registered profiles, with an explicit inventory of
  referenced files outside `config/`. Never traverse arbitrary symlinks or copy
  an entire home directory. Record unavailable or excluded dependencies.
- Every published entry's current revision and complete status metadata. Preserve
  disabled, conflicted and expired entries conservatively; omit older revisions.
  Keep blacklist comparison text and metadata even though they cannot be recalled.
- Stable source/domain/Agent/audience/scope identities, backend selection and
  original generation and revision identifiers. The archive hashes logical
  checkpoint files; import regenerates hashes for relocated metadata.
- The transitive evidence needed by retained entries: source receipts, attributed
  source notes, referenced run/evidence records and referenced retained documents.
  Export through an explicit allowlist of state entities; do not copy unrelated
  runs, event streams or pending delivery queues. Export is incomplete if required
  evidence cannot be resolved. Report the exact missing reference.
- All Host-retained local-document snapshots. Other external roots remain declared
  dependencies; a source locator alone is not a preserved copy of original content.
  Referenced input attachments are copied with size/hash validation. Referenced
  run payloads retain their original context and audit decisions as evidence.

The allowlist includes current memory, referenced runs/events/sessions/candidates,
referenced channel receipts and external submissions, project registrations and
external connection registrations. No channel bindings, pending delivery queues
or collector cursors are imported. `recovery-original.json` preserves original
checkpoint payloads separately from operational copies with resumption disabled.
Historical revision references inside audit decisions remain historical provenance,
not available undo targets. Compact recovery does not provide historical undo.

Capture sources in deterministic state-home order. For each independent source,
hold its writer publication lock while reading its manifest and a consistent
SQLite transaction. Reject unfinished publication-receipt reconciliation.
Different source namespaces are not a cross-source transaction. Capture configuration
before and after and fail visibly if changed. Materialize the logical checkpoint
in memory and release locks before compression/encryption. No plaintext source
staging directory is written. File-memory schemas 14 and 15 are supported without
migrating the source database. Missing evidence fails the entire backup.

For sources not yet using the file backend, explicitly report unsupported compact
export and fail the full backup. Do not silently skip an enabled source. Support
for legacy state requires its own export mapping and acceptance evidence.

## Archive, encryption and retention

Store a versioned manifest with creation time, Host version, source
generations, logical identities, file sizes/hashes, exclusions and external
dependencies. Checksums detect damage; authenticated encryption protects archive
integrity and privacy. The age library encrypts the gzip-compressed standard tar
stream to an X25519 recovery identity. `backup init` generates a private key
outside the workspace and destination. The key is not included in archives or
logs. The owner must retain an independent key copy for disk-loss recovery.

The default destination is `ROOT/backups`, shared with historical migration
artifacts. Only this subtree is allowed for internal archive storage; configuration,
memory and cache directories are rejected. The export allowlist excludes all
backup storage. Retention only removes policy-owned archive files referenced by
its checkpoint index, leaving historical directories and unrelated files untouched.

Write a temporary encrypted archive in the destination filesystem; flush and
atomically rename only after verification succeeds. Failure, cancellation, disk
full or unavailable destination leaves previous verified archives intact. Restrict
staging and destination permissions; verification removes its private restore
directory after completion. Archives are bounded to 256 MiB expanded/compressed
content and individual members to 64 MiB; exceeding a limit fails explicitly.

A single supervisor-level job owns the schedule for all channel workers. Use a
cross-process lock to prevent overlap with manual backup. Store a durable last
success/check receipt; startup performs at most one overdue run rather than
replaying every missed day. Backup failures do not restart or replay business
tasks. Retry at the next hourly check with visible failure. The local workbench
reads `/api/backup`; it never exposes key contents or invokes backup mutations.

Retention selects seven most recent daily points and four weekly points by local
calendar date in the configured timezone. Multiple points can reference one
unchanged archive. Hash the canonical logical payload rather than timestamps to
detect changes. Delete an archive only when no retained point references it, and
only after a newer archive has passed isolated restore verification. Prune only
files owned by this policy, never migration backups or unrelated destination files.

## Restore contract

Restore offline into an empty target with no network-capable workers. Reject
unsafe archive paths, symlinks escaping the target, unsupported formats, digest
mismatches and invalid scopes before activation. Decrypt only into private storage.

Import logical records into fresh operational state, reconstruct compact publication
manifests referencing only included revisions, and create correct backend-selection
receipts. Preserve original IDs, revision numbers and source provenance. Record a
recovery receipt explaining history compaction; do not manufacture prior generations
or mark old operations as pending. Rebuild derived views and indexes through normal
backend validation. No execution, finalization, inbound collection or delivery queue
is resumed from the backup.

Import remaps state homes and JSON configuration paths inside the original Host
root. Referenced input files are placed under their restored state's recovery-files
directory; original paths remain in the recovery receipt. Logical scopes and
external paths are unchanged. `backup verify` prints the external dependency
inventory before an owner chooses activation. Arbitrary external path mapping and
external credential-session restoration are not implemented. Restored automatic
backup policy is disabled, and the restore command never starts channel workers.

Verification compares exported/imported current entries, statuses, scopes, Skills,
checkpoint hashes and complete current Memory payloads through partitioned reads.
Automated tests additionally check disabled-memory recall exclusion, audience
isolation, Skill evidence and external receipts. Verification invokes no model or Skill.
Only a successful isolated import makes a new archive eligible for retention as
a verified recovery point. External dependencies remain visible for owner review;
ordinary startup/provider checks still apply and no complete machine recovery is claimed.

## Acceptance checks

- Concurrent publication captures a consistent generation or fails visibly.
- Current facts and Skills survive; superseded/unpublished revisions are absent.
- Disabled/conflicted entries remain excluded and participate in admission checks.
- Referenced evidence is resolvable; omitted history is explicitly identified.
- Scopes and owner-private boundaries survive both same-path and relocated restore.
- No old task, message delivery or collector cursor causes an automatic replay.
- Wrong key, damaged archive, interrupted write and unavailable destination preserve
  the last verified recovery point.
- Unchanged data shares an archive; retention preserves all referenced archives.
- Restart triggers one overdue backup; multiple workers cannot start duplicates.
- Existing migration backups and unrelated worktree files are untouched.

## Validation

Focused Go tests cover encrypted round trips, compact revision/status recovery,
Skill/tool evidence and external receipt preservation, audience isolation,
corruption/wrong-key rejection, unsafe tar paths, existing-target refusal,
unchanged-archive reuse, retention, startup catch-up and cross-process exclusion.
The viewer HTTP test checks read-only health and recovery-key omission.
Live local acceptance on 2026-10-15 passed automatic startup catch-up and a separate
isolated restore drill. Three sources restored 104 current entries, including
nine Skills, two invalidated records and four conflicted records, from a
2,023,876-byte encrypted archive. Current payloads matched exactly; no pending
execution, memory processing, external intake or delivery was revived. The local
HTTP backup-health response matched the automatic recovery point. Private evidence
is recorded in `.agent-host/validation/backup-recovery-20261015.json`.

The 2026-10-15 storage audit moved automatic archives into `backups/` directly.
All 32 inspected historical SQLite databases passed integrity checks. Six readable
migration/config rollback directories were retained. Five disposable test fixture
directories and three superseded executable copies were removed, freeing
278,246,656 bytes while preserving eight raw reports/logs under `validation/`.
The retired external archive directory was removed after a new internal archive
passed isolated restore verification. Audit and cleanup receipts are retained in
`validation/backup-directory-audit-20261015.json` and
`validation/backup-directory-cleanup-20261015.json`.

The six retained historical directories were subsequently converted into ordinary
directory tar archives with the same age encryption key. Before removing each
directory, decryption and tar-member verification checked every file checksum,
size and permission mode against the original, including empty directories.
The 597 verified members occupied 412,365,659 bytes before compression and
156,258,819 bytes as encrypted archives. The conversion receipt is
`validation/legacy-backup-compression-20261015.json`.

| Archive prefix | Historical recovery purpose |
| --- | --- |
| `config-names-20261013T062858Z` | Configuration files before renaming |
| `datasource-config-20261013T105814Z` | Configuration and email data before the datasource migration |
| `file-memory-cutover-20261013` | SQLite memory databases and owner notes before file-memory cutover |
| `global-plugins-20261015T024349Z` | Agent and datasource settings before global plugin changes |
| `gtm-skilllab-group-20261014T030854Z` | Agent configuration before the group-agent change |
| `unified-config-20261013T103321Z` | Full legacy channel/runtime state before configuration consolidation |

Each filename ends in `.tar.gz.age`. These snapshots preserve historical layouts
for manual recovery and are not inputs to the current logical `backup restore`
command. Automatic retention does not prune them.
