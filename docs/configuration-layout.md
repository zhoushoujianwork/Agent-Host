# Configuration and plugin startup

状态：统一配置加载、渠道 bot/DWS 可选项和部署迁移已实现。使用方式见[运行指南](service-install.md)。
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
├── knowledge/                      # 当前部署的旧资料位置；新方案将迁入数据源目录
│   └── owner/snapshots/<快照名>/
├── cli-state/                      # 普通终端聊天的现有状态
└── backups/                        # 迁移备份和旧部署材料
```

插件的可编辑配置放 `config/datasource.plugins.<名称>.json`；`datasource/plugins/<名称>/ 保存运行时
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

## 知识与记忆

[文件记忆与 Skill 新方案](file-memory-design.md)规划增加 `.agent-host/memory/`，
保存学习后的知识正文、Skill 和版本；SQLite 保留运行状态和可重建索引。
该方案尚未迁移或启用，下面仍描述当前部署行为。
[workspace 目录方案](workspace-layout.md)补充 Agent 指令、用户 `USER.md`、手写 Skill
与任务上下文视图的归属；新增路径同样尚未生效。
新方案约定 config 为运行期只读的用户输入，生成内容写入 memory、cache 或所属
数据源运行目录；取消顶层 knowledge，并显式迁移已有快照与配置引用。

`knowledge/` 保存有来源的知识材料，由 `datasource.plugins.local-documents.json` 声明读取范围。
项目已有文档留在项目目录，通过知识源配置引用。私人快照可以供多个渠道引用，
但仍需满足项目、Agent 与 owner 可见范围，不能因为文件共享就向群聊开放。

Host 记忆仍保存在各渠道的 `state/state.db`，原生会话仍在 `native/`。
迁移后的渠道通过执行配置 `memory_scope` 保留原默认记忆范围；新渠道默认使用
稳定的 `channel:<名称>:default`，保证目录变化不再改变新渠道的记忆身份。
知识读取不会自动成为记忆。这次统一插件加载，不合并钉钉和飞书记忆数据库；
跨渠道助理记忆需要单独设计归属、授权和数据库写入生命周期。

## 本期交付与验证

1. 统一配置扫描、严格预检和已支持内置组件的启动装配。
2. 接入钉钉渠道的 bot/DWS 可选项、飞书渠道及 Email、知识插件；保留现有工具语义。
3. 停止后备份迁移配置、渠道数据、插件状态和知识快照，保留旧默认记忆范围。
4. 自动化验证独立启用和权限过滤；实际双渠道连接及插件验证见详细稿。

本期交付标准是“配置启用即可在启动时加载已有内置能力”。外部插件安装、公共 SDK、
插件市场、热加载和共享记忆服务留待具体需求出现后扩展。
