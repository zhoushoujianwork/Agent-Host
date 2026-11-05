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
