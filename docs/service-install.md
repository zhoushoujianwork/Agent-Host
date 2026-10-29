# Project-local runtime

新安装可用 `make install` 设置自动启动，或用 `make run` 前台运行渠道。
现有生产实例须先按下文确认实际运行方式。Editable
configuration lives in `.agent-host/config/`; channel data lives in
`.agent-host/channels/<name>/`, plugin data in `.agent-host/datasource/plugins/<name>/`,
learned knowledge in `.agent-host/memory/`, and rebuildable indexes/task views in
`.agent-host/cache/`. Source snapshots belong to their datasource plugin.
All remain Git-ignored.
See the [configuration guide](configuration-layout.md) and
[implementation details](configuration-layout-detail.md).

## 生产与开发：先确认实际运行方式

<a id="separate-production-from-development"></a>
<a id="source-checkout-observation"></a>

`.agent-host/production-migration.json` 记录安装迁移来源，不证明当前管理器、
进程或代码版本。2026-10-15 的独立目录迁移和 10 月 25 日的源码 LaunchAgent
恢复均为历史事件。10 月 27 日 #10 的部署记录显示 LaunchAgent 未运行，实际
supervisor 由既有 rollout 流程启动，viewer 使用不可变源码快照。
这些记录也不能代替下一次操作前的实时检查。

重启前执行[只读预检与模式选择](service-install-detail.md#production-preflight)，
确认唯一 supervisor、所属管理器、实际二进制、viewer 源路径及对应部署凭据。
无法确认、证据冲突、存在竞争管理器或孤儿订阅进程时停止操作。
不要根据迁移清单直接执行 `make install`，也不要以 `make run` 探测运行方式。

| 已确认的模式 | 授权维护时使用的既有机制 |
| --- | --- |
| rollout 快照部署 | 使用该部署凭据对应的既有 rollout 启停流程；保留二进制与 viewer 快照配对，不启用 LaunchAgent |
| 源码用户服务 | 在拥有服务定义的 checkout 使用 `tools.service.autostart install --config-dir` |
| 独立 bundle 用户服务 | 使用 `tools.service.autostart install --runtime-dir` 指向已验证 bundle |
| 前台运行且无服务管理器 | 使用明确配置目录的 `make run RUN_ARGS=--no-browser`，仅在授权替换该前台实例时执行 |

具体命令、排空、就绪与回退边界见[详细操作步骤](service-install-detail.md#production-restart)。
本指南修订不要求重启生产，也不授权切换管理器。

生产配置、渠道身份和状态必须沿用已确认的物理 runtime 目录。历史观察设置中，
checkout 的 `.agent-host` 是指向 `~/.local/share/agent-host/` 的整体软链接；
通过它写入就是修改生产。新工作树不一定具有该链接。独立开发测试使用独立
配置、渠道身份和隔离状态，不能只换目录却复用生产账号。

`make build` 只构建 checkout；如果运行进程使用 checkout 的二进制路径，
也不得把构建当作受控部署。viewer 来自快照时，修改 checkout viewer 不会更新它。
迁移感知的 `make install` 可能只保留独立 bundle 的 CLI/Skill 链接，不部署新代码
也不重启实际 rollout 实例。旧归档、CLI/Skill 链接与 schema 版本均应重新核对，
不能用它们推断当前服务归属。历史迁移见[迁移细节](service-install-detail.md#independent-runtime-relocation)。

### Runtime artifact cleanup

The 2026-10-19 cleanup removed superseded executables from historical validation
and channel directories, Finder metadata and the stale root `supervisor.pid`.
Historical acceptance material, channel backup snapshots, deployment receipts and
inactive legacy logs now live under `validation/archive/<timestamp>/`.
`validation/runtime-cleanup-<timestamp>.json` records deleted file hashes,
relocations and preservation checks. These historical executable copies are no
longer rollback binaries.

Keep `imports/`: published memory still references its source material. Keep the
root `bin/`, `tools/` and `skills/` installation assets while the migration-aware
installer references them. Channel `viewer/` directories are generated runtime
assets; the shared workbench currently uses the DingTalk copy. Configuration,
published memory, source snapshots, native sessions, current databases and
verified encrypted backups were retained.

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

以下安装与前台示例适用于新安装或已通过上述预检确认归属的运行环境，
不是 rollout 生产实例的通用重启命令。迁移清单存在时还须遵循其安装分支。

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

已确认由源码服务管理且不存在迁移清单的安装，可在代码或配置变更后用
`make install` 构建并重启。Use `make uninstall-service` before switching to manual
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
workbench mounts that fixed path as a read-only `cli` source, even before the
first chat. New runs appear on refresh without restarting the workbench. An ad hoc
`chat --home` outside the default path stays independent and does not appear in
the shared workbench. To inspect channel records directly, select the relevant
`--home`; migrated default memories retain the original scope:

```sh
./bin/agent-host feishu status --home "$PWD/.agent-host/channels/feishu/state"
```

For CLI-only use, start the observer without channel or model credentials:

```sh
make build
mkdir -p /absolute/ROOT/config
chmod 700 /absolute/ROOT/config
./bin/agent-host service workbench --config-dir /absolute/ROOT/config --viewer-source "$PWD/tools/run-viewer"
```

In another terminal, run Chat with your normal model credentials:

```sh
./bin/agent-host chat --home /absolute/ROOT/cli-state -m 'Reply with OK.'
```

Use the same physical
ROOT in both commands; resolve directory symlinks first. The observer needs
Python 3.10+, opens the browser by default, and stops with Ctrl-C.
`--no-browser` and `--viewer-port 9000` are supported. It creates no chat database
and does not start or restart a channel service. Stop another workbench or use
a different port if the default port is occupied.

The execution page lists mounted sources and distinguishes uninitialized state,
no runs, scope-filtered records and source read failures. Compare the displayed
state home with Chat's `--home` when a view is empty. Pre-run setup errors remain
terminal diagnostics, rather than successful execution records. See the
[implementation and validation notes](service-install-detail.md#cli-workbench-observation).

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

平台当前状态可通过 `agent-host status --root /absolute/runtime --no-http` 只读查询；
该命令不判断进程归属、不启动或重启服务。见[平台状态](platform-status.md)。
