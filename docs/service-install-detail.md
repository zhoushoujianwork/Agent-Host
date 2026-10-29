# Runtime implementation and migration details

## 生产只读预检与证据分层

<a id="production-preflight"></a>

先读[主指南](service-install.md)。本节是操作顺序，不是部署脚本；所有路径和 PID
都必须来自本次检查，禁止复制历史 PID 发信号。

### 1. 确认物理目录和安装来源

本地检查 checkout 的 `.agent-host` 链接和 `production-migration.json`；不存在时
不创建它们。迁移清单仅定位历史源、目标及归档，目标还须与进程配置路径核对。
不要打印配置、凭据、完整环境或原始消息。记录只保留路径、PID、时间、提交、
摘要、状态和计数；完整 receipt、日志及进程参数只在私有终端检查，分享前脱敏。

```sh
ls -ld .agent-host
readlink .agent-host
# 使用已核实的目录；下列变量不是自动发现结果。
AGH_ROOT=/absolute/verified/runtime
AGH_BIN=/absolute/verified/bin/agent-host
"$AGH_BIN" status --root "$AGH_ROOT" --no-http
```

`status --no-http` 只读展示状态，不判断进程归属；缺少状态或历史 connected 事件
不能证明当前无人运行或已就绪。不要为查询而启动 workbench。
旧部署可能没有 `status` 子命令；先核对其帮助，缺失时使用现有只读执行记录，
或在隔离 checkout 构建支持该命令的查询工具，不能覆盖运行中的部署二进制。

### 2. 确认唯一 supervisor 和管理器

先查看不含参数或环境的进程清单，再针对候选进程及其父子进程在本地核对参数。
PID 文件、PPID=1、plist 文件存在或端口可用均不能单独证明管理器归属。

```sh
ps -axo uid,pid,ppid,comm
# bash/zsh：保留管道中管理器查询的失败退出码。
set -o pipefail
# macOS：仅输出状态和 PID，不转储 EnvironmentVariables。
launchctl print "gui/$(id -u)/com.agent-host.source" | awk '/^[[:space:]]*(state|pid) =/'
launchctl print "gui/$(id -u)/com.agent-host.production" | awk '/^[[:space:]]*(state|pid) =/'
# Linux 对应检查；不要在 macOS 执行。
systemctl --user show agent-host.service -p ActiveState -p SubState -p MainPID
```

管理器查询失败必须保留退出错误，不能把空输出当成 inactive。未加载服务可能报
not found；再检查本地服务定义是否会在登录时启动竞争实例。仅在私有终端查看候选
PID 的 `ps -p PID -o uid=,pid=,ppid=,command=`，核对 `service run`、
`--config-dir`、`--viewer-source`、工作目录和子 worker 的 runtime root。
用 `lsof -a -p PID -d txt,cwd` 辅助确认打开的可执行文件及工作目录，
用 `lsof -nP -iTCP:8765 -sTCP:LISTEN` 确认 viewer 的监听进程（自定义端口需替换）。
Linux 可核对 `/proc/PID/exe` 和 `/proc/PID/cwd` 的链接。
不要使用 `ps e`、输出完整环境或公开未经筛选的参数。

把管理器 MainPID/launchd PID 与 supervisor 对齐，并沿父子关系确认所有 channel
workers、viewer 和订阅子进程。若不是 OS 管理器启动，必须由 rollout 的私有
启动记录或已知前台会话补足归属；不能仅因 LaunchAgent inactive 就认定 rollout。
同一渠道身份只能有一个 supervisor。存在未知进程、孤儿订阅锁持有者、另一个
可自动拉起的管理器或无法核实的归属时停止，不删除锁、不强杀、不试启动。

### 3. 对齐部署快照与就绪证据

在已确认 runtime 的 `deployments/<deployment>/receipt.json` 中本地核对提交、
部署状态、二进制位置/摘要、viewer 源路径、旧进程退出与新进程证据。
字段以实际 receipt 为准，不假设迁移清单或目录排序指向活动部署。
用 `shasum -a 256 /absolute/verified/binary` 对照部署摘要；核对不可变
`source/tools/run-viewer` 与实际 `--viewer-source` 一致。viewer 的生成副本不等于
源快照。磁盘二进制若在启动后被覆盖，其当前摘要不能证明运行进程的版本；
无法从启动证据确认时报告未知，不声称已验证。

证据层次必须分开：安装清单证明来源，实时管理器/进程证明归属，receipt 和摘要
证明部署制品，启动后的连接事件与稳定 PID 证明启动就绪。旧 receipt 的
`deployed_verified` 不证明今日健康。当前 HTTP 200 也不证明渠道连接或消息交付。

## 授权重启、排空与回退

<a id="production-restart"></a>

1. 预检通过后，记录唯一 owner、物理 config/state 路径、当前制品摘要与 viewer
   快照、回退配对和 schema 兼容性。保留渠道账号、native profile/session、配置、
   memory 和状态；不重新初始化或从开发目录覆盖生产。按既有备份流程保存私有备份。
2. 安排无新请求的维护窗口，重复只读检查各渠道及 CLI 的 pending/running/claimed
   工作和交付、memory/experience closeout。可用 `status --no-http` 查看汇总，
   再用现有执行记录确认未完成项。`execution.ready` 或已发出回答不等于排空：
   等待 `run.finished` 及交付/closeout 终态。没有通用自动 drain 命令；持续有新工作
   就延后维护。SIGTERM 会取消原生执行，不是等待业务自然完成的 drain。
3. 只使用已确认 owner 的既有启停机制。rollout 必须沿用其记录的启动方式、工作目录、
   日志和二进制/viewer 配对；本仓库没有通用 `rollout` CLI，不能臆造命令或改用
   LaunchAgent。缺少 rollout 启停记录时停止并取得该记录。确认旧 supervisor、
   workers、viewer、订阅进程退出且锁已释放，再由同一机制启动一次。
   停止前用已验证二进制执行 `service check --config-dir "$AGH_ROOT/config"`；
   它只验证本地配置，不证明平台凭据有效，不创建数据库或发起原生执行。
4. 记录新启动时间；每个启用渠道必须出现该时间之后的 `channel.connected`，
   supervisor/worker PID 在观察窗口稳定，且 viewer `/`、`/settings.html` 返回
   HTTP 200。例如 `curl --fail --silent --output /dev/null http://127.0.0.1:8765/`，
   对 `/settings.html` 同样检查。不开浏览器，不发合成消息。旧日志、receipt 或
   单次 HTTP 成功不能替代这些检查；就绪不等于业务验收或持续可用性。
5. 失败时保留错误和新旧部署证据，不隐式重试任务或改管理器。回退也由同一 owner
   停止失败实例，再恢复已验证的旧二进制与旧 viewer 快照配对，沿用配置和状态。
   先确认旧版本支持当前 schema；不支持时停止，按既有备份恢复方案另行处理，
   不覆盖当前数据库或删除历史。回退后重新检查唯一 owner 和新鲜就绪证据。

以下仅展示通过预检且维护已授权的对应分支；不能连续执行或拿来探测模式：

```sh
# 源码用户服务：必须在拥有服务定义的 checkout，先验证待部署构建。
python3 -B -m tools.service.autostart install --config-dir "$AGH_ROOT/config"
# 独立 bundle 用户服务：已验证 bundle 包含 bin/config/tools/run-viewer。
python3 -B -m tools.service.autostart install --runtime-dir "$AGH_ROOT"
# 无管理器的前台实例：make run 会重建并替换同 runtime 实例。
make run CONFIG_DIR="$AGH_ROOT/config" RUN_ARGS=--no-browser
# rollout 的既有启动流程可调用此受支持入口；这不是完整 rollout 脚本。
# 仅在旧 owner 已停止时，使用已核实制品，保留重复实例拒绝行为。
"$AGH_BIN" service run --config-dir "$AGH_ROOT/config" \
  --viewer-source /absolute/deployments/verified/source/tools/run-viewer --no-browser
```

autostart 自带 `--no-browser`；`make run` 默认打开浏览器，必须显式选择上述选项。
`--restart` 能替换同 runtime 进程，但不能防止另一个外部管理器再次拉起它，
因此不替代 owner 检查。明确中断产生的 failed/unknown、未完成 closeout 与模糊
交付分别保留；只在支持的独立恢复路径中处理，不重放原生任务或重复发送答案。

### 证据来源与本次文档核验

[Issue #11](https://github.com/zhoushoujianwork/Agent-Host/issues/11) 记录 2026-10-27
预检：LaunchAgent 未运行；旧 supervisor 73024 使用 checkout 二进制和
`deployments/ea4970b0779c/source/tools/run-viewer`；对应 receipt 为
`ea4970b0779c`、`deployed_verified`。
[#10 后续部署记录](https://github.com/zhoushoujianwork/Agent-Host/issues/10#issuecomment-6435019503)
记录部署 `6220307cf76e-20261027T0555Z`、提交
`6220307cf76ee6f4b8ecfae59efff8450697e048`、二进制摘要
`6aa130fa4be14adaa704a187526a67ac5a31ed01ac861900f801d8cb10dad1a4`，
以及 supervisor 69628、workers 69909/69910、LaunchAgent 仍未运行、旧 viewer
快照与回退二进制保留。均是带时间的历史证据，不是当前 PID 建议。

本次文档核验使用上述公开部署摘要及仓库命令帮助/实现；工作树没有生产迁移清单，
本机预期目录下也没有部署 receipt，未直接读取私有原件或执行生产重启。
执行维护的 operator 仍须按上述预检核对实际 receipt 和进程，不得把摘要当实时检查。

## Reliability observation: 2026-10-25

本节为历史记录；当前操作须先执行上方只读预检，不能据此判定现有管理器。

The bounded review covered 2026-10-23 14:19 UTC through 2026-10-25 14:19 UTC.
DingTalk had two runs, both completed with memory unchanged; Feishu had no runs
in that window. Extending to the latest 20 recorded runs showed 19 completed
and one unknown for DingTalk, and 19 completed and one failed for Feishu.
The older DingTalk unknown outcome retained its ACP stream EOF evidence; it was
not replayed or relabeled. The last 20 finished CLI runs contained 16 completed
and four canceled outcomes. These execution counts do not establish business
acceptance or general reliability.

The recorded channel service PID was dead, and the source LaunchAgent was
absent and unregistered. The exact shutdown cause was not established: an older
supervisor log is not evidence of the most recent process exit. The temporary
foreground observation setup therefore had no service manager to restore it.

The full `make check` passed, including Go formatting, vet and tests plus the
Python and JavaScript suites. The checkout binary was rebuilt as
`0.2.0-observation-7bb6abc`. Rebuilding changed the runtime fingerprint, so the
first attempt to continue the read-only review session failed before creating
a run. Its exit status and evidence were retained; recovery uses a new session
in the same configured daily-task Agent and CLI workspace.

The source LaunchAgent installer passed preflight but exited 1 after its
60-second readiness deadline. Fresh stderr showed the Feishu subscriber exiting,
which stopped both channel workers. Inspection found an orphaned subscriber
wrapper (PID/process group 43792, parent PID 1) and its child 43793, started on
October 24, still holding the configured Feishu app's subscription lock. The
current user, configured profile, command, checkout working directory and open
app lock matched before SIGTERM was sent to that process group. Both processes
exited; no lock file was deleted, subscription lock bypassed or business task
manually replayed.

The already registered LaunchAgent then recovered automatically. Feishu emitted
fresh readiness at 14:30:20 UTC and DingTalk at 14:30:24 UTC. Supervisor PID 7733
remained stable through 14:31:08 UTC, and `/` and `/settings.html` on port 8765
returned HTTP 200. At that observation time, login startup and restart-on-failure used the existing
source service definition. A full reboot and deliberate forced-crash recovery
were not exercised. The independent installation bundle was not replaced.

Private evidence retains the initial failed installer separately from the
verified recovery: `validation/runtime-recovery-20261025-verified.json` under the
live data root contains the binary hash, timestamps, PIDs and checks. Historical
log errors alone did not justify changing project configuration; recovery needed
no account, provider or channel configuration changes. Existing uncommitted
viewer edits remain local and were not included in the source push; the
source-based service uses the checkout's current viewer assets.

## Independent runtime relocation

The [main guide](service-install.md#separate-production-from-development) defines
the production/development boundary. `tools/service/migrate_runtime.py` copies a
stopped unified runtime to an absent destination. It acquires every channel's
runtime and Host locks, and the development CLI lock, before taking a complete
snapshot. Symlinks are copied as links; external accounts and document directories
remain explicit external dependencies. Every regular copied file is hash-checked
before transformation, and copied SQLite databases pass integrity checks.

Configuration paths inside the old runtime are rebased; `memory_scope` remains a
stable logical identity. Every memory revision retains its complete original
payload bytes, including raw audit JSON. Only its `state_home` storage metadata
changes, with updated revision metadata hashes in authoritative current and
historical publication manifests. Domain mappings and file-memory receipts are
updated consistently. Source IDs, logical scopes, content, revisions, evidence,
publication generations and operation payload bytes remain unchanged. Derived
indexes are rebuilt through `memory verify`, and both channel reports must match
their source reports exactly except for the index location. Legacy empty/null
manifest lists are supported.

Development CLI state is copied as inactive history, with its physical marker
paths updated. Its old database schema is not upgraded or treated as a running
channel. That preserved snapshot requires the normal writable schema upgrade
before future direct use. No original conversation/task tables are discarded.

The bundle includes an independent executable, viewer, memory skill and service
control scripts. `tools.service.autostart --runtime-dir ABSOLUTE_ROOT` uses
`ROOT/bin/agent-host`, `ROOT/config` and `ROOT/tools/run-viewer`. Existing
source-bound default behavior is retained when this option is absent.
The source installer reads `.agent-host/production-migration.json` and verifies
the bundle's `migration.json` source/target provenance. For a verified migrated
checkout it links the existing deployed CLI and skill, and skips source-bound
service registration. Repeated `make install` therefore accepts the production
links without copying development artifacts or restarting the service. Invalid
receipts or unrelated CLI targets fail explicitly. An explicit `--config-dir`
with autostart is rejected in this mode; test channels use `make run CONFIG_DIR=...`.
Migration does not start channels or edit user connection profiles itself; a
failed partial destination is never reported as active and the source is kept.

The 2026-10-15 local cutover stopped the source LaunchAgent, copied 2,070 files,
verified all copied database files and preserved both channel memory digests.
DingTalk retained 104 memories, 125 revisions and 10 evidence links; Feishu's empty
memory publication remained unchanged. The independent LaunchAgent connected
both channels and the workbench returned HTTP 200. Seven owned CLI/skill symlinks
and the existing external-memory connection were switched to the new bundle.
The original `config` was moved into the ignored `retired-production` directory;
the development default cannot start the real channels. User/custom links were
not replaced. Production receipts are `migration.json` and `activation.json`;
the development receipt is `.agent-host/production-migration.json`.

Pre-activation failures restored the original service. The stopped source data
and archived configuration remain the pre-cutover recovery point. After new
production activity, restoring that old snapshot alone would omit new data;
move the latest production state back through a verified stopped migration when
rolling back. No automatic replay or destructive source cleanup is performed.

## Source-checkout automatic startup

The [main guide](service-install.md) defines installation and removal.
`tools/install.py` calls `tools/service/autostart.py` after successful CLI/skill
linking when runtime configuration exists. Explicit `--config-dir` requires a
valid configuration; an unconfigured default checkout reports setup pending.
`--no-autostart` bypasses OS registration.

The installer checks `agent-host service check --config-dir` before stopping the
previous service. It preserves definitions owned by another checkout and rejects
symlink targets. It writes owner-only definitions atomically. Only executable
discovery PATH and HOME are supplied to launchd; provider secrets remain in the
existing private configuration. The service invokes the checkout binary directly
with `service run --restart`, explicit configuration/viewer/Python paths and
`--no-browser`. It does not rebuild at login.

macOS uses `~/Library/LaunchAgents/com.agent-host.source.plist`, `RunAtLoad` and
`KeepAlive.SuccessfulExit=false`, following [Apple's LaunchAgent lifecycle](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).
Linux uses `$XDG_CONFIG_HOME/systemd/user/agent-host.service` (default
`~/.config/systemd/user`), `WantedBy=default.target` and
[`Restart=on-failure`](https://github.com/systemd/systemd/blob/main/man/systemd.service.xml).
Both wait 30 seconds between failure restarts and allow 45 seconds for shutdown.
They use existing runtime locks and verified same-runtime replacement; no task
replay is introduced. User lingering is not enabled by the installer.

Readiness requires fresh `channel.connected` events from every enabled channel
after registration begins, plus a running service for one second, within a
60-second deadline. Old log events cannot satisfy this check. Errors leave the
definition and private logs available for inspection and report failure.
Removal stops/unregisters only this checkout's service and retains runtime data,
CLI links and skill links. Linux supervisor diagnostics use the user journal;
macOS uses `ROOT/logs/autostart.stdout.log` and `autostart.stderr.log`.

Offline tests cover both OS definitions, escaping, repeat installation, preflight
failure preservation, foreign/symlink rejection, registration errors, fresh
per-channel readiness and removal. Linux registration remains unverified on a
real Linux host; macOS live verification is recorded separately below.

On 2026-10-15, `make check` passed and `make install` registered the macOS
LaunchAgent for this checkout. Both Feishu and DingTalk emitted fresh connection
events, launchd reported the supervisor running and the workbench returned HTTP
200. A fresh Feishu message entered processing after startup. Login after a full
reboot and forced-crash recovery were not exercised.

## Foreground restart through make run

The unified Make entry resolves `CONFIG_DIR` with quoted `cd` and `pwd -P`
before invoking Host. This supports a linked checkout `.agent-host` and paths
containing spaces while keeping the native checks on the resolved configuration
and credential files. If the directory cannot be resolved, Host is not invoked
and an existing runtime is not stopped. Explicit legacy `RUNTIME_ROOT` selection
retains its existing behavior. Browser opening remains enabled unless the caller
passes `RUN_ARGS=--no-browser`.

The Go runner copies the settings backend and page assets together with the
execution and memory workspaces. Workbench lifecycle tests install assets through
that same preparation function before checking readiness, browser opening and
the served settings routes, so a missing imported module is caught at startup.

`make run` supplies `service run --restart` for both `CONFIG_DIR` and explicit
`RUNTIME_ROOT`. Direct service starts remain fail-on-duplicate unless they also
request `--restart`. Internal worker mode cannot request a managed restart.

A checkout restart also recognizes the selected private unified runtime's
independent `ROOT/bin/agent-host`. The deployed executable must be owned by the
current user, executable, non-symlinked and not writable by group/others; the
configuration and runtime directories remain private. The process owner, exact
channel root and supervisor configuration path must still match. This permits
`make run` after a separately deployed binary has started the same runtime,
without treating arbitrary executables or unrelated runtime PIDs as managed.
On 2026-10-22, the real checkout `make run RUN_ARGS=--no-browser` replaced the
independent service successfully; Feishu and DingTalk reconnected at 15:31:59
and 15:32:05 Singapore time, and the workbench returned HTTP 200.

The supervisor checks every enabled configuration and reads the source viewer
assets before stopping an existing instance. A busy `run.lock` triggers inspection of
the private `service.pid`: the live process must have the current user, matching
Host executable and exact runtime `--root`. When its parent is the matching Host
supervisor, SIGTERM goes to that parent so all workers and the workbench stop
normally. Older standalone runners fall back to stopping the verified worker.
An unrelated, missing or unverifiable PID is never signaled.

The new process waits for the existing flock to become available, bounded by
30 seconds and caller cancellation. Lock release follows worker/workbench cleanup;
PID-file existence alone does not establish completion. Locks are retained through
startup, so concurrent invocations cannot create duplicate runtime owners. No
lock files are deleted and no forced SIGKILL or task replay is added.

Regression tests use isolated subprocesses to verify a successful replacement,
normal old-supervisor exit, invalid-preflight preservation, duplicate direct starts,
state-file preservation, unrelated-PID rejection and bounded lock waiting.
Production startup verification is recorded separately from these offline tests.

On 2026-10-15, the 15 focused startup/restart tests and
`make check GOFLAGS=-p=1` passed. An isolated source snapshot containing only
this task's changes was built into the project binary. Two successive
`make -o build run RUN_ARGS=--no-browser` invocations replaced the live supervisor
without manual stopping; `-o build` reused that already verified binary to avoid
including concurrent workspace changes. Each old runner exited with status 0,
each worker PID changed, both channels produced fresh connection events, and the
workbench returned HTTP 200. Private evidence is retained in
`.agent-host/acceptance/make-restart-20261015T030705Z/`.

The [main guide](service-install.md) defines the current project-local workflow.
The old system installation instructions below are retained as historical
reference for prior evidence; current `make install` uses the source-checkout
installer described above.

## Unified configuration runtime

The current make run default scans ROOT/config/channels.*.json and
datasource.plugins.*.json.
See [the current configuration details](configuration-layout-detail.md) for the
loader contract, bot/DWS optional branches, migration and live connection evidence.
Use `python3 -B tools/service/migrate_config.py --root ABSOLUTE_PATH` only for the
known stopped paired deployment; it refuses running locks, existing destinations
and discovery configurations needing an explicit mapping. Failures after backup
restore the old layout. The migration report retains database hashes and counts.

### File-memory runtime, 2026-10-13

Explicit `memory migrate` creates versioned documents/Skills and a private
state/file-memory.json receipt for each stopped state home. `memory verify`
checks authoritative publication and rebuilds stale or missing cache indexes.
`host.json` may explicitly select `memory.backend` as `files`; startup rejects
missing/invalid migration receipts. Selecting `sqlite` requires explicit rollback
when a receipt exists, preserving file-era changes before switching.
Commands, recovery boundaries and history comparison are in the
[file-memory details](file-memory-design-detail.md#迁移命令与回退).

The local DingTalk, Feishu and CLI state homes have been migrated with distinct
sources and shared memory/cache roots. Both channel workers reconnected, and the
workbench memory API returned 3 memories, 1 candidate and 2 channels. Old channel
memory/revision/evidence and original derived tables remain identical to the
stopped backup. The final isolated history receipt is
`.agent-host/validation/file-memory-history-20261013-final/report.json`. The
historical cutover archive was retired on 2026-10-15 after current recovery
verification; use the [workspace backup procedure](workspace-backup-design.md).

The default Agent now uses user-maintained AGENTS/SOUL/IDENTITY through
instructions_dir, preserving the prior CLAUDE.md role text. No owner USER was
automatically bound. The 14 owner_notes source documents moved into their
local-documents plugin snapshot with unchanged hashes and grants. Configuration
remains runtime-read-only; generated context and native packages live in cache.

The project-local sections below describe the prior layout and retained evidence.

## Previous project-local runtime

`make run` builds the checkout binary and invokes `agent-host service run`
directly. The Go CLI scans `.agent-host/channels/channel-*.json` for registrations with
`enabled: true` and a `runtime_root`, resolving relative paths against the
registration directory. `CHANNELS_DIR` selects another registration directory;
an explicit `RUNTIME_ROOT` bypasses scanning and selects one runtime.
`RUN_ARGS=--no-browser` suppresses browser launch. Without a registration
directory, startup retains `.agent-host/runtime` as the legacy default.

The CLI validates all enabled runtimes and acquires their exclusive `run.lock`
locks before connecting. It copies tracked viewer assets with private atomic
replacement, then starts one Go worker process per runtime. Child processes
isolate process-wide provider environment variables and native profiles.
The supervisor starts one workbench for all enabled channels at fixed loopback
port 8765 (`--viewer-port` overrides it). Workers use `--no-workbench` and do not
start separate Web processes. The workbench reads each channel's state separately
and qualifies displayed record/memory links with its registration name.
With `--config-dir`, `discoverCLIState` forwards the documented fixed
`ROOT/cli-state` path beside `ROOT/config` as `--channel-home cli=...`, even when
its database does not exist yet. CLI chat is optional and its database is
created lazily by the first invocation. Only this
fixed default path is auto-discovered; an ad hoc `chat --home` elsewhere is an
independent, unregistered runtime.
Workers relay stdout/stderr to the
terminal and their private append-only logs, and record `service.pid`.
The supervisor forwards cancellation with SIGTERM, waits for cleanup, removes
its worker PID files, and releases locks. Unexpected worker exit fails the
supervisor and stops peers; it never replays tasks or restarts silently.
The retained Python single-runtime runner is compatibility tooling only;
it is not invoked by `make run`. Python serves the workbench projection and
forwards explicit memory actions to the corresponding running Host.
The Host owns graceful channel, native execution and viewer shutdown. No launchd
registration, startup-at-login, automatic crash restart or shell daemon is added.
The legacy installer remains available for historical release tooling, but is
not part of the checkout's `make run` workflow.

### CLI workbench observation

`service workbench --config-dir ROOT/config --viewer-source ABSOLUTE_PATH`
starts an observer for `ROOT/cli-state` without reading channel registrations,
provider credentials or model configuration. The config directory must be a
private canonical absolute path; it can be empty. Viewer assets and logs live
under `ROOT/viewer` and `ROOT/logs`. The observer takes no chat writer lock and
does not initialize state. Both observer and Chat explicitly select the same
state home; ordinary Chat still defaults to the invocation directory's
`.agent-host/cli-state`. The command accepts browser/port options, but rejects
`--root`, `--channels-dir`, `--restart` and `--no-workbench`.

The shared observer registers only the fixed CLI home, never searches other
directories or merges native channel sessions. Initial discovery checks existing
state ownership, private permissions and symlinks. The Python projection repeats
these checks on CLI refresh/read, including a state home created after startup.

`GET /api/records` returns per-source diagnostics: `missing_database`, `no_runs`,
`scope_filtered`, `ready` or `read_failed`, with the exact state home and source
label. A damaged mounted source has an explicit failure while healthy sources
remain visible. The UI also identifies an unmounted CLI source in legacy
multi-channel mode and local search/source filters that hide loaded records.
Setup failures before run creation stay in the terminal; no synthetic run is
created. Existing run journals preserve actual model/provider events, tools,
failure/cancellation and independent memory/acceptance outcomes.

Automated coverage on 2026-10-20 starts this public service entry with an empty
config and no model credentials, then calls the official Chat entry using its
default home. Deterministic native/Jev fixtures verify first-run discovery,
successful continuation, native failure and cancellation through the running
HTTP viewer.
Python fixtures cover scope filtering, missing/empty state, damaged sources and
read-only isolation. These checks establish the application path, not a new live
native-model or platform acceptance result.

| Project-local path | Purpose |
| --- | --- |
| `bin/agent-host` | Binary built from the checkout |
| `.agent-host/channels/channel-*.json` | Private enabled-channel registrations |
| `.agent-host/runtime/config` | Current Feishu service/provider/channel configuration |
| `.agent-host/runtime/state` | Feishu task/history/memory database and input artifacts |
| `.agent-host/runtime/native` | Dedicated native profiles and sessions |
| `.agent-host/runtime/workspace` | Default task workspace |
| `.agent-host/runtime/logs` | Service and viewer logs |
| `.agent-host/runtime/viewer` | Private copy of viewer assets |
| `.agent-host/email` | Shared mailbox registry, private credentials and operation ledger |
| `.agent-host/dingtalk` | Independently configured DingTalk runtime |
| `.agent-host/legacy-home` | Preserved remaining former home-directory data and evidence |
| `.agent-host/cli-state` | Default state for ordinary local CLI invocations |

The existing root `.gitignore` rule excludes the complete `.agent-host/` tree.
Registrations require the `channel-` filename prefix. Other classified JSON files
(`data-`, `component-`, `credential-`) are ignored by the channel scanner;
unclassified JSON files fail explicitly. Runtime configuration prefers
`component-service.json`, `component-environment.json`, and `channel-<protocol>.json`.
Legacy names remain compatible when there is no canonical file; coexistence is
an error rather than implicit precedence. Explicit `agents_config`,
`knowledge_config`, `discovery_config`, and `email_config` paths use the names
listed in the [configuration naming guide](../config/README.md).
The CLI requires current-user ownership and private regular files, rejects
symlink runtime paths, and does not write credential values to commands. It uses
`python3` for the viewer unless `--viewer-python` is explicitly supplied.

Go regression tests cover enabled and disabled registrations, relative paths,
duplicate roots, malformed/private/symlink configuration, legacy fallback,
preflight failure before any connection, runtime-lock release, concurrent worker
startup, cancellation, peer failure and PID cleanup.

On 2026-10-13, the prior email deployment mismatch was traced to two separate
services: DingTalk production had email configured, while the Feishu lab still
used an older binary, no email registry and `no_tools: true`. An actual Host CLI
turn using the Feishu model/provider configuration completed the owner's original
mailbox query with email tools. No outgoing mail or test channel message was sent.

The later project-local migration stopped both old services, unloaded and disabled
`com.agent-host.production`, removed its LaunchAgent plist and moved the former
home `.agent-host` tree into the ignored project area. Feishu, DingTalk and email
subtrees became the paths listed above. Absolute live configuration references
were updated; immutable historical evidence retains original recorded paths.
Stopped Feishu and DingTalk database SHA-256 values matched before and after the
move. The remaining legacy files and former plist were preserved locally for
rollback; no state database was initialized in their place. Changed workspace
paths start new native sessions. Historical unregistered workspace scopes remain
recorded under their original paths; registered projects keep their stable scopes.

Forward migration and rollback must stop writers first. Rollback restores private
configuration references and the corresponding preserved runtime; do not run both
versions concurrently against one state directory. Restoring the archived plist
is an explicit operator action, not automatic fallback.

Validation covers runner preflight, private viewer preparation, retained state,
visible log relay, duplicate-run rejection, signal propagation and child cleanup.
`make check` passed after the change. Real `make run` startup on 2026-10-13
produced fresh meeting-entry and Feishu channel connections plus a loopback
workbench URL. A separate Host CLI turn with the migrated runtime configuration
completed the original mailbox query; successful paired results were observed
for `mcp__email__list_accounts` and `mcp__email__search_messages`. No outgoing mail
or new test channel message was sent. These receipts are stored privately under
`.agent-host/runtime/evidence/project-run-mail`; they do not establish a new
Feishu user-message round trip.

## Legacy system installation reference

## Layout and configuration

| Production path under `~/.agent-host/production` | Purpose |
| --- | --- |
| `bin/agent-host` | Installed executable, atomically replaced |
| `config/service.json` | Fixed native executable, model, Jev selection, deadline and tool policy |
| `config/environment.json` | Private provider environment values |
| `config/dingtalk.json`, `config/dingtalk.secret` | Owner/bot identity and application secret |
| `config/agents.json`, `config/CLAUDE.md` (optional) | Instance definitions and explicitly loaded service response instructions |
| `config/knowledge.json`, provider token files (optional) | Private remote/local source declarations and remote credentials |
| `config/discovery.json` (optional) | Fixed DWS profile, selected conversations, registered projects and bounded polling/extraction limits |
| `state` | Production SQLite database, native-run linkage and exclusive writer lock |
| `state/attachments` | Private downloaded task inputs, with source-linked content metadata |
| `workspace` | Native working directory and production memory scope |
| `native` | Enforced `CLAUDE_CONFIG_DIR`, isolated native sessions/settings |
| `logs/service.ndjson`, `logs/service.stderr.log` | Structured output and terminal errors |
| `installation.json` | Receipt of the last successful installation, not live health |

Only OS registration metadata lives outside this hidden root:
`~/Library/LaunchAgents/com.agent-host.production.plist`. The plist contains
paths and a fixed argument vector, with no shell command or credentials. Native
Claude itself remains a separately installed dependency at the configured
absolute path. No checkout runtime file is used by the installed service.

`service.json` requires `claude`, `model`, `jev_provider` (`typesafe` or
`custom`), `jev_model` and `timeout`; `custom` also requires `jev_endpoint`. Optional `no_tools` and `allowed_tools` retain existing native
permission behavior; they cannot be combined. Optional `knowledge_config` is
an absolute path to the private [knowledge configuration](knowledge-sources.md).
Its source project and optional audience lists select the MCP instances for each
turn. An Agent source subset can narrow these grants. Local Markdown uses explicit
root/path declarations without provider credentials. Its source root is separate
from the execution workspace and native directory policy. The
[local-source details](local-knowledge-detail.md) cover owner-only retained
snapshots and provenance. Configured native allow rules must authorize their use
in unattended tasks. Model/provider selection is explicit, without fallback.
Optional `discovery_config` is an absolute path to the owner-private
[DWS discovery configuration](conversation-history-detail.md#dws-configuration-and-operation).
Preflight checks its local shape and existing project registrations without
calling DWS or Claude. The system collector runs independently of channel tasks
and emits compact `discovery.cycle` coverage/failure events. Omission disables
it. Local topic acceptance exports owner-private citations; it creates no task
or memory. History MCP mounts automatically for eligible DingTalk tasks and is
excluded from tool-free execution.
`environment.json` accepts only string values for
`TYPESAFE_API_KEY`, `JEV_API_KEY`, `ANTHROPIC_BASE_URL`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`,
`CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK` and `CLAUDE_CODE_MAX_OUTPUT_TOKENS`.
Configure only the selected providers' entries. Native non-streaming recovery is
disabled by default. An explicitly selected gateway may opt in with
`CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK="0"`. Omitting
`CLAUDE_CODE_MAX_OUTPUT_TOKENS` retains Claude's native output budget; an optional
value must be a canonical positive integer. There is no Host-imposed 8,192-token
ceiling. Both settings are applied to the child environment and native invocation
settings. A configured output limit applies to each native model response,
including thinking tokens; it is not a total task budget. Recovery belongs to
Claude's existing model loop and retains the provider, model and observed tool
results. Gateway support is verified independently; opt-in does not imply that
every gateway accepts a large non-streaming request.

The production `cc` selection resolves the shell alias to its executable,
`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_MODEL`. The executable
and exact model go in `service.json`; the gateway and credential go in
`environment.json`. The daemon uses these explicit values with its dedicated
native profiles and configured Host tools. Shell echo commands, personal hooks
and conversation state are not imported. The current selection is
`http://gateway.example.invalid/api` and `claude-sonnet-5[1m]`, with native recovery
enabled and no explicit output cap. Jev retains its separately configured provider
and credential. These values are a deployment snapshot; later alias changes
require updating the private production configuration and restarting.

Optional `max_concurrent_conversations` bounds active conversation turns to
1–32; omission or zero uses four. The foreground equivalent is
`dingtalk serve --max-concurrent-conversations N`. Each authenticated conversation
and shared agent/audience partition remains FIFO through native work, synchronous
memory processing and reply delivery. Different audiences can run concurrently;
shared project files are not isolated. Configuration changes require restart.
The [conversation/input details](channel-conversations-detail.md) describe limits,
private attachment storage and the absence of an immediate `/status` or `/stop`.

`agent-host service check|run --root ABSOLUTE_PATH` is the installed entry point.
It requires private, current-user-owned configuration files and distinct fixed
state/workspace/native directories. It rejects symbolic links in managed paths,
unknown/duplicate configuration fields and oversized JSON. Native executable
symlinks are allowed. Provider credentials and backend-selection variables
inherited from development are cleared before applying the private environment;
`CLAUDE_CONFIG_DIR` is fixed to the managed native directory. The original
process environment is restored when the command returns.

The local checker opens no network connection, executes no Claude turn, and
creates no database. It confirms configuration shape and executable availability,
not provider authentication. The installer creates the directory layout; it
does not discover/copy user credentials automatically or import test data.

Project registration remains optional and local. Each new channel task uses a
reliably associated project set or the configured agent's default workspace;
an empty catalog skips the project provider entirely. The native assistant owns
business follow-up questions. No new Host project waiting record is created,
and old pending records are never automatically replayed. Default execution
does not mount additional projects or sources. Configuration, selected-directory
and provider failures remain explicit errors.

## Startup workbench

The installer copies the eight runtime viewer assets into `ROOT/viewer` with
owner-only permissions. The LaunchAgent passes the installer's Python executable
through `--viewer-python`, so startup does not rely on checkout files or a shell's
Python search path. `service check` does not launch a viewer or browser.

After local configuration validation, `service run` starts that viewer before
loading managed provider credentials, with `--home ROOT/state`, no archived
report import, and the configured fixed loopback port. It waits up to five seconds for the
viewer URL, prints execution and memory URLs on stderr, then uses macOS `open`
to request the default browser. `--no-browser` suppresses that request while
leaving observation available. Ordinary channel reconnects do not reopen tabs.

Service cancellation stops and reaps the viewer child. Viewer startup or browser
failures are reported separately without stopping channel execution; viewer
runtime errors remain in `ROOT/logs/viewer.log`. The service's structured event
journal remains separate. The current owner-local memory controls use a private
socket per channel Host, while Python retains read-only SQLite access. There is
no remote listener or task submission; source and request checks are documented
in the [workbench details](../tools/run-viewer/README-detail.md#owner-local-memory-controls).

Regression tests start the installed viewer assets against isolated schema-13
state, verify both pages and the memory API, check default/disabled/failed browser
requests, and prove that cancellation closes the listener. Installer checks
verify asset preservation and private permissions before launch.

## Response-instruction configuration

`CLAUDE_CONFIG_DIR` separates native settings and sessions, but Claude can still
discover `CLAUDE.md`, `CLAUDE.local.md` and rules through the execution directory's
ancestors or project imports. A separate profile alone does not prevent personal
instructions from entering a service turn.

Use the existing `instructions_file` field to give the service its own role.
Add an absolute `agents_config` reference to `config/service.json`, then point
the applicable agent in `config/agents.json` at a private instruction file:

```json
{
  "default_agent": "default",
  "agents": [
    {
      "id": "default",
      "name": "General assistant",
      "instructions_file": "/absolute/home/.agent-host/production/config/CLAUDE.md"
    }
  ]
}
```

Preserve existing agent definitions and group bindings when adding the field.
The Host reads this file at startup and appends its contents to trusted channel
instructions for every audience using that agent; its filename does not rely on
native `CLAUDE.md` discovery. Keep the existing business role and add a short
response preference, for example:

```markdown
Respond in the language used by the user unless they request another language.
Give English-learning tips or other language coaching only when the current
request asks for it.
```

The instruction file must be an absolute, current-user-owned regular file of
at most 32 KiB with owner-only permissions (`0600`). Its role extends channel
instructions; it does not grant tool access or replace native permission rules.

In production's `native/settings.json`, merge this setting while preserving
existing permissions and other native settings:

```json
{
  "claudeMdExcludes": ["/absolute/home/.claude/**"]
}
```

Replace the example with the real absolute path to the owner's personal Claude
directory. Claude matches these glob patterns against absolute instruction-file
paths, including rules and imported files. If the personal directory is linked,
exclude both its normal path and resolved target; review project-file links
separately on older Claude versions. This excludes personal
instruction loading without editing the owner's `~/.claude/CLAUDE.md`. The Host
copies shared native settings into each agent/audience profile, so the same
exclusion applies to owner-private and group turns.

Keep project instructions enabled. Managed policy instructions remain in force
and cannot be excluded this way. Personal text copied into a project file or
imported from a different directory needs its own review or exclusion. This
setting controls instruction loading, not ordinary file reads or OS isolation.
See Claude Code's official
[instruction exclusion documentation](https://code.claude.com/docs/en/memory#exclude-specific-claudemd-files).

Wait until the inbox has no `queued` or `claimed` turns before changing
configuration; `claimed` includes memory/experience processing and final reply
delivery after native execution completes. Then run `make install` to validate
configuration, restart the service and verify a fresh connection. Role content
and shared native settings participate in the existing runtime fingerprint:
changes replace incompatible native conversations on the next turn while
retaining Host history and durable memory. No new configuration schema or native
session migration is required.

### Instruction-isolation validation, 2026-10-12

Claude 2.1.223 passed clean-input native receipt probes: the baseline loaded the
personal ancestor instruction; the excluded profile skipped it while retaining
its own user and project instructions. A separate native turn used a temporary
profile with the deployed settings, the actual service role and production cwd.
It completed in Chinese without unsolicited English coaching. Initial probes
inherited the probe script through stdin and were superseded before behavioral
acceptance; the private evidence retains those attempts.

The service restarted at 07:18 UTC with a fresh Stream connection. The personal
instruction file, provider/model configuration and every preexisting run,
session, memory and channel-message row remained unchanged. Independently
arriving channel tasks added records after restart; an unchanged total row count
is not claimed. This task sent no DingTalk test message. The
structural validation report (private validation evidence)
separates native instruction receipts, deployed-policy behavior, startup and
historical-data preservation. Complete output and backups remain owner-private.

## History/discovery upgrade acceptance, 2026-10-11

On 2026-10-11, the [history/discovery upgrade](conversation-history-detail.md)
passed `make check` and isolated native acceptance, then installed with a fresh
Stream connection. A private schema 11 backup was taken first; read-only
comparison after schema 12 migration found unchanged rows in all 23 legacy
tables. DWS configuration remained absent. This is startup/migration acceptance,
not a new DingTalk user-turn or live DWS collection check. The structural
report (private validation evidence) retains
the verification without private message content.

## Directory access policy

The production agent adds the owner's existing `~/github` and `~/gitlab` trees
to Claude's native directory allowlist. This replaces the earlier root-directory
grant. It is a private native-profile setting, not a default granted by
installation. The owner-only `native/settings.json` uses the following shape;
replace the illustrative paths with the verified absolute paths on the host:

```json
{
  "permissions": {
    "additionalDirectories": ["/absolute/path/github", "/absolute/path/gitlab"]
  }
}
```

Resolve `~/github` and `~/gitlab` to their existing real directories before
writing the array. Replace the previous `"/"` entry, preserving unrelated
settings rather than replacing the file. These entries cover the two directory
trees in addition to the session cwd and any explicitly selected project mounts;
they do not create an operating-system sandbox.
The native setting extends working-directory access without changing Claude's
permission mode or pre-approving all Bash commands. Command parsing/approval,
edits and OS permissions remain independent checks. Keep the existing MCP allow
rules and source selection unchanged. The setting affects only Agent-Host's dedicated
`CLAUDE_CONFIG_DIR`, not the owner's personal Claude profile.

These native permission directories are not project participants: they do not
change the Host cwd, project registry, memory scopes or selected knowledge sources.
Unlike project `--add-dir` entries, settings-based directories grant native access
without requesting extra project instruction loading. The profile settings file
participates in the runtime fingerprint, so changing this policy starts a fresh
compatible native conversation on the next message while preserving history and
memory. Tool-free extraction remains tool-free. Check the inbox is idle before
changing the policy and use `make install` to restart and verify the connection.

The native behavior is documented in Claude Code's
[working-directory permissions](https://code.claude.com/docs/en/permissions#working-directories).

On 2026-10-10, a fresh native Claude 2.1.223 session using the production profile
successfully accessed both allowed roots from an isolated cwd. A separate empty
fixture outside the cwd and both roots was rejected by the native directory
permission check, as expected; no retry or alternate access was attempted.
`make check` passed and `make install` restored a fresh Stream connection with
the narrower settings. This is native and startup validation, not a new DingTalk
round trip. Real inputs and full evidence remain outside the repository.

### Historical root-directory validation

The following evidence used the earlier `additionalDirectories: ["/"]` policy,
which is superseded by the two-root allowlist above. It does not establish
acceptance of the narrower policy.

On 2026-10-10, the exact production Claude 2.1.223 binary passed a read-only
probe from an isolated cwd: an external fixture read and a directory inventory
both succeeded with the earlier root grant and no native permission denials.
`make check` passed, and `make install` restored a fresh Stream connection. The production
profile changed without changing the shared command allowlist. Private inputs,
fixture paths, complete outputs and platform records remain outside the repository.

The subsequent owner-DM inventory completed in a fresh `/new` conversation and
matched the independent count. Its final reply was verified from one completed
historical window after the bounded event listener expired; reply and completion
reaction were separately accepted by the Host, and no listener subscriptions or
consumers remained. Memory was unchanged and experience was empty. The run
retains one native shell-syntax approval refusal before successful read-only
commands; directory access does not imply arbitrary command approval.
An earlier repeated-input attempt stopped at an existing routing clarification
and never reached Claude. That failure is preserved separately, and only that
test request was cancelled. This validation does not claim that repeated-input
automatic routing was repaired by a permission setting.

## Installation and lifecycle

`make install` builds the binary and invokes `tools/service/install.py`. The
installer stages a private candidate binary and checks production configuration
before stopping an existing Agent-Host instance. It validates installation
destinations, waits for the old process to exit, atomically replaces the binary
and plist, enables the launchd job, and bootstraps it. A failed step is surfaced;
the installer does not silently restore an old service or switch providers.

The LaunchAgent has `RunAtLoad=true`, `KeepAlive={SuccessfulExit:false}`,
`ThrottleInterval=30`, `ExitTimeOut=30`, and an owner-only umask. Temporary Stream
disconnects recover inside the receiver with the official SDK's cancellable
backoff, retaining running native work and the durable queue. Unexpected process
failures remain visible and launchd starts a replacement, throttled to avoid a
tight restart loop. SIGTERM cancels native execution while allowing the existing
bounded outcome/delivery recording to finish. `launchctl bootout` unloads the job
for an intentional stop. Login starts one fresh process; already claimed work
and ambiguous replies are never replayed.

Schema 11 adds private attachment download capabilities without rewriting old
queued inputs or run evidence. Downloads occur after claim and before native
execution; missing/expired capabilities fail explicitly. Stored attachments and
database backups require the same owner-private protection as the rest of state.
Quoted callback summaries do not trigger historical-message or media retrieval.

Readiness requires a new `channel.connected` event after the previous writer
stopped, plus the same running PID through a short stability window. Historical
log events cannot establish readiness. Startup errors, malformed event output,
PID changes, termination and missing connection evidence fail installation.
This is a startup check, not continued uptime monitoring or end-to-end delivery
proof. The last successful installation receipt may remain after later failure;
use launchd state and current logs for live diagnosis.

Stop the writer before local memory review/retry. Operator stop command:
`launchctl bootout gui/$(id -u)/com.agent-host.production`. Use `make install`
for the next explicit start. This operator action does not affect development
state or delete production history.

## Validation

The historical records below predate the concurrent-conversation and attachment
paths. On 2026-10-11, `make check`, the relevant 886-test Go race suite and isolated
native image/file checks passed. `make install` verified a fresh connection;
separate live tests verified overlapping group/private execution and private
session continuation. Live image/file delivery and quoted-content callbacks
remain unverified, as explained in the
[conversation validation record](channel-conversations-detail.md#migration-recovery-and-validation).
Installation readiness alone does not validate those input paths.

Offline tests cover fixed production paths, native-profile/environment isolation,
private configuration, no-network checks, preservation of production state during
installation, no import of checkout fixtures, stale-log rejection, startup/stop
failures and secret-safe errors. `make check` includes installer tests in addition
to Go formatting, vet and tests. Actual deployment evidence is separate below.
The whole Go race suite passed 499 cases across 18 packages; the installer suite
passed 20 cases. A real native Claude/company-Jev smoke turn ran in temporary
state/workspace/native directories, streamed the expected response, completed
execution and memory processing, and left production state untouched.

## Deployment evidence

At 15:50 Singapore time on 2026-10-12, the final `make install` restoration
verified a fresh Stream connection and running PID 11630 with channel policy
version 6 and shared concise-response/source-link guidance. `make check` passed;
ordinary CLI/channel and tool-free invocation tests preserve structured
extraction isolation. A bounded native turn retained its supplied source URL and
key facts in 317 characters after an initial 605-character reply prompted tighter
default guidance. This probe used supplied public-source material, not a native
external lookup or a new DingTalk test message. Personal instructions and
production configuration remained unchanged; no historical records were deleted.

The first restart used an incomplete idle check and cancelled one pending
experience extraction after its native task completed. Independent
`run retry-experience` recovered that save to `empty`, without task replay or a
new chat message. The original failure remains in channel and execution evidence.
The corrected operator check includes `claimed` turns through finalization and
delivery. The response-guidance report (private validation evidence)
retains this incident and separates prompt transmission, native behavior,
production startup and independent recovery.

At 06:09 UTC on 2026-10-12, the owner's explicit `cc` selection replaced the
production Claude gateway and credential with the alias values and selected
`claude-sonnet-5[1m]`. `make install` started PID 72171 with binary SHA-256
`86b64376b5bdb46648a268c8cb9aa82311cfe0c81613972bd0c9df988d57164e` and a fresh
DingTalk connection. The temporary output override and Host 8192 ceiling were
removed. Jev, dedicated native profiles and configured tools retained their
separate settings. Read-only comparison found all 9,244 historical rows in 28
tables unchanged after the backed-up configuration switch.

`make check` and 294 affected race cases passed. Normal native execution and an
updated-adapter injected empty-stream recovery both completed through the exact
`cc` gateway/model. The latter used the native 64,000-token output budget, one
successful Read and a same-model non-streaming HTTP 200 response. Its observed
native context window was 1,000,000 tokens. The
cc switch report (private validation evidence)
separates this isolated acceptance and production startup from a new DingTalk
user-task round trip, which remains unverified.

At 05:49 UTC on 2026-10-12, `make install` deployed connection recovery and
bounded native stream recovery with PID 39170. The installed binary SHA-256 is
`4a3f84d85ccb5f99977e7f23c3f42c5920326b02aaf5d7b919e1f8d14fdd3af0`.
The idle runtime was backed up before its private environment opted in to native
fallback with an 8,192-token per-response bound. Startup established a fresh
Stream connection; launchd showed the running process and applied unsuccessful
exit restart policy with a 30-second throttle. Read-only comparison found all
9,151 historical rows in 28 tables unchanged.

`make check` and 493 affected race cases passed. A real updated-adapter probe
injected an empty second stream after one successful Read; same-provider/model
native recovery returned HTTP 200 and completed without another tool call.
Production connection, isolated native recovery and local WebSocket reconnect
tests are distinct evidence: no new DingTalk user-task round trip or natural
production reconnect is claimed. The
recovery report (private validation evidence)
preserves those boundaries and the failed comparison probes.

Observed on 2026-10-10 at 06:51 UTC:

- The authorized memgov `service stop` succeeded. Its LaunchAgent was unloaded
  and disabled, its old process exited, and active channel leases reached zero.
  Unified shutdown also stopped proactive monitoring and the associated
  data-source worker. Existing memgov state was preserved.
- `make install` succeeded and confirmed a new DingTalk Stream connection with
  production process PID 92002. Runtime data lives under the managed user root;
  the production inbox was initially empty, with no development-data import.
- Installed binary SHA-256:
  `40593c21b7f53bee6fb5a0c22003d14fdc6ace656143b01a2aebfa3239140adb`.

The first installer attempt incorrectly treated launchd's transitional state
with a PID as termination, even though the receiver connected. The installer
was corrected to distinguish startup from terminal states, with a regression
case including nested launchd state fields. The second `make install` completed
the stop/reinstall/fresh-connection path successfully. No automatic fallback or
task replay was introduced.

At 06:52 UTC the owner's first private message completed the live channel loop:
durable receipt, source-linked native execution, Jev memory classification and
one reply with DingTalk business acceptance (`errcode=0`). Execution completed;
memory was `unchanged`, experience was `empty`, and reply status was `accepted`.
The greeting was not promoted to long-term memory. The host run took 41.5 seconds:
39.4 seconds in native execution and 2.0 seconds in memory classification.
This is one measured turn, not a latency guarantee. Platform acceptance does not
prove the user read the reply or accepted a business outcome. Live follow-up,
`/new` and interrupted-turn cases remain separately unverified; offline tests
cover those behaviors.

At 07:07 UTC, `make install` deployed original-message progress reactions with
PID 18704 and binary SHA-256
`dc81bd0ecccdfba221c887b9c37b599047766efd2ed2a478f6bd100760c17f10`.
The private bot configuration gained the verified legacy `robot_code`; native
session bindings and production memory scope were preserved. Startup migrated
state from v4 to v5 and left the earlier message without retroactive markers.

The next owner message completed processing and final reply delivery. Its
已收到, 处理中 and 已完成 additions were all `accepted`; both predecessor
removals were also `accepted`, leaving only 已完成 known active. The measured
stage operations took 600 ms, 824 ms and 591 ms respectively. The owner replied
“可以了”, confirming the live display. Real-platform failure/timeout recovery
remains covered by offline regression rather than this successful live turn.

At 07:16 UTC, a further `make install` deployed the Markdown reply layout
with PID 32445 and binary SHA-256
`a8db0644d3153d340ba02248f80a3771ef3079fec462292c7ec6ce93f268de57`.
The production inbox was idle before restart and the new process established a
fresh Stream connection. `make check` and the 40-case channel test suite passed;
formatting regressions cover measured metadata, failure visibility, UTF-8
excerpts and code-fence closure. Markdown platform acceptance/client rendering
is awaiting a new owner message; the preceding live reaction success does not
prove the new reply layout.

At 07:17 UTC the next owner message completed with a platform-accepted Markdown
reply. The owner then requested removing the lowest-level status details and ID.
At 07:19 UTC, `make install` deployed that presentation-only adjustment with
PID 36886 and binary SHA-256
`db2eca0c5552777180e82a606c6a2556b69d13145a0fa403f4c693782d3baa9b`.
The reply footer now keeps only measured timing and the reported model; run IDs,
detailed outcome labels and diagnostic commands remain local. Failure warnings
and excerpt notices remain visible. `make check` passed, the inbox was idle
before restart, and the new process established a fresh Stream connection.

At 07:30 UTC, `make install` deployed host-phase reactions from an isolated,
verified source snapshot, with PID 56789 and binary SHA-256
`7bc29a02ef3c07df5d5a95f587b89811bec99b892fefc9e4ca31224a18e70ae4`.
This deployment includes memory retrieval, AI execution, Jev decision, evidence
and conditional experience phases; it excludes concurrent project-routing work.
State migrated from v5 to v6, preserving the four prior completed messages and
their platform-accepted replies. The inbox was idle and startup established a
fresh Stream connection. `make check` and 398 race cases across state, host,
channel, DingTalk transport and CLI passed. A blocked-Jev test confirms its
marker can be accepted before the decision returns. Live acceptance and client
display of the new phase labels await a new owner message.

## Registered project execution

The installed service uses its existing workspace as the default, and resolves
locally registered projects from its own state database. Registry writes retain
the single-host lock requirement. Source checkouts are never registered or
modified implicitly by installation. Native sessions share the isolated native
profile but bind separately to project, physical cwd and host runtime
configuration. Auto memory is disabled for child invocations. Historical default
workspace knowledge is not imported into other projects.

At 07:43 UTC on 2026-10-10, `make install` deployed project routing and runtime
assembly with PID 76308 and binary SHA-256
`229cde9ad09e0d2e733bb9d3d61bde80c0384dc865aadba62e84640f6a4613fd`.
That same binary passed six real Claude turns in two isolated projects. The
production inbox was idle; a stopped-state backup preceded schema v6 → v7.
All 12 historical table digests and four existing native-profile files remained
unchanged. Startup established a fresh Stream connection, and the process was
still running at the subsequent check. The production project registry remains
empty, so existing owner messages keep the default workspace. See the
[runtime assembly validation](runtime-assembly-detail.md#validation) and
installation evidence (private validation evidence).
Real DingTalk project-command round trips remain unverified.

## Runtime knowledge deployment

At 08:39 UTC on 2026-10-10, an isolated `make install` enabled Dokki and Confluence
runtime tools for the default workspace, with PID 68845 and binary SHA-256
`4039619822962c7b1cd613a56d04e01ef474fb55677a9a942d3c3a9d6409c335`.
Private source configuration and provider-specific token files now live under
the production config directory; the installed service has no memgov process,
database or checkout dependency. Five exact native MCP allow rules authorize
these read-only tools. Registered projects still need explicit source selection.

The inbox was idle before restart. The new process established a fresh Stream
connection and passed installed local source/service checks. State remained at
schema 7 with seven completed and one failed historical channel messages.
The binary excludes concurrent runtime-phase/schema work. The
installation receipt (private validation evidence)
and [native-source acceptance](../experiments/knowledge/README.md) record separate
evidence: no new DingTalk knowledge-question round trip is claimed. DWS
conversation intake and learning remain disabled.


## History-reference diagnosis and fixed workbench ports (2026-10-13)

The live DingTalk turns at 14:38–14:39 Singapore time completed execution and
reply delivery. The second normalized receipt contained an `interactiveCard`
quote's platform message ID and type, without body text. Later inspection found the
parser discarded this message type; absence in the receipt did not prove
absence in the raw callback. See the
[card parsing correction](channel-conversations-detail.md#interactive-card-quote-parsing-2026-10-13).
Its three history reads used the platform ID, which the Host incorrectly reported as invalid read bounds.
History search succeeded; the quoted original was absent from admitted history.
The reference error and tool guidance now identify this mismatch explicitly.
No external card retrieval or business-ticket completion is claimed.

The shared service workbench now uses fixed port 8765, with a CLI override.
Regression checks exercise restart URL stability, distinct source links for
colliding run/memory IDs, preserved raw tool evidence, invalid port configuration,
platform-ID rejection and continued reads of valid Host references.


After restart, the shared live API returned 54 runs (47 DingTalk, seven Feishu),
with readable details from both sources and two DingTalk memory records.
Execution and memory pages returned HTTP 200. Both channel connections emitted
fresh readiness events at 14:47 Singapore time. One viewer process listened on
8765, and both workers used the supervisor workbench. `make check` passed;
no task replay or outbound test message was performed.
