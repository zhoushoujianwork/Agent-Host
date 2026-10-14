# Configuration and plugin startup

状态：统一配置加载、渠道 bot/DWS 可选项和部署迁移已实现。使用方式见[运行指南](service-install.md)。
工程开发配置与共享工程 workspace 是待实现扩展，不属于当前加载器支持范围。
实现约定见[详细稿](configuration-layout-detail.md)；数据源分类与实例规则见
[数据源插件设计](datasource-plugins-design.md)。

## 目标和使用方式

把所有手工维护的配置集中到 `.agent-host/config/`。渠道负责接收任务和交付
回复并管理所属平台的可选能力；插件提供邮件、知识检索等通用能力。配置中设置
`enabled: true`，下次 `make run` 时由 Host 发现、检查和加载。

Email 是内置能力插件，可以供钉钉和飞书使用。DWS 与钉钉机器人是钉钉渠道内
两个独立可选项，统一放在 `channels.dingtalk.json`，可以只启用其中一个。
未配置的选项不初始化、不连接、不挂载工具，也不启动对应后台功能。
首期加载随程序编译的内置插件，
增加新插件实现后沿用同一启动机制。

## 目录约定

```text
.agent-host/
├── config/                         # 人工维护的配置，唯一入口
│   ├── host.json                   # 命名执行配置与 provider 环境文件引用
│   ├── agents.json                 # Agent 角色、项目和能力选择
│   ├── development.json            # 待实现：共享工程目录、查找范围和默认开发规则
│   ├── channels.dingtalk.json
│   ├── channels.feishu.json
│   ├── datasource.plugins.email.json
│   ├── datasource.plugins.local-documents.json
│   ├── datasource.plugins.dokki.json
│   ├── datasource.plugins.confluence.json
│   └── credentials/                # 配置引用的私密凭证
├── channels/                       # 按渠道隔离的运行数据
│   ├── dingtalk/{state,workspace,native,logs,dws}
│   └── feishu/{state,workspace,native,logs}
├── datasource/plugins/                        # 按插件隔离的运行数据
│   ├── email/                      # 操作账本、授权令牌等
│   └── ...                         # 其他插件需要的运行状态
├── memory/                         # 已迁移文件后端的正式知识、Skill 和版本
├── cache/                          # 可重建检索索引和本轮上下文视图
├── cli-state/                      # 普通终端聊天的现有状态
└── backups/                        # 迁移备份和旧部署材料
```

插件的可编辑配置放 `config/datasource.plugins.<名称>.json`；`datasource/plugins/<名称>/` 保存运行时
产生的数据，不再放一套独立配置。凭证输入与插件生成的令牌分别归入配置凭证目录
和插件状态目录，加载器不会扫描它们作为组件配置。

`workspace` 只保存任务文件和产物，不放账号登录态。关联项目的任务仍使用项目真实
目录，不需要把项目代码搬入 `.agent-host/`。目录结构表达归属，不构成操作系统隔离。
DWS 的账号状态、生成令牌和操作账本放 `channels/dingtalk/dws/`。

## 启用与加载

例如，邮件插件的配置入口如下；邮箱参数填写在同一文件的 `settings` 中：

```json
{
  "version": 1,
  "enabled": true,
  "access": {
    "channels": ["dingtalk", "feishu"],
    "audiences": ["owner"]
  },
  "settings": {}
}
```

`settings: {}` 仅展示结构，不是可运行的邮件配置。邮箱账号、连接参数及凭证引用
在详细稿中说明。渠道配置使用相同的 `version`、`enabled` 和 `settings` 结构。
文件名决定加载哪个内置渠道或插件，用户无需再填写运行目录或重复引用邮件配置。

钉钉渠道的 `settings.bot` 与 `settings.dws` 各自带 `enabled` 和业务参数。
省略或设为 `enabled: false` 就不加载该部分；可以只启用机器人、只启用 DWS，
或同时启用。只启用 DWS 时提供其配置允许的业务能力，不建立机器人消息连接。
渠道总开关关闭时，两部分都不运行；总开关开启时至少要启用其中一项。

启动先检查启用组件的配置，再准备运行目录，最后启动渠道和插件所需服务。
配置错误在连接外部平台之前报告；平台连接或授权失败明确显示，不自动换提供方
或重放任务。禁用组件不连接、不挂载工具，也不删除其历史数据。

插件启用后，Host 按渠道、Agent 和可见范围决定是否向本次任务提供工具。
私人邮件和渠道内 DWS 个人业务工具默认仅向 owner 私聊提供；启用本身不授予发送消息
等业务操作的授权。配置修改通过重启 `make run` 生效，本期不做热加载。

## 工程开发配置与共享 workspace（待实现）

[工程记录方案](development-projects.md)纳入统一配置约定：`development.json` 是
可选的 Host 内建开发能力配置，不是数据源插件，也不放进各 Agent 或渠道 workspace。
沿用 `version: 1`、`enabled`、`access` 和 `settings` 结构。

| 配置职责 | 位置 |
| --- | --- |
| 共享开发根目录、已有仓库查找范围、clone 子目录、默认开发规则 | `config/development.json` |
| 模型、执行 profile、provider 环境引用 | 现有 `config/host.json` |
| Agent 角色、项目与能力选择 | 现有 `config/agents.json` |
| 工程登记、任务进度、PR 状态、worktree 和生成记录 | 共享开发 workspace，不是可编辑启动配置 |

拟默认使用 `~/workspace/development/`，已有仓库查找根为 `~/github`、`~/gitlab`，
显式要求的新 clone 放入开发根下的 `repositories/`。已有仓库原地使用。
CLI 和渠道引用同一开发配置及同一共享根，Agent workspace 只引用任务。
配置与运行数据可以分处不同目录，目录位置不授予工具访问、发布或合并权限。

未配置或关闭时不初始化开发状态、不扫描或 clone 仓库、不挂载开发工具。
启用后的预检只检查配置；启动准备共享状态，实际扫描、clone 和开发由任务触发。
访问范围首期只允许本地 owner 和 owner 私聊；Agent 选择与项目授权进一步缩小范围。

配置样例和加载细节见[详细稿](configuration-layout-detail.md#工程开发配置待实现)。
**当前程序不识别 `development.json`；此时放入实际配置目录会触发未知配置错误。**
样例保存在文档目录，不能据此宣称已启用开发能力。

## 知识与记忆

[文件记忆与 Skill 方案](file-memory-design.md)已实现并完成本地渠道迁移：
`memory/` 保存学习后的知识正文、Skill 和版本；SQLite 保留运行证据和可重建索引。
未迁移的 runtime 仍使用兼容的 SQLite 记忆后端；后端选择与迁移回执不能由目录存在推断。
[workspace 目录方案](workspace-layout.md)补充 Agent 指令、用户 `USER.md`、手写 Skill
与任务上下文视图的归属。config 为运行期只读的用户输入，生成内容写入 memory、
cache 或所属数据源运行目录；拟新增的工程运行记录写入共享开发根。
已迁移布局取消顶层 knowledge，并保留原快照来源和授权。

有来源的本地材料由 `datasource.plugins.local-documents.json` 声明读取范围，
迁移后的保留快照进入本地文档数据源存储；详细稿中的旧 `knowledge/` 路径是迁移历史。
项目已有文档留在项目目录，通过知识源配置引用。私人快照可以供多个渠道引用，
但仍需满足项目、Agent 与 owner 可见范围，不能因为文件共享就向群聊开放。

各渠道的运行证据仍保存在 `state/state.db`，原生会话仍在 `native/`；
文件后端的正式记忆由来源发布清单决定，不由运行数据库或生成视图替代。
迁移后的渠道通过执行配置 `memory_scope` 保留原默认记忆范围；新渠道默认使用
稳定的 `channel:<名称>:default`，保证目录变化不再改变新渠道的记忆身份。
知识读取不会自动成为记忆。统一加载和拟新增的共享工程状态均不合并
钉钉、飞书及不同 Agent 的私有会话或记忆；工程进度共享与记忆授权保持独立。

## 本期交付与验证

1. 统一配置扫描、严格预检和已支持内置组件的启动装配。
2. 接入钉钉渠道的 bot/DWS 可选项、飞书渠道及 Email、知识插件；保留现有工具语义。
3. 停止后备份迁移配置、渠道数据、插件状态和知识快照，保留旧默认记忆范围。
4. 自动化验证独立启用和权限过滤；实际双渠道连接及插件验证见详细稿。

本期交付标准是“配置启用即可在启动时加载已有内置能力”。外部插件安装、公共 SDK、
插件市场、热加载和共享记忆服务留待具体需求出现后扩展。
