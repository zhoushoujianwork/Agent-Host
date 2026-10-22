# Lightweight workspace backup

Status: implemented on 2026-10-15; encrypted compact export, isolated restore
verification, daily supervisor scheduling and retention are available.
See the [workspace layout](workspace-layout.md) and [implementation details](workspace-backup-design-detail.md).

## Goal

Restore the Host's current configuration, learned knowledge and operating methods
without carrying every old conversation, task log or memory revision. A restored
Host starts fresh conversations and keeps the original memory scopes and sources.
This is a recovery point for the Host's knowledge, not a full machine backup.

## Recommended policy

- Automatically back up once a day while the Host is running. On startup, run
  once if the last successful backup is more than 24 hours old. An offline Host
  cannot produce new backups; the latest successful recovery point stays visible.
- Keep seven daily recovery points and four weekly recovery points. If content
  has not changed, reuse the same immutable archive and record a successful check.
- Store automatic archives directly in `.agent-host/backups/`. The exporter
  excludes backup storage, so archives do not include older archives. An external
  destination remains configurable for protection against loss of this directory
  or disk.
- Show the last successful backup, destination, size and last verification result
  in the local workbench. Show failures explicitly and keep the previous good copy.

The default maximum loss is approximately one day of changes while the Host is
running and backups succeed. A manual backup is also available before important
changes. Recovery points contain private information and credentials, so archives
must be encrypted and the recovery key kept separately.

## Use

Archives are compressed tar files wrapped in age encryption: `.tar.gz.age`.
Initialize once, then produce the first verified recovery point:

```sh
./bin/agent-host backup init --root /absolute/.agent-host --timezone Asia/Singapore
./bin/agent-host backup create --root /absolute/.agent-host
./bin/agent-host backup status --root /absolute/.agent-host
```

Initialization prints the separate recovery-key path, never its contents. Copy
that key to independently protected storage. `make run` activates the daily job
in the channel supervisor; it checks the deadline hourly and once at startup.
The execution workbench shows backup health. The default local archive directory
is `.agent-host/backups/`, without an additional namespace directory. Use
`--destination /another-disk/agent-host-backups` at initialization to select an
external location instead. Local backups share the source disk.

## What is retained

| Keep | Leave out |
| --- | --- |
| Current Host/channel/Agent configuration, profiles and credential files under `config/` | Binaries, build output and repository source available through Git |
| Latest published revision of each memory or Skill, including disabled/conflicted state that controls future learning | Superseded revisions, abandoned drafts and unpublished files |
| Managed authored Skill catalog, including pinned files, revisions and lifecycle audit | Native home-directory links and rebuildable task Skill snapshots |
| Scope identities, source attribution and the evidence required by retained memory | Unrelated historical conversations, task logs and native sessions |
| Retained local source snapshots and files required by those references | Rebuildable indexes, task context copies and caches |

“Current” means the complete state governing behavior, including exclusions; it
does not mean only entries eligible for recall. A restore must preserve disabled
rules so they do not silently become learnable again.

External source directories are not recursively copied by default. For example,
`patsnap_docs` currently points outside this repository. Keep its configuration
and any retained evidence needed by memory; protect the complete external
directory with its own backup. External-directory inclusion is not implemented. Report
external dependencies in every backup and restore preview.

## Recovery experience

Select a recovery point and restore into a new, empty directory. The Host verifies
the archive, restores configuration and memory, rebuilds indexes, and checks that
the retained memory can be read. The inventory lists external paths to review;
native account sessions may require login again on the new machine.
Channels remain stopped until the owner reviews dependencies and starts the Host.
Old tasks and outgoing messages are never replayed as part of recovery.

```sh
./bin/agent-host backup list --root /absolute/.agent-host
./bin/agent-host backup verify --archive /backups/backup-HASH.tar.gz.age \
  --identity /safe/recovery.key
./bin/agent-host backup restore --archive /backups/backup-HASH.tar.gz.age \
  --identity /safe/recovery.key --target /new/location/.agent-host
```

`verify` performs an isolated restore and prints the source/file inventory and
external dependencies for review. `restore` requires a nonexistent target. It
remaps paths inside the old Host root, retains logical memory scopes, and leaves
automatic backups disabled in the restored configuration. Review external paths
and renew native account logins before starting channels. Arbitrary external-path
mapping and automatic account-session recovery are not provided.

The backup is considered usable only after a restore check in an isolated
directory. Verify every newly created archive without connecting channels, sending
messages or running Skills. Keep older verified recovery points if verification fails.

Full conversation recovery, external account sessions, full project checkout
backup and cloud synchronization are outside the first increment. Retention only
prunes archives tracked by the backup policy.

The six obsolete migration archives were explicitly removed on 2026-10-15 after
current recovery verification. Use the verified daily recovery archives for
restoration; audit receipts remain under `.agent-host/validation/`.
