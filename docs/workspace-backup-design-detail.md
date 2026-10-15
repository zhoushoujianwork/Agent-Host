# Lightweight workspace backup: implementation details

Status: proposed. Product scope and defaults are defined in the
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

Introduce a versioned logical recovery format, separate from an operational
directory copy. Proposed CLI surfaces are `backup create`, `backup verify`,
`backup list` and `backup restore`; these commands do not exist yet.

The export contains:

- Configuration files and registered profiles, with an explicit inventory of
  referenced files outside `config/`. Never traverse arbitrary symlinks or copy
  an entire home directory. Record unavailable or excluded dependencies.
- Every published entry's current revision and complete status metadata. Preserve
  disabled, conflicted and expired entries conservatively; omit older revisions.
  Keep blacklist comparison text and metadata even though they cannot be recalled.
- Stable source/domain/Agent/audience/scope identities, backend selection and
  original generation, plus original revision identifiers and hashes.
- The transitive evidence needed by retained entries: source receipts, attributed
  source notes, referenced run/evidence records and referenced retained documents.
  Export through an explicit allowlist of state entities; do not copy unrelated
  runs, event streams or pending delivery queues. Export is incomplete if required
  evidence cannot be resolved. Report the exact missing reference.
- Host-retained local-document snapshots, deduplicated by content. Other external
  roots remain declared dependencies unless explicitly included. Do not claim that
  a source locator alone is a preserved copy of its original content.

Audit existing metadata relations before implementing this allowlist. Any retained
record with a revision dependency must either include that required revision or
use an explicit recovery representation preserving the original reference and
reason. Never leave a dangling reference or invent replacement evidence. Compact
recovery does not promise historical undo of actions predating the recovery point.

Use the same publication locks as writers, in deterministic source order, and a
consistent SQLite read snapshot for each state source. Finish or reject in-flight
file-operation reconciliation before capture. Freeze config changes during capture
or detect changed input hashes and retry a bounded number of times. Materialize a
private immutable staging set while locked, then release locks before compression
and encryption. Do not hold memory publication locks throughout archive I/O.

For sources not yet using the file backend, explicitly report unsupported compact
export and fail the full backup. Do not silently skip an enabled source. Support
for legacy state requires its own export mapping and acceptance evidence.

## Archive, encryption and retention

Store a versioned manifest with creation/check time, Host version, source
generations, logical identities, file sizes/hashes, exclusions and external
dependencies. Checksums detect damage; authenticated encryption protects archive
integrity and privacy. Use an established encryption library/format, never a new
cryptographic construction. Configure the key locally outside archives; document
an independently retained recovery key before activation. Never put keys in logs.

Write a temporary encrypted archive in the destination filesystem; flush and
atomically rename only after verification succeeds. Failure, cancellation, disk
full or unavailable destination leaves previous verified archives intact. Restrict
staging and destination permissions; clear temporary plaintext after completion.

A single supervisor-level job owns the schedule for all channel workers. Use a
cross-process lock to prevent overlap with manual backup. Store a durable last
success/check receipt; startup performs at most one overdue run rather than
replaying every missed day. Backup failures do not restart or replay business
tasks. Retry on the next scheduled check with bounded backoff and visible failure.

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

Path relocation must explicitly map state homes, configuration roots and external
dependencies while retaining the original logical scope identity. Do not equate
two scopes merely because they resolve to the same restored directory. Preserve
original locators as provenance. Require a restore preview for changed mappings.

Verification compares exported/imported current entries, statuses, scopes, Skills,
source hashes and blacklist behavior, and performs read-only recall checks with
private and shared audiences. It does not invoke a model or execute a Skill.
Only a successful isolated import makes a new archive eligible for retention as
a verified recovery point. Missing external dependencies remain visible; startup
checks must block affected capabilities rather than claim complete recovery.

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
