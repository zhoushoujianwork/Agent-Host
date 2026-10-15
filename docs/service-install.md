# Project-local runtime

Run enabled channels and built-in capabilities with `make run`. Editable
configuration lives in `.agent-host/config/`; channel data lives in
`.agent-host/channels/<name>/`, plugin data in `.agent-host/datasource/plugins/<name>/`,
learned knowledge in `.agent-host/memory/`, and rebuildable indexes/task views in
`.agent-host/cache/`. Source snapshots belong to their datasource plugin.
All remain Git-ignored.
See the [configuration guide](configuration-layout.md) and
[implementation details](configuration-layout-detail.md).

## Configure and check

Use the tracked `config/*.example.json` templates, removing `.example` in the
private configuration directory. Protect directories with mode `700` and files
with mode `600`. Existing installations must migrate before using the new default.

| File | Purpose |
| --- | --- |
| `host.json` | Execution profiles, provider environments and explicit memory backend |
| `agents.json` | Roles, task projects, `instructions_dir` and optional owner `user_file` |
| `channels.dingtalk.json` | Channel enablement and independent `settings.bot`/`settings.dws` options |
| `channels.feishu.json` | Feishu enablement and text/meeting settings |
| `datasource.plugins.email.json` | Globally available mailboxes |
| `datasource.plugins.local-documents.json` | Globally available local Markdown sources |
| `datasource.plugins.dokki.json` | Globally available Dokki sources |
| `datasource.plugins.confluence.json` | Globally available Confluence sources |
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

Repeating `make run` checks the new configuration, gracefully stops the same
running instance, waits for its runtime locks to be released, and starts the
replacement. The previous terminal exits normally. Invalid configuration leaves
the existing instance running. Different runtime directories are independent.

The Go supervisor starts one worker per enabled channel and one shared workbench
at `http://127.0.0.1:8765/`. It keeps source labels on records and memory. Terminal
output is also appended to each channel's `logs/service.ndjson`. Ctrl-C stops
workers and the workbench. Configuration changes take effect by running `make run` again.
Use `CONFIG_DIR` to select another `ROOT/config`; use
`RUN_ARGS='--viewer-port 9000'` to select another workbench port.

The [memory workbench](../tools/run-viewer/README.md#memory-workspace) forwards
owner review actions to the corresponding running Host. Channel databases and
native sessions remain independent. Enabled datasource plugins are global;
Email shares one operation ledger across configured channels. No automatic task
replay, crash restart or login-time service is installed.

Ordinary CLI chat keeps `.agent-host/cli-state`. To inspect channel records, select
the relevant `--home`; migrated default memories retain the original scope:

```sh
./bin/agent-host feishu status --home "$PWD/.agent-host/channels/feishu/state"
```

Direct `service run` retains duplicate detection. Use `--restart` with managed
startup (`--config-dir`, `--channels-dir`, or `--root` plus `--viewer-source`)
to request the same restart behavior. Legacy entry points remain for unmigrated
runtimes. The stopped-runtime migration and deployment evidence are
recorded in the [details](service-install-detail.md#unified-configuration-runtime).

## File-memory migration

File memory requires an explicit stopped-runtime migration and verification for
each enabled channel; it is not created by startup. Back up the current state,
then use the [file-memory commands](file-memory-design-detail.md#迁移命令与回退)
with distinct sources such as `dingtalk` and `feishu`. Set
`"memory": {"backend": "files"}` in `host.json`, run service check and restart.
Missing receipts, changed source identity or damaged documents fail explicitly.

The published `memory/` documents and Skills own knowledge. `cache/memory/`
SQLite indexes can be rebuilt; channel state databases retain operational history.
The workbench verifies file publication before reading that index. Task views
and native Skill packages live under `cache/workspaces/`, without changing the
project cwd or writing generated content into `config/`.

`memory rollback --home PATH` preserves file-era updates before returning to
SQLite. After all relevant channels roll back, explicitly select `sqlite` and
restart. Merely changing config cannot bypass a migrated state's backend marker.

## Data-source configuration migration

Stop the current supervisor before migrating the earlier `plugins.*.json` setup:

```sh
python3 -B tools/service/migrate_datasource.py --root "$PWD/.agent-host"
./bin/agent-host service check --config-dir "$PWD/.agent-host/config"
make run
```

The migration backs up configuration and Email state, splits document sources by
provider and updates Agent references and tool allow rules. It leaves documents,
channel databases and memory scopes intact. Existing runtimes from before unified
configuration first need `migrate_config.py`, then this datasource migration.
See the [datasource guide](datasource-plugins-design.md).

Task execution uses a one-hour timeout (`1h`) in the supplied configuration.
Set `profiles.<name>.execution.timeout` in `config/host.json` to override it;
restart the service after changing this value. The deadline includes routing,
execution and memory finalization. Timed-out tasks are never replayed automatically.
