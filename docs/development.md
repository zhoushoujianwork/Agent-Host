# Development and publication

Use one clone of `https://github.com/zhoushoujianwork/Agent-Host.git` for daily
development. `main` tracks `origin/main`; local changes and public updates use
the same Git history. There is no separately maintained public-source export.

Develop features in branches or temporary worktrees when useful. Before
committing, review the diff, run `make check`, and stage only the intended files.
Integrate reviewed commits into `main` and publish authorized updates with a
normal push. Preserve the existing public history; do not force-push a private
repository's unrelated history.

Keep real accounts, credentials, configuration, database state and execution
reports outside Git, normally under `~/.agent-host`. Commit configuration
examples with placeholders. The ignore rules are a safeguard, not a substitute
for reviewing files before publication. Private historical validation reports
are kept in a local archive rather than copied into the public repository.

Build with `make build`. A source push does not create a downloadable release.
When a versioned release is requested, package a clean commit with
`make package VERSION=YOUR_VERSION`, verify its checksum and build identity,
then publish its tag and GitHub release assets. See the
[beta distribution notes](beta-release-detail.md).
