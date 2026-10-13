# Configuration and plugin startup

状态：设计提案，尚未实现或迁移。现行启动方式仍见[运行指南](service-install.md)。
实现约定见[详细稿](configuration-layout-detail.md)。

## 目标和使用方式

把所有手工维护的配置集中到 `.agent-host/config/`。渠道负责接收任务和交付
回复；插件提供邮件、知识检索、钉钉业务操作等能力。配置中设置
`enabled: true`，下次 `make run` 时由 Host 发现、检查和加载。

Email 是内置能力插件，可以供钉钉和飞书使用。钉钉消息渠道与钉钉业务工具插件
分别配置，启用其中一个不会自动启用另一个。首期加载随程序编译的内置插件，
增加新插件实现后沿用同一启动机制。

## 目录约定

```text
.agent-host/
├── config/                         # 人工维护的配置，唯一入口
│   ├── host.json                   # 模型、执行策略和工作台等公共设置
│   ├── agents.json                 # Agent 角色、项目和能力选择
│   ├── channels.dingtalk.json
│   ├── channels.feishu.json
│   ├── plugins.email.json
│   ├── plugins.dingtalk-workspace.json
│   ├── plugins.knowledge.json
│   └── credentials/                # 配置引用的私密凭证
├── channels/                       # 按渠道隔离的运行数据
│   ├── dingtalk/{state,workspace,native,logs}
│   └── feishu/{state,workspace,native,logs}
├── plugins/                        # 按插件隔离的运行数据
│   ├── email/                      # 操作账本、授权令牌等
│   ├── dingtalk-workspace/          # 账号状态、登录令牌、操作账本等
│   └── knowledge/                  # 插件需要的缓存或运行状态
├── knowledge/                      # 知识材料，按归属组织
│   └── owner/snapshots/<快照名>/
├── cli-state/                      # 普通终端聊天的现有状态
└── backups/                        # 迁移备份和旧部署材料
```

插件的可编辑配置放 `config/plugins.<名称>.json`；`plugins/<名称>/` 保存运行时
产生的数据，不再放一套独立配置。凭证输入与插件生成的令牌分别归入配置凭证目录
和插件状态目录，加载器不会扫描它们作为组件配置。

`workspace` 只保存任务文件和产物，不放账号登录态。关联项目的任务仍使用项目真实
目录，不需要把项目代码搬入 `.agent-host/`。目录结构表达归属，不构成操作系统隔离。

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

启动先检查完整配置，再准备启用组件的运行目录，最后启动渠道和插件所需服务。
配置错误在连接外部平台之前报告；平台连接或授权失败明确显示，不自动换提供方
或重放任务。禁用组件不连接、不挂载工具，也不删除其历史数据。

插件启用后，Host 按渠道、Agent 和可见范围决定是否向本次任务提供工具。
私人邮件和钉钉个人业务工具默认仅向 owner 私聊提供；启用本身不授予发送消息
等业务操作的授权。配置修改通过重启 `make run` 生效，本期不做热加载。

## 知识与记忆

`knowledge/` 保存有来源的知识材料，由 `plugins.knowledge.json` 声明读取范围。
项目已有文档留在项目目录，通过知识源配置引用。私人快照可以供多个渠道引用，
但仍需满足项目、Agent 与 owner 可见范围，不能因为文件共享就向群聊开放。

Host 记忆仍保存在各渠道的 `state/state.db`，原生会话仍在 `native/`。
知识读取不会自动成为记忆。这次统一插件加载，不合并钉钉和飞书记忆数据库；
跨渠道助理记忆需要单独设计归属、授权和数据库写入生命周期。

## 本期范围与实施顺序

1. 建立统一配置扫描和内置组件注册，提供检查及加载结果展示。
2. 接入钉钉、飞书渠道以及 Email、钉钉业务工具、知识插件；保留现有工具语义。
3. 停止旧服务，备份并迁移配置、渠道数据、插件状态和知识快照，处理默认记忆范围。
4. 验证双渠道启动、插件按权限挂载、知识检索、存量记忆和停机清理，更新运行指南。

本期交付标准是“配置启用即可在启动时加载已有内置能力”。外部插件安装、公共 SDK、
插件市场、热加载和共享记忆服务留待具体需求出现后扩展。
