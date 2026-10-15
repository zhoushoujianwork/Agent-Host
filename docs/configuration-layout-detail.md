# Configuration and plugin startup details

当前数据源配置和加载规则见[数据源插件详细稿](datasource-plugins-design-detail.md)，
`datasource.plugins.*.json` 已替代初次迁移的 `plugins.email.json` 与
`plugins.knowledge.json`。

Current enabled datasource plugins are global; `access`, source project/audience
fields and Agent datasource selectors are obsolete. See the linked datasource
details for current assembly and migration behavior.

下文保留初次 unified rollout 的结构和证据，涉及旧插件
文件名及状态路径的段落属于迁移历史；当前使用方式见[运行指南](service-install.md)。


状态：统一布局、配置加载和现有部署迁移已实现。[主设计](configuration-layout.md)决定范围和用户行为。
以下说明区分已实现行为与后续扩展。

## 动态工程登记与运行记录

[工程记录](development-projects.md)改为请求触发的 Runtime 状态，不新增
`development.json`、开发能力 enabled 字段或人工项目启用步骤。现有 `host.json`
仍负责执行 profiles，Agent/渠道配置仍负责角色、身份和现有授权。
工程登记不是数据源插件，不使用 `datasource.plugins.*.json` 外壳。

已实现的 `internal/projectruntime.RootForState` 将 `ROOT/cli-state`、
`ROOT/channels/<name>/state` 和旧 `ROOT/state` 归入同一 `ROOT`；任意其他
自定义状态 home 自身作为根，保持独立。native cwd 不决定根目录。
登记和执行回执放 `ROOT/projects/state.db`，生成视图放
`ROOT/projects/<project-id>/`。不同 Host 根不能自动合并登记。

已实现顺序：在已知目录、`~/github`、`~/gitlab` 和当前 workspace 查找 Git 候选
→ 路由选定工程 → 原子登记来源与路径并更新入口兼容工程镜像 → 装配原生执行
→ 持续生成有来源的执行回执。只读取 Git 元数据，不搬动、复制或 clone。
查找限制为三层目录和 2,048 次目录探查，遇到 checkout 停止下钻，跳过隐藏、依赖
与构建目录；目录预算耗尽或边界外直接出现 checkout 时显式报错。
目录查找提示来自当前请求、Runtime 观察和常见本地位置，不需要用户声明 enable。

首次路由选定工程时按需创建登记；只读项目问题可登记目标，但不会登记全部候选。
启动预检不 clone、不执行任务。未来 clone/新工程准备、持久功能任务、单 writer
worktree、PR/MR 恢复与合并仍待实现；执行回执不能视作这些能力已交付。

当前工程身份来自 canonical Git common-directory 路径 hash；symlink 与同库
worktree 共享身份，已有 ID/历史 memory scope 保留。移动仓库可能改变派生身份，
尚不支持移动对账。登记保留 primary checkout，选定 worktree 保留实际执行 cwd。
项目规则不能授予新权限；未来发布/合并仍核对真实操作授权及当前提交。

`project.json`/`PROJECT.md` 和 `tasks/<run-id>/task.json`/`TASK.md` 由数据库
生成；更新与原子导出由 `registry.lock` 串行化，目录/文件采用 owner 私有权限。
JSON 的 `version` 是生成记录版本，不是 `version/enabled/settings` 启动配置。
任务文件当前是 `execution_receipt`：来源 state home/run ID、Agent/audience、
entry/message/session 引用、cwd、时间及独立执行/验收/记忆状态。
不复制原始 Input/Output、context、事件或凭证；共享登记不合并私有会话或记忆。
固定配置扫描器无需为了开发登记接受额外配置文件；当前入口已接入项目路由与
Runtime 装配路径，不影响现有组件的启用语义。

实现边界与后续功能任务样例见[工程记录详细稿](development-projects-detail.md)。
自动化覆盖无人工启用、身份/scope 兼容、共享登记去重、并发导出及来源隐私。
真实本地候选查找已找到 POP；clone、跨轮功能任务、单 writer 和 PR/MR 验收仍待实现。
后文旧 `plugins.*`、`knowledge/` 和首次迁移状态仍为历史证据；当前记忆布局见
[workspace 约定](workspace-layout.md)。

## 旧实现与本次变更

- `service_channels.go` 扫描 `channels/channel-*.json`，读取 `runtime_root`。
- `service.go` 从各 runtime 的 `config/` 加载渠道、模型和来源设置。
- Email 通过 `email_config` 逐个 runtime 引用，由 `resolveEmail` 挂载统一 MCP。
- 钉钉业务工具有独立配置和操作账本，但当前部署把状态放进了任务 workspace。
- 知识源由 `knowledge_config` 引用，现有 private snapshots 位于钉钉 runtime 下。
- 渠道 worker 隔离进程级 provider 环境、原生会话和 Host SQLite 写入。

保留已有 harness adapters、工具协议及状态所有权，在启动与上下文装配层接入
统一加载。原生模型/工具循环继续由 harness 管理。

## 配置发现和命名

默认根目录为启动项目的 `.agent-host`，配置目录为 `ROOT/config`。
已增加 `service check|run --config-dir ABSOLUTE_PATH`；目录必须是根目录下的
`config`，由其父目录确定运行根。`make run` 默认使用此入口。

固定配置文件为 `host.json` 和可选 `agents.json`。组件文件为
`channels.<id>.json` 和 `plugins.<id>.json`。首期 ID 分别为
`dingtalk`、`feishu`；`email`、`knowledge`。
文件名决定组件类型和稳定实例标识，不重复设置可冲突的 `type` 或 `id`。
首期每种组件一个实例，多邮箱放在 Email 的账号列表中。

只扫描顶层 JSON，不递归扫描 `credentials/`。未知配置文件或未知组件 ID
显式报错，避免拼错名称后静默漏加载。组件必须有整数 `version: 1` 和布尔
`enabled`；禁用项检查公共外壳，不要求完整业务 settings，也不访问凭证或网络。
JSON 重复字段、未知字段、非法路径等沿用现有严格读取方式。

运行目录由 `ROOT/channels/<id>` 和 `ROOT/plugins/<id>` 推导。普通配置引用路径
相对其所在配置文件解析，绝对路径也允许；知识源 `paths` 继续相对 source root。
加载后统一转为规范绝对路径供现有组件使用，拒绝不受支持的符号链接及目录别名。
私密配置目录使用 0700，文件使用 0600，并验证当前用户所有权。

## 配置职责

| 文件 | 内容 |
| --- | --- |
| `host.json` | version、default_profile、profiles；各 profile 包含 execution 和 environment_file |
| `agents.json` | 现有角色、项目许可、知识源子集、可选插件子集及指令引用 |
| `channels.<id>.json` | enabled、可选 execution profile、协议 settings、渠道 Agent 绑定 |
| `channels.dingtalk.json` | settings.bot 与 settings.dws 两个独立可选配置 |
| `plugins.email.json` | enabled、access、settings 中的邮箱 accounts 和 provider 参数 |
| `plugins.knowledge.json` | enabled、access、settings 中现有 sources 声明 |

公共 profile 避免重复模型配置；渠道可选择不同 profile，迁移时保留各自实际值，
不能用公共 defaults 覆盖当前不同 provider。凭证通过配置文件引用，不通过命令行
携带私密值。由 Host 推导的插件及渠道子功能状态路径替代需要用户填写的 state directory。

## 钉钉渠道可选功能

`settings.bot` 保存机器人身份、凭证引用与消息接入参数；`settings.dws` 保存
profile、services、账号设置及业务能力授权。各项存在时必须显式填写布尔
`enabled`。下例只表示启用结构，业务参数需补全后才能运行：

```json
{
  "version": 1,
  "enabled": true,
  "settings": {
    "dws": {
      "enabled": true
    }
  }
}
```

| bot | dws | 行为 |
| --- | --- | --- |
| 开启 | 缺省或关闭 | 仅机器人消息接入与回复，无 DWS 初始化或工具 |
| 缺省或关闭 | 开启 | 仅 DWS 业务能力，无机器人连接、消息回调或 reaction worker |
| 开启 | 开启 | 两项分别检查并加载，保留各自身份和授权 |
| 缺省或关闭 | 缺省或关闭 | 渠道总开关开启时报无有效功能；关闭时不启动 |

缺省与关闭分支不校验对应业务凭证、不构造 client、不创建功能状态目录、
不注册 MCP、不启动事件连接或采集任务。只启用 DWS 不依赖机器人 app secret
或 bot owner 配置，业务身份来自 DWS 自身授权；只启用机器人不要求 DWS 登录态。
渠道内部组合 bot 与 DWS 适配器，不把 DWS 注册为顶层插件。
DWS 状态统一在 `ROOT/channels/dingtalk/dws`，任务 workspace 保持独立。
现有 `dingtalk-workspace` CLI 子命令和底层包名称可保留，不决定配置归属。

DWS-only 允许渠道 worker 作为能力提供方运行，不伪造入站消息或启动机器人任务
循环。能力挂载沿用既有 owner 和工具策略；本期不因该配置自动向飞书开放 DWS。

## 通用插件配置

插件统一外壳为 `version`、`enabled`、`access`、`settings`。`access.channels`
为显式允许的渠道列表，`access.audiences` 为允许的 audience；Email 只支持 owner，
不能通过填写群 audience 放宽。渠道内 DWS 也保留现有 owner 限制。
首期启用插件必须声明 access。
Agent 插件子集只能缩小授权，省略时继承 eligible plugins；不增加 per-Agent
邮箱账号子集。本期配置作用于 service workers，普通 CLI 的显式工具参数保留。

Email `settings.accounts` 沿用现有账号 ID、provider、启用操作、连接参数和
credential references。Gmail OAuth 等运行时生成授权保存在插件运行目录。
固定账号凭证输入在 `config/credentials/`；迁移器按用途拆分原目录，保留操作
ID、账本和未知发送状态，不能通过目录迁移重新发送邮件。

知识 sources 的 project/audience grants 与 Agent `knowledge_sources` 保留；
新增 plugin access 与其取交集。远程知识凭证在配置凭证目录，本地材料在
`knowledge/` 或外部项目目录。禁用 knowledge 只取消工具，不删除材料。
历史话题采集继续由 Host collector 管理，由 execution 的 `discovery_config` 引用
现有选择配置，统一迁移工具遇到这类旧配置时要求显式路径映射，不自动猜测。它引用
已启用的 `channels.dingtalk.json` 中 DWS 及现有 source selectors；不要求 bot
开启。声明的 DWS 依赖未启用时明确报错；未启用的采集功能不初始化。

## 内置注册与生命周期

`service_unified.go` 使用明确的内置组件目录和加载分支检查当前支持的渠道/插件。
`componentconfig` 统一严格 envelope 和 settings 解码；各能力现有 loader 解码自身参数，
无需生成 runtime 下的配置副本。钉钉仅装配已启用 bot/DWS；Email、知识在统一
装配层按 channel access 选择，再由现有任务装配按 Agent/audience/project 过滤。
新内置实现必须加入目录与装配；动态发现任意外部代码和公共 factory API 尚未实现。

加载顺序：扫描配置 → 解析启用项 → 检查身份、依赖和完整配置 → 获得运行锁
→ 准备目录 → 启动必要服务及渠道 worker → 工作台展示。
`check` 不连接平台、不执行原生任务、不初始化业务数据库；所有启用项本地检查
通过后 `run` 才允许连接。无启用渠道时明确报错，后台插件不单独建立任务入口。

保留一渠道一 worker。插件“加载”表示能力可用，不要求每个插件都常驻进程。
现有按需 MCP 启动继续按需，带共享可变状态的插件需明确唯一写入者或数据库并发
协议。Email 多渠道共用账本必须验证现有事务和去重机制，不能直接复制两份账本。
若需要常驻插件服务，由 supervisor 管理单实例，各 worker 连接它。
没有共享安全证明时不能把相关能力同时挂到两个 worker。

任务装配取 enabled plugin、channel access、Agent subset、audience、project grants
和 native tool policy 的交集。tool-free extraction 不挂载插件。全局启用不等于
全渠道全任务挂载；内容与引用材料仍不能授予权限。

装配结果保留 config fingerprint，用配置变化使不兼容 native sessions 失效。
输出组件 ID、启用/禁用、运行目录、可用能力与失败原因，隐藏凭证。
依赖缺失在启动前报错；平台认证/连接失败沿用明确失败策略。必要常驻服务或
worker 异常退出时 supervisor 停止相关运行，不降级、不自动重放任务。
停止时取消任务、结束 worker/MCP 服务并释放锁；配置重载通过重启完成。

## 迁移和兼容

提供显式停止后迁移流程，不在启动时悄悄移动数据。先备份配置和目录映射，
旧格式通过现有显式 `--root`/`--channels-dir` 诊断入口继续可读；新默认入口
发现未迁移的部署时给出迁移提示，不自动 fallback。混用新旧 service config
报错，不进行隐式配置合并。兼容入口应复用新组件装配以保持权限语义一致。

| 旧位置 | 新位置 |
| --- | --- |
| `channels/channel-*.json` 及 runtime `config/` | `config/channels.*.json`、Host/Agent/profile/plugin 配置 |
| `dingtalk/{state,native,logs}` | `channels/dingtalk/{state,native,logs}` |
| `runtime/{state,workspace,native,logs}` | `channels/feishu/{state,workspace,native,logs}` |
| `dingtalk/workspace` 中任务文件 | `channels/dingtalk/workspace` |
| 同目录中业务登录态及 `host-operations.db` | `channels/dingtalk/dws/` |
| `email/` | 配置拆至 `config/`，账本和生成令牌移至 `plugins/email/` |
| `dingtalk/knowledge/owner` | `knowledge/owner` |

不能凭文件名批量猜测所有 workspace 内容：按已知业务文件清单迁移，未知内容
保留为任务文件并在迁移报告中列出。保留快照 manifest 和原始来源摘要。
运行历史、原始证据及历史路径保持不变，活动配置引用更新；验证迁移后的数据库
保留记录，并生成前后计数和身份映射报告。

新渠道默认记忆 scope 使用稳定的 `channel:<id>:default`，继续叠加既有
agent/audience 分区。迁移工具在 execution.memory_scope 保存旧默认 workspace
范围，通过 `--memory-scope` 独立于 cwd 传给执行装配。旧记忆、候选、历史 run、
版本和关联不需要改写，注册项目仍为 `project:<id>`。将旧记忆统一转换为新 scope
留作后续独立迁移；本次没有重新启用失效记忆。旧 native sessions 保留历史，
新 cwd 建立新绑定。回滚须停止所有写入后恢复备份，不能让旧新版本同时运行。

## 验证重点

- 有效/禁用配置、未知名称、版本、重复字段、权限和路径错误；禁用项无连接。
- bot-only、DWS-only、两项开启、缺省/关闭及总开关组合；未启用子功能无初始化，
  不检查其凭证、不创建状态目录、不构造 client、不挂载工具或运行后台任务。
- 双渠道不同 execution profile，统一 Email 插件及渠道与 owner/群权限过滤。
- 插件缺失依赖、凭证失败、服务异常退出和停止清理；没有隐式重放。
- 两渠道共享邮件操作 ID 并发去重，unknown/pending send 不重复发送。
- 知识来源权限与来源引用保留，知识读取不触发经验发布。
- 迁移数据库 SHA-256 和记录数一致，默认记忆 scope 独立于 cwd，原始证据不变。
- `make check`、本地预检，以及获授权的双渠道和插件实际调用验证。

## 已实现配置结构

```json
{
  "version": 1,
  "default_profile": "default",
  "profiles": {
    "default": {
      "execution": {
        "claude": "/absolute/path/claude",
        "model": "explicit-model",
        "jev_provider": "typesafe",
        "jev_model": "jev-latest",
        "timeout": "1h"
      },
      "environment_file": "credentials/environment.json"
    }
  }
}
```

插件 settings 沿用现有 typed 配置；Email 的 state_directory 和 DWS 的
state_directory 由加载器推导，可以省略。Knowledge settings 仍含 `version: 1`
和 `sources`，其 plugin audience access 与每个 source audience grant 取交集。
Agent 的可选 `plugins` 列表只能选择 email/knowledge；空列表禁用两者，省略则继承。
工作台端口仍通过 CLI 配置，不是 host.json 字段。`check` 不创建运行目录；`run`
预检后才准备路径。DWS-only 不连接机器人；显式启用采集时运行 collector，否则
保持能力可用状态并等待关闭，CLI 可用同一渠道配置按需调用 DWS。

## 2026-10-13 迁移与启动验证

停止旧 supervisor 后，迁移工具持有旧 runtime 的运行锁，备份旧配置与数据。
两份 Host 数据库在迁移前后的 SHA-256 与各表记录数完全一致。任务文件保留，
DWS 登录态移入 channels/dingtalk/dws，Email 保留一份共享操作账本，私人知识快照
移入顶层 knowledge。迁移报告和原部署备份保存在忽略目录 backups 中。

新配置本地检查通过，make run 使用 --config-dir 启动两渠道，观察到新的 DingTalk
Stream connection、Feishu meeting entry connection 和 Feishu text receiver ready。
工作台 8765 已启动。自动化检查覆盖 bot-only/DWS-only/both、禁用项不初始化、
插件 channel/audience 过滤、迁移锁与数据保留。第一次全量并行检查有 fixture 时限
失败，失败项独立复跑通过，随后 make check GOFLAGS=-p=1 全量通过。
此记录证明启动及配置生效，不代表新增渠道用户消息的往返验收。

同一配置的隔离 Host CLI 验证完成，观察到成功的 email list_accounts、check_account、
DWS list_services 和 owner_notes 本地知识检索调用。直接邮箱连接检查确认 IMAP 与
SMTP authenticated；迁移后的 DWS auth status 返回 authenticated=true。工作台
HTTP 返回 200。验证未读取邮箱消息、未发送邮件或渠道测试消息；私密工具证据保存在
迁移备份的 validation 子目录。
