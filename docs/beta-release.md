# Independent beta use

Each tester runs their own instance with their own accounts, credentials and
local memory. This beta uses Claude Code; automatic user-statement memory is
available, while execution experience still requires `memory accept`.
See [release details](beta-release-detail.md) and [feedback](feedback.md).

## Try the CLI first

Install Claude Code and verify its native authentication. Supply your own
`TYPESAFE_API_KEY` securely; choose a model available to your account.

For a source checkout, use Go 1.26+ and Make:

```sh
make build
./bin/agent-host --version
./bin/agent-host chat --model YOUR_MODEL --no-tools -m 'Calculate 17 times 19.'
```

A macOS binary package already contains `bin/agent-host`; skip `make build`
and use the same version/chat commands after extracting it. Verify the archive
against its accompanying SHA-256 before using it.

State defaults to `~/.agent-host`. Use `--home /absolute/private/test-state`
to keep an isolated trial. Repeated chat uses native continuation and saved
memory; original evidence remains local. Inspect it with the
[local viewer](../tools/run-viewer/README.md).

## Add your own DingTalk bot on macOS

Prepare your private configuration from the included `config/*.example.json`,
using your own Claude executable, model, Jev key, enterprise application and
owner identity. Follow the [installation guide](service-install.md); do not copy
another tester's configuration, credentials or database.

Install the prebuilt binary without rebuilding it:

```sh
python3 -B tools/service/install.py --binary bin/agent-host
```

Send a private request or @mention your bot in an enterprise group. Groups
share an audience and use system-prompt judgment; native tools retain their
configured access. The installation guide explains stopping and upgrading the
service. The managed service installer supports macOS; the CLI can be built
from source on macOS/Linux.

When something fails or memory seems wrong, submit a [reviewed feedback report](feedback.md).
The project uses [Apache License 2.0](../LICENSE), with retained third-party notices.
