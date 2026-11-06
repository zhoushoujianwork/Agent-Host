# Branch maintenance

Agent-Host keeps `main` as the release source and `dev` for integration. Short-lived work is reviewed and checked before integration; CI and branch protection must not be bypassed.

## Local recurring maintenance

The local Offload task `agh-daily-branch-integration` (`t-f6475ba4`) runs daily at 09:00 Asia/Singapore, beginning 2026-11-06. It requires this Mac to be running and the user LaunchAgent scheduler to be active. Missed occurrences are skipped, not replayed. No cloud execution is implied.

Inspect with `offload schedule show agh-daily-branch-integration --json`, results with `offload schedule results agh-daily-branch-integration --latest --json`, and pause with `offload schedule pause agh-daily-branch-integration`.

The task processes only completed committed work, uses a clean worktree, runs headless checks, and waits for the exact PR head checks before merging. Dirty work, conflicts, architectural changes and required desktop acceptance remain pending with evidence. It never automatically commits user working files, launches apps, deploys services or grants tools. An active integration causes the scheduled task to skip.

## 2026-11-05 reconciliation

- Original inventory: 22 registered worktrees; five missing temporary directories were pruned only from Git registration.
- Most Offload, native-control and explicit-skill branches are already contained in main.
- Dream-memory design is integrated once; duplicate installation commits are not replayed.
- `docs/task-default-profile` is superseded by main default-profile guidance.
- `feat/clawflow-execution` preserves obsolete ACP work; do not restore it into Offload-only main.
- `feature/offload-builtin` preserves an obsolete implicit Skill-loading implementation; do not restore it into the explicit allowlist architecture.
- macOS work from `codex/macos-local` requires adaptation to current Offload APIs, rather than blindly merging its old runtime configuration.
- Local `.gitignore` additions and `tmp/` files are retained outside integration. In particular, ignoring `.github/` is not adopted.

Original commits must remain reachable until equivalent integration is verified. Remove temporary branches only when ancestry or reviewed replacement proves their work is preserved; never delete a dirty or occupied user worktree.

## 2026-11-06 unified HOT release cleanup

The user authorized production deployment and reconciliation of all worktrees
and branches. Verified archival and removal of inactive work can be prepared
during release; final release-worktree cleanup follows production verification. The five old `codex/macos-local` commits still require a future
adaptation to current Offload APIs; obsolete ACP and implicit Skill-loading work,
and the superseded default-profile documentation, must not be merged into the
production release merely to make the branch list empty.

Before removing those independent refs, a repository archive was created at:

```text
/Users/mikas/.local/share/agent-host/repository-archives/20261106-unified-hot-release/
```

`all-refs.bundle` was created with `git bundle create --all` and passed
`git bundle verify`. The archive also preserves `refs.txt`, `worktrees.txt`,
`root-gitignore.patch`, `root.gitignore`, and the three former root `tmp/` files
under `root-tmp/`, with SHA-256 verification. The root temporary files were moved
out of the worktree, the tracked `.gitignore` was restored, and appropriate local
tool ignores were moved to `.git/info/exclude`; `.github/` is deliberately not
ignored.

Inspect and verify the archive before any recovery:

```sh
archive=/Users/mikas/.local/share/agent-host/repository-archives/20261106-unified-hot-release/all-refs.bundle
git bundle verify "$archive"
git bundle list-heads "$archive"
```

Restore only a specifically needed branch under a recovery namespace, after
reviewing `list-heads`; do not fetch every archived ref by default:

```sh
git fetch "$archive" \
  refs/heads/codex/macos-local:refs/heads/recovery/codex-macos-local
```

The archive proves recoverability, not that its independent code was merged,
released or accepted. In particular, it does not establish macOS UI or desktop
acceptance. Seven inactive worktrees, thirteen local short-lived branches and
five remote short-lived branches were removed after matching their heads to the
verified archive; no open PR depended on those remote branches. Production
deployment and final release-worktree cleanup remain pending at this revision.

Final release cleanup requires all of the following:

1. This temporary `chore/unified-hot-release-cleanup` change is reviewed and
   merged through the normal PR path, and the final release commit is reachable
   from refreshed `main`.
2. The production rollout and its required post-start verification have
   completed from that final `main` source; source checks alone are insufficient.
3. The bundle still passes verification, its saved ref/worktree inventory is
   readable, and every worktree selected for removal is clean or its local
   material is accounted for by the verified archive.
4. Merged worktrees and branches are removed first. Archived independent work is
   then removed from active worktrees and local refs without restoring or
   merging it. Occupied or unexpectedly dirty paths stop cleanup.
5. The final local topology is one primary worktree on `main`, with only `main`
   and `dev` retained as long-lived local branches. The temporary cleanup branch
   is removed only after its PR is merged and the final topology is rechecked.
