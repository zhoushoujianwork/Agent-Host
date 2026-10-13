# Project-local runtime

Run enabled channels and built-in capabilities with `make run`. Editable
configuration lives in `.agent-host/config/`; channel data lives in
`.agent-host/channels/<name>/`, plugin data in `.agent-host/plugins/<name>/`,
and retained knowledge in `.agent-host/knowledge/`. All remain Git-ignored.
See the [configuration guide](configuration-layout.md) and
[implementation details](configuration-layout-detail.md).

## Configure and check

Use the tracked `config/*.example.json` templates, removing `.example` in the
private configuration directory. Protect directories with mode `700` and files
with mode `600`. Existing installations must migrate before using the new default.

| File | Purpose |
| --- | --- |
| `host.json` | Named execution profiles and provider environment-file references |
| `agents.json` | Optional roles, project/source selection and plugin subsets |
| `channels.dingtalk.json` | Channel enablement and independent `settings.bot`/`settings.dws` options |
| `channels.feishu.json` | Feishu enablement and text/meeting settings |
| `plugins.email.json` | Mailboxes and allowed channels; owner-private only |
| `plugins.knowledge.json` | Sources, allowed channels and audience grants |
| `credentials/` | Private provider environments and referenced credentials |

Component files require `version: 1` and explicit `enabled`. A missing or disabled
plugin is not mounted. DingTalk bot and DWS each require their own `enabled` when
present; an omitted/disabled branch does not initialize clients, inspect its
credentials or run background functionality. DWS state lives in
`channels/dingtalk/dws`, separate from the task workspace. Enabling a component
does not authorize outgoing messages or broaden native permissions.

Run the local-only preflight after changing configuration:

```sh
make build
./bin/agent-host service check --config-dir "$PWD/.agent-host/config"
```

It checks every enabled component without platform connections, native execution
or database creation. Unknown names, invalid configuration and missing credentials
fail explicitly. Runtime directories are derived from component names. A channel
can select a different execution profile; provider environments remain isolated.

## Start and observe

```sh
make run
make run RUN_ARGS=--no-browser
```

The Go supervisor starts one worker per enabled channel and one shared workbench
at `http://127.0.0.1:8765/`. It keeps source labels on records and memory. Terminal
output is also appended to each channel's `logs/service.ndjson`. Ctrl-C stops
workers and the workbench. Configuration changes take effect after restarting.
Use `CONFIG_DIR` to select another `ROOT/config`; use
`RUN_ARGS='--viewer-port 9000'` to select another workbench port.

The [memory workbench](../tools/run-viewer/README.md#memory-workspace) forwards
owner review actions to the corresponding running Host. Channel databases and
native sessions remain independent. Plugins are mounted only for eligible tasks;
Email shares one operation ledger across configured channels. No automatic task
replay, crash restart or login-time service is installed.

Ordinary CLI chat keeps `.agent-host/cli-state`. To inspect channel records, select
the relevant `--home`; migrated default memories retain the original scope:

```sh
./bin/agent-host feishu status --home "$PWD/.agent-host/channels/feishu/state"
```

Legacy explicit `service --root` and `--channels-dir` entry points remain for
unmigrated runtimes. The stopped-runtime migration and deployment evidence are
recorded in the [details](service-install-detail.md#unified-configuration-runtime).
