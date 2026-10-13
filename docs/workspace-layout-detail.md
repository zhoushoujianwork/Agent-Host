# Host 目录与 workspace 文件约定：实现详细稿

配套[主文档](workspace-layout.md)与[文件记忆详细稿](file-memory-design-detail.md)。
所有新增目录、文件和加载行为仍为提案；主文档约定范围。

## 参考与差异

核对日期：2026-10-13。[OpenClaw 官方 workspace 文档](https://docs.openclaw.ai/concepts/agent-workspace)
区分 AGENTS 工作规范、SOUL 风格、IDENTITY 展示身份、USER 偏好、MEMORY 长期摘要，
以及 workspace skills；同时将配置、凭证与会话状态放在 workspace 之外。
当前 Tools 说明在 AGENTS 的 Tools 章节，而非新增 TOOLS 文件。

[OpenClaw SOUL 模板](https://docs.openclaw.ai/reference/templates/SOUL)允许 Agent
逐步更新该文件并通知用户。Host 本期选择用户维护角色原文，自动成长仅发生在
知识和 Skill 层；用户显式要求修改配置时才执行角色管理操作。

## 默认角色与模板复制

仓库的 `config/agent-templates/default/` 和 `operations/` 提供可复制原文。
用户初始化 default 或复制 operations 为 ops 后，原文归用户维护；运行期不会
同步模板更新或自动创建额外角色。模板目录不是启用配置扫描入口。
沿用 agents.json 的 default_agent、agents 列表和已有 group_bindings，复制后必须
注册 ID/name、项目和能力选择，才能被路由选中。改变默认角色只影响之后的任务，
旧角色记忆不因改名或复制模板而合并，新角色保留独立身份。

目前加载器没有多文件指令目录字段，AGENTS/SOUL/IDENTITY 联合装配仍待实现。
现有 instructions_file 可显式引用 AGENTS 原文；这种使用仅加载该单文件，不能
声称其他模板文件已经生效。默认身份已支持，模板初始化入口仍是后续实现。

Host 采用这些名称，但将用户配置、版本化学习结果、任务装配视图分开保存。
本方案不是 OpenClaw workspace 兼容实现，也不照搬其启动、日记或注入预算。
原生 Claude/Codex 是否自动识别这些名字不能作为加载依据，由适配器显式装配。

## workspace 与执行 cwd

Host 的 task workspace 由两个部分组成：

- 执行 cwd：注册项目的真实目录，或当前渠道/Agent 已配置的默认工作目录。
- 上下文视图：`cache/workspaces/<context-id>/` 中的私有、不可变本次任务快照。

context-id 由 Host 创建并绑定 run，不从平台消息文本生成路径。
manifest 记录 Agent、渠道、用户绑定、audience、项目/默认 scope、记忆快照、
输入指令摘要及 native 装配器版本。相同角色和项目、不同 audience 必须分开。
多项目任务只合并本次明确允许的资料，不从 cwd 扩大知识范围。

渠道默认工作目录暂不搬动。Agent 的既有 workspace override 保留，项目 cwd
也不写 Host 的 USER、MEMORY、AGENTS 或生成 Skill。上下文视图不等于沙箱；
已授权 native 文件能力仍受现有执行政策管理，不宣称此目录布局阻止 OS 访问。
旧布局的凭证、DWS 数据、数据库和 native 历史不复制到视图。

## 配置输入与单一维护入口

Host 运行时只读 config。提炼、发布、索引、快照留存和上下文装配均不得向
config 写入生成内容；缺少必要配置时报告错误，不通过自动生成配置继续运行。
配置模板、初始化或迁移只通过用户明确执行的管理操作写入，不属于运行期学习。
系统提议的配置修改留在所属渠道/CLI 运行状态目录的 `management-drafts/`，
不自动改写用户原文；只有用户明确应用后才成为配置输入。

沿用 `config/host.json` 的命名执行配置和 `config/agents.json` 的 Agent 选择。
后续增加 Agent 指令目录、本地 Skill 引用，以及明确的 owner 用户资料绑定。
路径相对 config 解析，文件必须通过格式、范围和大小校验；具体字段实现时确定。
不按目录名自动启用全部 Agent 或 Skill，不改变 channels/plugin 的启用规则。

`config/agents/<agent-id>/` 的 AGENTS、SOUL、IDENTITY 都是用户维护的配置输入。
Agent 的已有 role 仍是路由选择与默认角色文本，文件补充不同维度的内容。
重复引用同一来源时明确报配置错误；语义冲突按来源优先关系处理并保留原因，
不承诺启动时可以识别所有自然语言矛盾。学习任务不会自动改写这些文件。
工具说明写 AGENTS Tools 章节，内容只能引用本次已装配能力。
插件禁用后，其配置说明不保留为“工具可用”的陈述；指令不能启用插件。

`config/users/owner/USER.md` 首期仅用于已有可信 owner 身份。引用与渠道绑定需
显式配置；同一人工用户文件可供指定私人渠道使用，但不合并这些渠道的学习域。
首期不根据平台昵称或 USER 中的自述跨平台关联用户，也不加载群成员私人资料。
未来 per-person profile 仍遵循独立设计与验证，不能因该目录存在就宣称已支持。

用户文件适合写沟通方式、长期偏好与必要背景；不放凭证、工具许可或任务授权。
自动学到的偏好保留在相应 memory domain 的版本化 entries，带陈述与时间依据。
用户修改 USER 文件是显式配置更新，自动沉淀不写回该原文；二者的来源可以区分。
自动生成的用户资料摘要只出现在任务视图，不能保存到 config/users。

USER 原文的示例结构如下，仅用于展示，不是已生成的实际用户档案：

```markdown
# User preferences

## Active preferences
- 2026-10-13: 默认用中文回答；保留产品名称和代码标识符原文。
- 2026-10-13: 先说明结果，再提供必要细节。

## Background
- 只记录用户主动提供且仍适用的背景信息。
```

背景陈述作为资料，不包装成工具授权。明确撤回的设置移入历史记录或删除原文，
生成新视图时排除旧项；学习偏好中有冲突的旧记录同时标记替代，避免原文移除后
又回落到已撤回的旧偏好。停用用户资料绑定时不装配该配置或相应私人偏好。

`config/skills/<name>/` 是显式选择的本地手写 Skill 包，首期使用指令与引用。
自动沉淀包采用 Host ID，挂载时使用 Host 命名空间避免撞名；不会覆盖 native
用户/项目手写 Skill。外部原生插件的安装与管理不因此成为 Host 已交付能力。

## 装配内容、优先关系与预算

Host 在任务启动前重新检查指令摘要、用户配置和记忆 current 快照，再生成视图：

1. AGENTS/SOUL/IDENTITY 来自所选 Agent 配置；无可选文件就省略，不虚构身份。
2. USER 只在 owner 私人任务中生成：已配置资料与当前适用学习偏好的有界集合。
3. MEMORY 收集适用事实、决策、约束的有界摘要，保留 ID/revision/来源，按数据装配。
4. skills 只含此任务授权且适用的包；知识来源与 native 挂载引用写入 manifest。

当前明确请求覆盖普通风格与偏好默认值；用户主动维护的明确设置覆盖学习偏好。
Host 配置授权、运行政策和原生权限始终由相应实现检查，任何 Markdown 不能变更。
冲突处理逐项保留来源与原因，不通过把所有文件拼接成一个强指令来解决。
项目自身的 AGENTS/CLAUDE 与原生用户指令遵循 harness 既有行为；有实际冲突时
记录并明确处理，不宣称 Host 可以重排 native system/user 的全部优先关系。

配置指令使用单独预算，超限明确报配置错误。USER、MEMORY 和 Skill 描述使用
独立与总量预算；返回摘要及截断状态，正文可按需查询。默认值在实现时以现有
8 项/12 KiB 记忆预算为起点评估，不能照搬 OpenClaw 数值当作本项目交付能力。
群聊只可接收适用的群域知识；不会通过共享 USER 或 owner MEMORY 摘要获得私人内容。

USER/MEMORY 是派生视图，没有第二条自动保存通道。修改需要回到配置源或记忆
条目修改入口；原生执行结果只进入正常的提炼/选择流程。manifest 与正文摘要
必须核对，缓存被修改就撤销使用并报告，而非将修改当作用户授权。

## Skill 权威路径与原生交付

学习后的 Skill 完整路径：

```text
memory/domains/<domain-id>/skills/<skill-id>/revisions/<revision>/
├── record.json
└── SKILL.md
```

域的 current 清单决定有效版本。可通过 Host 按名称、主题与来源查看；文件路径
中的 ID 不要求用户记住。来源证据留在运行库/源文件，用包含渠道、run 和证据 ID
的 locator 查询，避免两个渠道 ID 相同导致引用串库。

视图的 `skills/<native-name>/SKILL.md` 是有效正文的派生复制，保持 ID/revision
与摘要映射。最终原生插件包装依 Claude 安装版本验证，辅助包装放同一 cache
视图内，不在全局配置或项目 `.claude/skills/` 下安装。
不会将所有域的 Skill 软链接到 workspace；有引用文件时只复制已验证允许范围
的内容，并阻止路径逃逸。缺引用或能力不足明确失败，不使用其他范围的替代包。

## 数据源资料与生成目录

取消新布局的顶层 knowledge 目录。项目文档和用户外部笔记仍以配置的 root/path
作为原始来源；普通读取不复制到 Host。需要持久留存的快照由插件按来源实例归档：

```text
datasource/plugins/<plugin>/sources/<source-id>/snapshots/<snapshot-id>/
├── manifest.json                    # 原始定位、时间、摘要和可见范围
└── documents/                       # 留存原文
```

这是持久资料，不是派生缓存。插件令牌与 Email 账本沿用已交付插件目录，无需
仅为统一树形结构而迁移它们。插件内部可重建缓存和临时处理文件按实现区分，
不能用统一清理操作删除 snapshots。未启用插件不自动创建这些目录。
没有对应来源配置的系统输入保留为渠道运行证据，不凭空创建数据源或写配置。

| 生成内容 | 写入位置 | 是否可重建或删除 |
| --- | --- | --- |
| 事实、偏好、决策、Skill 及版本策略 | memory/domains | 权威学习结果，需备份 |
| 学习内容的修改草案 | memory/domains/<domain-id>/drafts | 尚未发布，不能注入 |
| 系统提议的配置修改 | 渠道/CLI 运行状态目录的 management-drafts | 用户明确应用后才更新配置 |
| 数据源快照与清单 | datasource/plugins/<plugin>/sources | 持久来源资料，需备份 |
| 检索索引、USER/MEMORY 视图、原生 Skill 包 | cache | 从配置与发布记忆重建 |
| 任务产物、执行证据、日志与会话 | 工作目录及渠道/CLI 运行目录 | 按各自生命周期保留 |

学习条目引用快照时记录插件、source-id、snapshot-id 与正文摘要。不能因为把
资料移到 datasource 就取消 owner 限制，也不能因为资料被检索到就发布为 Skill。

## 更新、清理与会话

配置更改下次服务启动生效，沿用现有无热加载规则；本期不新增后台监视器。
文件记忆更新通过协调器发布，下次任务读取最新快照。
指令/USER/Skill/关键记忆摘要影响 native 会话兼容性；变化时新建兼容会话，
保留授权连续上下文，撤回的正文不能混在 handoff 中。
首次新建或恢复会话均显式装配，而非依赖原生 cwd 的自动文件发现。

活动任务视图保持不可变，发布修改不能改变已启动任务的输入。
任务结束后记录输入摘要和来源版本到运行证据，再释放视图；清理只能删除无活动
引用的快照。cache 是可重建数据，清理它不能删除学习结果、配置或历史证据。
重建旧快照仅供历史检查，不能让过期 Skill 再次进入新任务。

## 迁移与验证

先登记现有 `config/CLAUDE.md`、Agent `instructions_file`、role 与 workspace。
迁移时由明确映射决定哪些文字进入 Agent AGENTS 或执行配置的共享指令，
保留原文备份；同一来源不能同时作为旧指令和新 AGENTS 重复注入。
原生 provider/profile 的配置不因迁移指令而被替换。用户资料未知时不生成 USER。
无需对实际项目代码目录迁移或复制。随文件记忆切换停机、备份、核验与回退。

旧 knowledge 下的快照先按已配置来源映射到 local-documents 等插件，保留正文
校验值、旧来源定位与授权。更新 root 引用属于显式迁移管理动作，不能由运行期
检索自动改写配置。没有可靠映射的资料保持原位并显示待处理，不猜测归属。
完成引用与检索验证后才清理旧目录；回退同时恢复配置路径与资料位置。

| 场景 | 必须验证 |
| --- | --- |
| owner 默认任务和项目任务 | 正确角色/偏好；cwd 保留；视图与来源可检查 |
| 同 Agent 从私聊进入群聊 | 无私人 USER/MEMORY/Skill；不同快照与会话 |
| 两渠道同名 Agent | 配置绑定可复用；学习域不自动合并；来源 locator 不混淆 |
| 配置与学习偏好冲突 | 显式来源优先，当前请求可覆盖普通偏好 |
| 指令或 Skill 更新/禁用 | 后续任务刷新，旧内容不经 native history/handoff 回流 |
| 缓存损坏与清理 | 摘要错误可见；删除后重建；活动任务不被清理 |
| 原生加载 | 提供/发现/使用分别记录；手写 Skill 无覆盖；不增额外目录权限 |

本阶段先交付设计，不触发自动日记、开机任务、定时学习、外发消息或生产迁移。
