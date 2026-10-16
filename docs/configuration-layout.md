# Configuration and plugin startup

状态：统一配置加载、渠道 bot/DWS 可选项和部署迁移已实现。使用方式见[运行指南](service-install.md)。
本地工程查找、自动登记和共享执行回执已实现；不新增人工启用配置。
clone、新工程、持久开发任务和 worktree/PR/MR 生命周期仍待实现。
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
│   ├── agents.json                 # Agent 角色和任务项目
│   ├── users.json                  # Optional verified private-user mappings and source grants
│   ├── backup.json                 # Optional encrypted backup policy
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
├── projects/                       # Runtime 自动登记的工程与执行回执
│   ├── state.db                    # Host 级工程登记与执行记录
│   ├── registry.lock               # 登记更新与视图导出锁
│   └── <project-id>/               # project.json、PROJECT.md、tasks/<run-id>/ 回执
├── cli-state/                      # 普通终端聊天的现有状态
├── backup-state/                   # Backup lock and last-success/failure receipt
└── backups/                        # Automatic recovery archives
```

插件的可编辑配置放 `config/datasource.plugins.<名称>.json`；`datasource/plugins/<名称>/` 保存运行时
产生的数据，不再放一套独立配置。凭证输入与插件生成的令牌分别归入配置凭证目录
和插件状态目录，加载器不会扫描它们作为组件配置。

`backup.json` is a separate Host policy, validated at startup rather than decoded
as a channel/plugin envelope. It schedules encrypted archives directly in
`backups/` by default; an external destination is optional. The recovery key stays
outside the root and archive directory. See
[workspace backup and recovery](workspace-backup-design.md).

`workspace` 只保存任务文件和产物，不放账号登录态。关联项目的任务仍使用项目真实
目录，不需要把项目代码搬入 `.agent-host/`。目录结构表达归属，不构成操作系统隔离。
DWS 的账号状态、生成令牌和操作账本放 `channels/dingtalk/dws/`。

## 启用与加载

例如，邮件插件的配置入口如下；邮箱参数填写在同一文件的 `settings` 中：

```json
{
  "version": 1,
  "enabled": true,
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

Enabled datasource plugins are global across channels, Agents, projects and
owner/group conversations at catalog level; optional `users.json` restricts
non-owner/shared sources through explicit grants. Current restricted member
execution omits business MCP tools. Older plugin `access` and source/Agent
selectors are ignored by unified plugin loading. DingTalk DWS remains a
channel feature. Sending still requires authorization for the requested operation.
Restart `make run` after configuration changes; hot reload is not implemented.

## 动态工程登记与运行目录

[工程记录](development-projects.md)属于 Runtime：CLI 和通用渠道路由先在有限
本地候选中选择工程，再自动登记目录、来源和执行回执。只读项目问题也可登记
已选工程；目录盘点不登记全部候选。
不要求用户先 enable、手工登记项目或创建 `config/development.json`。

| 职责 | 位置 |
| --- | --- |
| 模型、执行 profile、provider 环境引用 | 现有 `config/host.json` |
| 角色、既有授权与渠道身份 | 现有 Agent/渠道配置 |
| 工程目录、请求来源、执行/验收/记忆状态回执 | Runtime 生成的 `projects/` |

优先复用用户电脑上已有工程，包括当前请求的目录、已知工程位置及可访问的
`~/github`、`~/gitlab` Git 候选；只查 Git 元数据，已有仓库原地使用。
未来需要 clone 已确定的远程工程或创建新工程时，计划在
`.agent-host/projects/<project-id>/repository/` 准备源码，再建立任务 worktree；
这部分及 PR/MR 生命周期尚未实现。

同一 Host 的 CLI/渠道状态目录共享工程登记；任意自定义状态 home 保持独立。
`tasks/<run-id>/` 当前是有来源的执行回执，不是跨多轮的功能开发任务。
共享回执不保存原始输入、输出和上下文，入口保留兼容工程镜像与原始证据。
角色、原生会话和私人记忆仍保留原归属；动态登记不扩大工具、资料或发布/合并权限。
工程登记和回执是持久运行资源，不随普通上下文缓存清理。

Runtime 绑定与后续恢复方案见[详细稿](configuration-layout-detail.md#动态工程登记与运行记录)。
现有模型、渠道和数据源组件的启用规则保持原用途。

## 知识与记忆

[文件记忆与 Skill 方案](file-memory-design.md)已实现并完成本地渠道迁移：
`memory/` 保存学习后的知识正文、Skill 和版本；SQLite 保留运行证据和可重建索引。
未迁移的 runtime 仍使用兼容的 SQLite 记忆后端；后端选择与迁移回执不能由目录存在推断。
[workspace 目录方案](workspace-layout.md)补充 Agent 指令、用户 `USER.md`、手写 Skill
与任务上下文视图的归属。config 为运行期只读的用户输入，生成内容写入 memory、
cache 或所属数据源运行目录；工程运行记录写入 Host 的 `projects/`。
已迁移布局取消顶层 knowledge，并保留原快照来源和授权。

有来源的本地材料由 `datasource.plugins.local-documents.json` 声明读取范围，
迁移后的保留快照进入本地文档数据源存储；详细稿中的旧 `knowledge/` 路径是迁移历史。
项目已有文档留在项目目录，通过知识源配置引用。私人快照可以供多个渠道引用，
Enabled datasource plugins supply the configured snapshot catalog globally.
Optional user/group grants and the execution profile constrain actual access.
File paths define document coverage; they do not configure task audiences.

各渠道的运行证据仍保存在 `state/state.db`，原生会话仍在 `native/`；
文件后端的正式记忆由来源发布清单决定，不由运行数据库或生成视图替代。
迁移后的渠道通过执行配置 `memory_scope` 保留原默认记忆范围；新渠道默认使用
稳定的 `channel:<名称>:default`，保证目录变化不再改变新渠道的记忆身份。
知识读取不会自动成为记忆。统一加载和共享工程状态均不合并
钉钉、飞书及不同 Agent 的私有会话或记忆；工程进度共享与记忆授权保持独立。

## 本期交付与验证

1. 统一配置扫描、严格预检和已支持内置组件的启动装配。
2. 接入钉钉渠道的 bot/DWS 可选项、飞书渠道及 Email、知识插件；保留现有工具语义。
3. 停止后备份迁移配置、渠道数据、插件状态和知识快照，保留旧默认记忆范围。
4. 自动化验证独立启用和全局插件装配；实际双渠道连接及插件验证见详细稿。

本期交付标准是“配置启用即可在启动时加载已有内置能力”。外部插件安装、公共 SDK、
插件市场、热加载和共享记忆服务留待具体需求出现后扩展。
