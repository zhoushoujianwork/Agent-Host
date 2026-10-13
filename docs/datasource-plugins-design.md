# 数据源插件配置设计

状态：已实现并迁移运行配置。这是[统一配置与启动方案](configuration-layout.md)的后续调整：
将数据源接入统一命名为 `datasource.plugins.*`，替代原有的 `plugins.email.json`
和 `plugins.knowledge.json`。现行服务通过这些文件名加载四种内置数据源。
实现约定见[详细稿](datasource-plugins-design-detail.md)。

## 目标和使用方式

用户配置需要接入的资料来源，设置 `enabled: true`，Host 在启动时加载对应的
内置数据源插件。Agent 按需查询和读取资料，回答保留来源。

本地项目文档、私人笔记、历史快照使用同一个本地文档插件，只是来源实例不同。
不额外建立“旧记忆导入”机制，也不要求用户把文档复制到固定目录。

## 配置与目录

```text
.agent-host/
├── config/
│   ├── host.json
│   ├── agents.json
│   ├── channels.dingtalk.json
│   ├── channels.feishu.json
│   ├── datasource.plugins.local-documents.json
│   ├── datasource.plugins.dokki.json
│   ├── datasource.plugins.confluence.json
│   ├── datasource.plugins.email.json
│   └── credentials/
├── channels/                       # 渠道历史、工作目录、会话和日志
├── datasource/plugins/             # 插件产生的状态、缓存、令牌和操作账本
│   ├── local-documents/
│   ├── dokki/
│   ├── confluence/
│   └── email/
└── knowledge/                      # 可选的文档存放位置，不是另一套存储系统
```

只有需要持久运行数据的插件才创建状态目录。本地文档直接引用配置的原始目录，
普通查询不复制正文。需要保留历史版本时可以显式创建快照，来源清单与校验信息
随快照保留；普通文档不强制具有 manifest。

## 插件与来源实例

| 插件 | 一个配置文件可以包含什么 |
| --- | --- |
| `local-documents` | 多个本地来源，例如项目文档和 owner 私人笔记 |
| `dokki` | 多个使用不同凭证或授权范围的 Dokki 来源 |
| `confluence` | 多个 Confluence 来源 |
| `email` | 多个 Gmail/163 邮箱账号，沿用现有账号模型 |

首期 `local-documents` 复用现有 Markdown 搜索和读取能力。名称表示本地文档
接入职责，并不意味着已支持 PDF、Word、图片或任意文件解析；这些格式另行扩展。

每个文件使用统一的 `version`、`enabled`、`access`、`settings` 外壳。
文件名决定插件类型；`settings` 保存该类型的来源列表或邮箱账号，来源名称在
所属插件内唯一。插件可以独立省略或关闭，修改配置后重启 `make run` 生效。

Host 启动时发现配置、检查启用项，再加载内置实现。未配置或关闭的插件不连接
平台、不初始化客户端、不挂载工具，也不创建运行状态。配置或认证失败明确报告。
启用表示能力可用，文档正文不会在启动时全部读入模型上下文。

## 授权和职责

可用来源由插件的渠道授权、Agent 选择、任务项目与可见范围共同决定。
私人笔记和邮件保留 owner 限制；共享文件位置不等于共享读取权限。
Email 仍保留账号级发送开关、操作去重和业务授权，启用数据源不自动授权发送。

钉钉机器人与 DWS 继续作为 `channels.dingtalk.json` 的两个独立选项，不移到
数据源插件配置中。Host 对话历史和 SQLite 记忆仍由 Host 内部管理。

现成资料通过数据源工具按需读取；长期偏好和已接受执行经验通过 Host 记忆召回。
本地笔记不会自动写入 SQLite 记忆，读取记录可以作为运行证据留存。

## 交付与验证

1. 增加统一数据源插件注册和新配置发现，接入四个已实现的适配器。
2. 拆分旧知识配置，保留来源名称、授权、工具行为和来源引用。
3. 停止写入后迁移 Email 状态，保留唯一账本；本地文档正文保留原位。
4. 验证独立启用、多来源、权限过滤、只读检索及配置迁移，并切换启动入口。

已完成配置迁移、双渠道重新连接与隔离原生工具验证：本地笔记检索、邮箱账号
发现及认证检查通过。迁移前后渠道数据库与 Email 账本校验值相同；没有读取
邮件正文或发送消息。Dokki/Confluence 新配置通过本地预检，远程调用未在本次复验。

本期不新增文档格式、向量数据库、自动资料导入或共享记忆服务，也不建立外部
插件安装机制。详细稿中的字段与迁移约定可在实施时补充，范围以本稿为准。
