# Run from source

Each tester runs their own instance with their own accounts, credentials and
local memory. This beta uses Claude Code; automatic user-statement memory is
available, while execution experience still requires `memory accept`.
The repository distributes source, without GitHub Releases or prebuilt packages.
See [distribution notes](beta-release-detail.md) and [feedback](feedback.md).

## Try the CLI first

Install Claude Code and verify its native authentication. Supply your own
`TYPESAFE_API_KEY` securely; choose a model available to your account.

Use Go 1.26+ and Make, and clone the repository:

```sh
git clone https://github.com/zhoushoujianwork/Agent-Host.git
cd Agent-Host
make build
./bin/agent-host --version
./bin/agent-host chat --model YOUR_MODEL --no-tools -m 'Calculate 17 times 19.'
```

Ordinary CLI state defaults to `.agent-host/cli-state` in the invocation directory. Use `--home /absolute/private/test-state`
to keep an isolated trial. Repeated chat uses native continuation and saved
memory; original evidence remains local. Inspect it with the
[local viewer](../tools/run-viewer/README.md).

## Add your own channel bot

Prepare your private configuration from the included `config/*.example.json`,
using your own Claude executable, model, Jev key, enterprise application and
owner identity. Follow the [project-local runtime guide](service-install.md); do not copy
another tester's configuration, credentials or database.

Build and run in the foreground from your checkout:

```sh
make run
```

Send a private request or @mention your bot in an enterprise group. Groups
share an audience and use system-prompt judgment; native tools retain their
configured access. The runtime guide explains project-local configuration, visible logs and
Ctrl-C shutdown. DingTalk and Feishu are supported; each needs its own channel
configuration. Building and local execution support macOS/Linux.

When something fails or memory seems wrong, submit a [reviewed feedback report](feedback.md).
The project uses [Apache License 2.0](../LICENSE), with retained third-party notices.
