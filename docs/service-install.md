# Project-local runtime

Install automatic startup with `make install`, or run enabled channels in the
foreground with `make run`. Editable
configuration lives in `.agent-host/config/`; channel data lives in
`.agent-host/channels/<name>/`, plugin data in `.agent-host/datasource/plugins/<name>/`,
learned knowledge in `.agent-host/memory/`, and rebuildable indexes/task views in
`.agent-host/cache/`. Source snapshots belong to their datasource plugin.
All remain Git-ignored.
See the [configuration guide](configuration-layout.md) and
[implementation details](configuration-layout-detail.md).

## Separate production from development

The local production service was migrated to `~/.local/share/agent-host/` on
2026-10-15. Its `bin/`, `config/`, `channels/`, `memory/`, `datasource/` and viewer
assets are independent copies. The launch service, installed CLI, memory skill
links and memory connection now use that directory. Rebuilding the development
checkout does not update the production binary.
After migration, `make install` recognizes the migration receipt and installs or
retains links to the independent bundle. It succeeds without deploying the new
development build or restarting production. Genuine custom-link conflicts still
remain visible.

The development checkout's former production configuration is archived under
`.agent-host/retired-production/`; `.agent-host/production-migration.json` records
its location. The default development configuration is absent, so ordinary
`make run` cannot accidentally start the production channels. Prepare separate
test configuration and test channel identities before running development channels.
Automated tests continue using their isolated fixtures.

Restart or remove the independent service from this checkout with:

```sh
python3 -B -m tools.service.autostart install --runtime-dir "$HOME/.local/share/agent-host"
python3 -B -m tools.service.autostart uninstall --runtime-dir "$HOME/.local/share/agent-host"
```

These commands use the already copied production binary. Updating production
requires explicitly replacing its verified artifacts and restarting; `make build`
only builds the development checkout. The [migration details](service-install-detail.md#independent-runtime-relocation)
describe preservation, verification and recovery. The source-bound setup below
remains available for new installations.

### Temporary source-checkout observation

On 2026-10-19, local automatic startup was removed to observe the current source
build against the existing data. Start it from this checkout with:

```sh
make run CONFIG_DIR="$HOME/.local/share/agent-host/config" RUN_ARGS=--no-browser
```

The executable and viewer come from the checkout; configuration and live data
remain in `~/.local/share/agent-host/`. Explicitly pass the real configuration
path: configuration discovery rejects symlink paths. Plain `make run` still
expects separate checkout-local configuration.

Git-ignored `.agent-host/live` links to the complete data root. The checkout's
`channels`, `memory`, `cache`, `datasource`, `projects`, `users`, `cli-state` and
`logs` directories under `.agent-host/` also link to the corresponding live
directories. Previous checkout directories are retained under
`.agent-host/retired-production/manual-observation-<timestamp>/`. The private
`.agent-host/manual-source-run-current` pointer locates the switch receipt and
saved launch-service definition. These links expose the same live data; changes
through them affect the running service.

The installed CLI and external Skill links now point to this checkout so all
entry points use the same code revision. Operational databases now use schema
15; the schema-14 CLI state was backed up and upgraded through
the existing, identity-preserving memory migration command. Published memory
manifests were unchanged. The private switch receipt retains previous link
targets and the CLI database backup.

The production migration receipt remains as historical installation provenance.
`make install` still selects that independent bundle and is not the command for
restarting this temporary setup. Before restoring its automatic startup, stop
the foreground runtime and verify that the independent binary supports the
current database schema. Keep only one supervisor connected to these channel
identities.

## Configure and check

`make install` builds and links the CLI and external memory skill into detected
local agents. When `.agent-host/config/host.json` exists, it also installs and
starts a user service: macOS LaunchAgent or Linux user systemd. It checks the
configuration first and confirms that every enabled channel connects. Without
configuration, it installs the CLI and skills and explains how to finish setup.
Use `INSTALL_ARGS=--no-autostart` for CLI and skills only. Other platforms require
that option; automatic startup is not implemented there.
See the [memory skill setup](external-memory-skill-design.md#setup-and-first-release).
Continue below to configure and start channel services with `make run`.

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
make install
# Stop and remove automatic startup, keeping configuration, memory and state:
make uninstall-service
# Run manually in a terminal:
make run
make run RUN_ARGS=--no-browser
```

Automatic startup runs at user login and retries failed processes after 30
seconds. It keeps the checkout in place, uses the existing private configuration,
and opens no browser. On Linux, a working user systemd session is required;
continuing after logout or starting before login requires separately configured
user lingering. macOS starts after login, not before it.

After code or configuration changes, rerun `make install` to rebuild and restart
the background service. Use `make uninstall-service` before switching to manual
foreground operation. A custom runtime uses
`INSTALL_ARGS="--config-dir /absolute/ROOT/config"`; removal uses the matching
`CONFIG_DIR=/absolute/ROOT/config`.

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
replay is performed. With `make install`, the service manager restarts the
supervisor after failures. Supervisor output goes to
`.agent-host/logs/autostart.*.log` on macOS and
`journalctl --user -u agent-host.service` on Linux. Channel logs remain under
their existing runtime directories.

Ordinary CLI chat keeps `.agent-host/cli-state`. With `--config-dir`, the shared
workbench automatically adds it as a read-only `cli` source once that directory
holds a `state.db` — no separate viewer or `--home` setup is required. An ad hoc
`chat --home` outside the default path stays independent and does not appear in
the shared workbench. To inspect channel records directly, select the relevant
`--home`; migrated default memories retain the original scope:

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
