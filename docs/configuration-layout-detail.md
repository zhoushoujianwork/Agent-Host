# Configuration and plugin startup details

状态：待实现。[主设计](configuration-layout.md)决定范围和用户行为。
以下约定不代表当前二进制已支持新文件名或配置结构。

## 当前实现与差距

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
拟增加 `service check|run --config-dir ABSOLUTE_PATH`；目录必须是根目录下的
`config`，由其父目录确定运行根。`make run` 默认使用此入口。

固定配置文件为 `host.json` 和可选 `agents.json`。组件文件为
`channels.<id>.json` 和 `plugins.<id>.json`。首期 ID 分别为
`dingtalk`、`feishu`；`email`、`dingtalk-workspace`、`knowledge`。
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
| `host.json` | 公共执行 defaults、命名 execution profiles、工作台、并发策略 |
| `agents.json` | 现有角色、项目许可、知识源子集、可选插件子集及指令引用 |
| `channels.<id>.json` | enabled、可选 execution profile、协议 settings、渠道 Agent 绑定 |
| `plugins.email.json` | enabled、access、settings 中的邮箱 accounts 和 provider 参数 |
| `plugins.dingtalk-workspace.json` | enabled、access、settings 中的 profile、services 和账号设置 |
| `plugins.knowledge.json` | enabled、access、settings 中现有 sources 声明 |

公共 profile 避免重复模型配置；渠道可选择不同 profile，迁移时保留各自实际值，
不能用公共 defaults 覆盖当前不同 provider。凭证通过配置文件引用，不通过命令行
携带私密值。由 Host 推导的插件状态路径替代需要用户填写的 state directory。

插件统一外壳为 `version`、`enabled`、`access`、`settings`。`access.channels`
为显式允许的渠道列表，`access.audiences` 为允许的 audience；Email 与钉钉业务
工具只支持 owner，不能通过填写群 audience 放宽。首期启用插件必须声明 access。
Agent 插件子集只能缩小授权，省略时继承 eligible plugins；不增加 per-Agent
邮箱账号子集。本期配置作用于 service workers，普通 CLI 的显式工具参数保留。

Email `settings.accounts` 沿用现有账号 ID、provider、启用操作、连接参数和
credential references。Gmail OAuth 等运行时生成授权保存在插件运行目录。
固定账号凭证输入在 `config/credentials/`；迁移器按用途拆分原目录，保留操作
ID、账本和未知发送状态，不能通过目录迁移重新发送邮件。

知识 sources 的 project/audience grants 与 Agent `knowledge_sources` 保留；
新增 plugin access 与其取交集。远程知识凭证在配置凭证目录，本地材料在
`knowledge/` 或外部项目目录。禁用 knowledge 只取消工具，不删除材料。
历史话题采集继续由 Host collector 管理，其选择配置归入 `host.json`，引用
已启用的钉钉业务插件及现有 source selectors；不伪装成聊天渠道或自动学习插件。

## 内置注册与生命周期

建立小型 Go 内置注册表，注册已实现的 channel factories 和 plugin factories。
插件 factory 负责解码和校验自身 settings、声明依赖及能力、生成任务 MCP binding。
公共加载器负责发现、enabled、路径、生命周期和加载状态。避免在各渠道中分别
维护 Email、知识、钉钉工具启用分支。

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
| 同目录中业务登录态及 `host-operations.db` | `plugins/dingtalk-workspace/` |
| `email/` | 配置拆至 `config/`，账本和生成令牌移至 `plugins/email/` |
| `dingtalk/knowledge/owner` | `knowledge/owner` |

不能凭文件名批量猜测所有 workspace 内容：按已知业务文件清单迁移，未知内容
保留为任务文件并在迁移报告中列出。保留快照 manifest 和原始来源摘要。
运行历史、原始证据及历史路径保持不变，活动配置引用更新；验证迁移后的数据库
保留记录，并生成前后计数和身份映射报告。

默认记忆 scope 从实际 cwd 改为稳定 `channel:<id>:default`，继续叠加既有
agent/audience 分区。注册项目仍为 `project:<id>`。迁移需事务性转换当前默认
scope 的有效记忆、待处理候选及其索引/关联，保留版本与来源；历史失效记忆
不重新启用，历史 run scope 不重写，依赖 run 的发布和重试路径须使用明确的
scope 映射。旧 native sessions 保留历史，新 cwd 建立新绑定。映射和旧备份用于
停止写入后的回滚。变更前先枚举所有按 scope 查找的关系，不能只更新 memories。

## 验证重点

- 有效/禁用配置、未知名称、版本、重复字段、权限和路径错误；禁用项无连接。
- 双渠道不同 execution profile，统一 Email 插件及渠道与 owner/群权限过滤。
- 插件缺失依赖、凭证失败、服务异常退出和停止清理；没有隐式重放。
- 两渠道共享邮件操作 ID 并发去重，unknown/pending send 不重复发送。
- 知识来源权限与来源引用保留，知识读取不触发经验发布。
- 迁移记录完整，稳定默认 scope 可召回，旧候选可接受/重试，原始证据不变。
- `make check`、本地预检，以及获授权的双渠道和插件实际调用验证。
