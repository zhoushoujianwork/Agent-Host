# Host 目录与 workspace 文件约定

状态：2026-10-13 设计提案，尚未创建运行目录或更改当前服务。
沿用现有名称 `.agent-host/`，不新增 `.agents-host/`。
与[文件记忆方案](file-memory-design.md)配套；实现约定见[详细稿](workspace-layout-detail.md)。

## 目标与使用方式

让用户一眼找到配置、Agent 指令、用户偏好、学习后的 Skill 和任务产物。
配置定义启用、角色与授权；记忆保存学习结果；workspace 提供当前任务实际需要的
上下文。私人资料按用户和使用范围装配，不因为同一个 Agent 进入群聊就一起带入。

`config/` 只保存用户维护的输入，Host 运行时只读。自动生成的用户偏好与 Skill
写入 `memory/`；资料快照写入所属数据源目录；任务装配文件写入 `generated/`，
检索索引写入 `cache/`。
新布局取消顶层 `knowledge/`，本地原文继续由数据源配置引用，不要求搬动。

参考 [OpenClaw 的 workspace 文件约定](https://docs.openclaw.ai/concepts/agent-workspace)，
采用 `AGENTS.md`、`SOUL.md`、`IDENTITY.md`、`USER.md` 和 `MEMORY.md` 的可读形式。
其当前工具说明归入 `AGENTS.md` 的 Tools 章节，本方案沿用该方式。
加载规则由 Host 实现并适配原生引擎，不假设原生 CLI 会自动识别这些文件。

## 根目录

```text
.agent-host/
├── config/                          # 用户维护的设置与指令
│   ├── host.json
│   ├── agents.json
│   ├── channels.dingtalk.json
│   ├── channels.feishu.json
│   ├── datasource.plugins.*.json
│   ├── agents/<agent-id>/            # 可选，用户手写的 Agent 配置原文
│   │   ├── AGENTS.md                 # 工作规范与工具使用约定
│   │   ├── SOUL.md                   # 角色、风格与行为边界
│   │   └── IDENTITY.md               # 名称与展示身份
│   ├── users/<user-id>/USER.md        # 用户主动维护的偏好与背景
│   ├── skills/<skill-name>/SKILL.md   # 用户配置的本地 Skill
│   └── credentials/                 # 凭证引用，不能进入上下文视图
├── memory/domains/<domain-id>/       # 按现有范围保存学习结果
│   ├── entries/                     # 事实、学习后的偏好、决策及版本
│   ├── skills/                      # 自动沉淀的 Skill 及版本
│   ├── policies/                    # 禁用、恢复、纠正记录
│   ├── current.json                 # 当前生效版本清单
│   ├── scope.json                   # 范围映射
│   └── drafts/                      # 待校验修改
├── channels/<channel>/
│   ├── state/                       # 任务、会话、证据与投递状态
│   ├── workspace/                   # 当前渠道默认任务目录
│   ├── native/                      # 原生引擎会话与配置状态
│   ├── logs/
│   └── dws/                         # 仅钉钉启用 DWS 时使用
├── datasource/plugins/<plugin>/     # 数据源插件运行数据
│   ├── sources/<source-id>/         # 来源实例的持久数据与快照（按需创建）
│   └── ...                         # 插件自己的令牌、账本等
├── generated/agents/<agent-id>/     # 系统生成，按 Agent 与任务区分
│   └── workspaces/<context-id>/     # 装配后的指令、USER/MEMORY 和原生 Skill 包
├── cache/memory/                    # 可重建检索索引
├── cli-state/                       # 终端运行状态
└── backups/                         # 停机迁移与恢复备份
```

**沉淀的 Skill 放在 `memory/domains/<domain-id>/skills/`。**
具体正文在 `<skill-id>/revisions/<revision>/SKILL.md`，Host 展示名称、来源和当前版本。
用户自己配置的 Skill 放 `config/skills/`；最终给原生引擎使用的包在 `generated/agents/<agent-id>/workspaces/`。
三者分别是学习结果、配置输入和可重建装配产物。

`agent-id` 对应 `config/agents.json` 中用户选择的角色，例如 `default`、`business`。
`config/agents/<agent-id>/` 是这些角色的可选手写指令目录，系统不会自动创建或
修改其中的角色正文。只有默认 Agent 时只需维护一份，也可以不提供可选文件。
系统装配后的 `AGENTS.md`、`SOUL.md`、`IDENTITY.md`、`USER.md`、`MEMORY.md`
与 `skills/` 全部位于 `generated/agents/<agent-id>/workspaces/<context-id>/`。

外部资料由数据源插件提供；其留存快照放
`datasource/plugins/<plugin>/sources/<source-id>/snapshots/`。
快照是有来源的持久资料，不能像缓存一样删除。已部署的旧 `knowledge/` 在实施时
核对来源与授权后迁移；本次设计不搬动实际文件。

项目任务继续使用项目真实目录。渠道默认 workspace 不存账号状态或一整套私人记忆；
Host 上下文视图独立存放，不把 `USER.md` 或 Skill 复制进项目代码库。

## workspace 中的文件

| 文件 | 含义与加载方式 |
| --- | --- |
| `AGENTS.md` | 配置选中的 Agent 工作规范，包含 Tools 章节；每次任务按版本装配 |
| `SOUL.md` | Agent 角色与回复风格，可选；不会被任务经验自动改写 |
| `IDENTITY.md` | 展示身份，可选；真实平台身份与授权由配置和渠道决定 |
| `USER.md` | 当前允许使用的用户资料与偏好；首期只在 owner 私人任务加载 |
| `MEMORY.md` | 此任务召回的事实和决策摘要，带来源与版本；不是全量记忆库 |
| `skills/` | 当前任务有权使用的 Skill；先提供说明，再按需加载正文 |
| `manifest.json` | Host 保存装配来源、范围和版本，供检查与恢复 |

`config/users/<user-id>/USER.md` 是用户维护的原文，自动学到的偏好另存记忆条目。
workspace 的 `USER.md` 是两者经过筛选后的视图。当前请求优先于默认偏好，
用户维护的明确设置优先于学习到的偏好；冲突有记录，旧偏好不能悄悄覆盖新设置。
首期不在群聊装配个人 USER 文件，也不从显示名称自动关联不同平台的用户。

workspace 的 `MEMORY.md`、`USER.md` 和 `skills/` 每次任务由 Host 生成，
用户通过原文或 Host 的记忆修改入口维护内容。Host 不把这些生成文件写回 `config/`。
generated 中的文件只是任务快照，修改它们不能成为新记忆或配置。
视图中出现某份文件不代表拥有额外工具或操作权限。

## 本期取舍与实施顺序

先实现目录、指令与 owner `USER.md` 配置，再结合文件记忆后端生成任务视图，
最后接入 Skill 原生装配和版本变化后的会话刷新。全部复用文件记忆方案的迁移门槛。
现有 `config/CLAUDE.md` 与 Agent 的 `instructions_file` 显式迁移，不自动重复加载。

首期不加载 `BOOT.md`、`BOOTSTRAP.md` 或 `HEARTBEAT.md` 触发自动任务。
不新增每日自动日记；需要日记时再定义独立、有来源的记录机制。
已有项目与原生引擎自己的指令和手写 Skill 保留各自机制。

交付时验证：owner 私聊能使用偏好；群聊没有私人 USER 内容；项目文件未被污染；
Skill 与事实更新后下一次任务使用新版本；清理无活动引用的 generated 或 cache
都能重建，删除 memory 则属于
知识数据丢失，必须用备份恢复。本设计阶段仅修改文档。
