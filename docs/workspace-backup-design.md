# Lightweight workspace backup

Status: proposed; automatic backup and compact restore are not implemented.
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
- Store archives outside `.agent-host/`, preferably on another disk or a backed-up
  directory. A copy on the same disk helps with accidental edits but cannot
  recover from loss of that disk. The destination must be explicitly configured.
- Show the last successful backup, destination, size and last verification result
  in the local workbench. Show failures explicitly and keep the previous good copy.

The default maximum loss is approximately one day of changes while the Host is
running and backups succeed. A manual backup is also available before important
changes. Recovery points contain private information and credentials, so archives
must be encrypted and the recovery key kept separately.

## What is retained

| Keep | Leave out |
| --- | --- |
| Current Host/channel/Agent configuration, profiles and configured credential files | Binaries, build output and repository source available through Git |
| Latest published revision of each memory or Skill, including disabled/conflicted state that controls future learning | Superseded revisions, abandoned drafts and unpublished files |
| Scope identities, source attribution and the evidence required by retained memory | Unrelated historical conversations, task logs and native sessions |
| Retained local source snapshots and files required by those references | Rebuildable indexes, task context copies and caches |

“Current” means the complete state governing behavior, including exclusions; it
does not mean only entries eligible for recall. A restore must preserve disabled
rules so they do not silently become learnable again.

External source directories are not recursively copied by default. For example,
`patsnap_docs` currently points outside this repository. Keep its configuration
and any retained evidence needed by memory; protect the complete external
directory with its own backup or explicitly include it in this policy. Report
external dependencies in every backup and restore preview.

## Recovery experience

Select a recovery point and restore into a new, empty directory. The Host verifies
the archive, restores configuration and memory, rebuilds indexes, and checks that
the retained memory can be read. The preview lists missing external paths,
credentials that need login again, and any path mappings needed on the new machine.
Channels remain stopped until those checks pass and the owner starts the Host.
Old tasks and outgoing messages are never replayed as part of recovery.

The backup is considered usable only after a restore check in an isolated
directory. Verify every newly created archive without connecting channels, sending
messages or running Skills. Keep older verified recovery points if verification fails.

## Delivery order

1. Implement compact export, archive verification and isolated restore together;
   prove that current knowledge, Skills and disabled rules survive recovery.
2. Add encryption, daily scheduling, retention and visible backup health.
3. Configure this workspace's destination and recovery key, produce the first
   verified backup, and record a successful recovery drill before enabling the policy.

Full conversation recovery, external account sessions, full project checkout
backup and cloud synchronization are outside the first increment. Existing
`.agent-host/backups/` migration artifacts remain separate and are not pruned by
this policy.
