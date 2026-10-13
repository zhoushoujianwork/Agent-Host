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
reports outside Git, normally in the project's ignored `.agent-host/` tree.
`make run` loads enabled `.agent-host/channels/*.json` registrations through the
Go CLI, falling back to `.agent-host/runtime` only when that directory is absent;
ordinary CLI state uses
`.agent-host/cli-state`. Commit configuration
examples with placeholders. The ignore rules are a safeguard, not a substitute
for reviewing files before publication. Private historical validation reports
are kept in a local archive rather than copied into the public repository.

Distribution is source-only. Users clone this repository and run `make build`
locally, using their own native harness, accounts and credentials. Normal
publication updates GitHub source commits; it does not create GitHub Releases
or upload binary assets. See the [source setup guide](beta-release.md).
