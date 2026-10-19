# Host 目录与 workspace 文件约定：实现详细稿

配套[主文档](workspace-layout.md)与[文件记忆详细稿](file-memory-design-detail.md)。
主文档约定范围。显式多文件配置和任务缓存视图已实现并完成本地迁移；
默认角色保留原指令全文，私人 USER 未自动绑定。验证见文件记忆方案。

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

加载器新增 `instructions_dir`，按 AGENTS、SOUL、IDENTITY 顺序读取已有文件，
总正文限 32 KiB，至少存在一份；文件目录缺失、超限、格式或权限错误显式失败。
路径相对 `agents.json` 所在目录解析；目录要求 0700、正文文件要求 0600、有效
UTF-8，且不得通过软链接引用。现有绝对路径 `instructions_file` 保留，二者
互斥以免重复注入。默认身份仍为 default；模板复制是用户显式管理操作。

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
自动学习不会提议并写入配置。用户显式执行配置管理任务才修改原文；
独立 management-drafts 工作流尚未交付。

沿用 `config/host.json` 的命名执行配置和 `config/agents.json` 的 Agent 选择。
Agent 已增加指令目录和显式 owner 用户资料绑定；手写 Skill 引用另行实施。
`instructions_dir` 和 `user_file` 相对配置目录解析，绝对路径也必须通过同样的
格式与权限校验。`user_file` 必须指向 USER.md，正文限 16 KiB。
不按目录名自动启用全部 Agent 或 Skill，不改变 channels/plugin 的启用规则。

`config/agents/<agent-id>/` 的 AGENTS、SOUL、IDENTITY 都是用户维护的配置输入。
加载器按固定顺序组合已配置角色正文，附带不授予能力或改变 audience 的边界提示。
instructions_file 与 instructions_dir 互斥，USER 不得同时被选作 Agent 正文。
当前不自动识别、解决或逐项记录角色文件的自然语言冲突；用户维护其一致性。
学习不会改写这些原文，指令也不能启用插件。插件禁用不会自动删改 AGENTS 中的
工具说明，用户应同步维护；实际工具是否装配由 Host 配置决定。

`config/users/owner/USER.md` 首期仅用于已有可信 owner 身份。引用与渠道绑定需
在所选 Agent 的 `user_file` 显式配置；该配置仅对可信 owner 的私人任务装配。
同一人工用户文件可供指定私人渠道使用，但不合并这些渠道的学习域。
首期不根据平台昵称或 USER 中的自述跨平台关联用户，也不加载群成员私人资料。
The optional users.json registry now enables per-person profiles as described below;
without it, this legacy owner-only binding remains unchanged.

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

背景陈述作为资料，不包装成工具授权。删除 USER 中的设置或移除 user_file 后，
重启会排除相应配置资料，但不会自动修改已学习的偏好；旧学习记录仍可能召回。
若同时撤回已沉淀的偏好，需要显式 memory invalidate。当前通过 prompt 提示
显式配置优先，不自动建立配置设置与学习条目之间的替代关系。

`config/skills/<name>/` 保留为用户维护的手写 Skill 位置；本期尚未增加 Host
对该目录的新选择和挂载入口，不能因为文件存在就宣称已生效。
自动沉淀包采用 Host ID，挂载时使用 Host 命名空间避免撞名；不会覆盖 native
用户/项目手写 Skill。外部原生插件的安装与管理不因此成为 Host 已交付能力。

## 装配内容、优先关系与预算

角色和 USER 在启动时加载；任务使用这份已加载配置并核对正式记忆快照，再生成视图：

1. AGENTS/SOUL/IDENTITY 来自所选 Agent 配置；无可选文件就省略，不虚构身份。
2. USER 在已识别用户的私人任务中生成：已配置资料与当前适用学习偏好的有界集合。
3. MEMORY 收集适用事实、决策、约束的有界摘要，保留 ID/revision/来源，按数据装配。
4. skills 只含此任务授权且适用的包；知识来源与 native 挂载引用写入 manifest。

当前明确请求覆盖普通风格与偏好默认值；用户主动维护的明确设置覆盖学习偏好。
Host 配置授权、运行政策和原生权限始终由相应实现检查，任何 Markdown 不能变更。
这些优先关系通过 prompt 表达，当前不提供自动的自然语言冲突识别或解决记录。
项目自身 AGENTS/CLAUDE 与原生用户指令遵循 harness 既有行为，
Host 不能重排 native system/user 的全部优先关系。

当前预算为角色正文合计 32 KiB、显式 USER 正文 16 KiB、最多 8 项/12 KiB
记忆正文；原生包最多 8 个 Skill、单个 16 KiB、合计 64 KiB。配置与装配超限
明确失败，不静默截断角色正文。Native Skill 描述有界，完整正文按原生 Skill 调用加载。
群聊只装配同 audience 的适用知识，不装配私人 USER；它不是 OS 文件隔离边界。

任务视图使用 Host 创建的 run ID，目录只发布一次，不覆盖已有视图。manifest
保存 Agent、audience、scope/project、执行 cwd、配置原文摘要及已召回记忆的
ID/revision/source-run/evidence-ID。USER 包含显式配置和本次私人用户学习偏好，
MEMORY 使用归属明确的数据封装；群域不生成 USER。生成前再次检查记忆范围、
生效状态和到期时间，沿用 8 项/12 KiB 记忆正文预算。
提供配置文件与实际注入分别可观察；这些文件不依靠 cwd 自动发现来生效。

USER/MEMORY 是派生视图，没有第二条自动保存通道。用户配置通过原文维护后重启；
正式记忆可通过已有禁用/恢复操作管理，正文编辑导入尚未交付。
原生执行结果只进入正常的提炼/选择流程。manifest 与正文摘要
必须核对，缓存被修改就撤销使用并报告，而非将修改当作用户授权。

## Skill 权威路径与原生交付

学习后的 Skill 完整路径：

```text
memory/domains/<domain-id>/skills/<skill-id>/revisions/<revision>/
├── record.json
└── SKILL.md
```

来源 `memory/sources/<source-key>/current.json` 是唯一原子发布权威，
域 current 只是便于查看的投影。可通过 Host 按名称、主题与来源查看；文件路径
中的 ID 不要求用户记住。来源证据留在运行库/源文件，用包含渠道、run 和证据 ID
的 locator 查询，避免两个渠道 ID 相同导致引用串库。

视图的 `native-skills/skills/<native-name>/SKILL.md` 是有效正文的派生复制，保持 ID/revision
与摘要映射。原生插件包装已通过安装版本的真实调用验证，包装放同一 cache
视图的 native-skills 子目录内，不在全局配置或项目 `.claude/skills/` 下安装。
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
| 事实、偏好、决策、Skill 及状态版本 | memory/domains 与 memory/sources 发布清单 | 权威学习结果，需备份 |
| 学习内容的修改草案 | 后续编辑流程范围 | 尚未交付 |
| 系统提议的配置修改 | 后续配置管理流程范围 | 尚未交付；自动学习不写配置 |
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

本期实现显式配置与任务装配，不增加自动日记、开机任务、定时学习或外发消息。
本地停机迁移、双渠道重新连接与历史对照已完成，详见
[文件记忆验证](file-memory-design-detail.md#已完成验证与边界)。

## Multi-user recording and retrieval

This section implements the [main design](workspace-layout.md#multiple-users-and-external-knowledge).
The optional `users.json` registry is loaded beside the selected `agents.json` at
startup. Without that registry, the existing owner-only behavior is preserved.
Its exact version-1 shape is shown in [the example](../config/users.example.json).

### Identity, profiles and ownership

Each user has a safe `id`, optional `accounts` and optional `knowledge_sources`.
An account contains exact `channel` (`dingtalk` or `feishu`), `account_id` (bot/app),
`tenant_id` and `sender_id` (staff ID or Open ID). Duplicate bindings, unsafe IDs,
unknown fields and malformed private files fail explicitly. Linking accounts is a
trusted local operator action after verifying both belong to the person; the Host
does not verify that ownership remotely or accept identity links from chat text.

A new authenticated account without a configured binding gets a deterministic
`u-<32 hex digits>` ID derived from all four account fields. It gets no owner
profile or owner-private MCPs. Account observations publish immutable, synced
receipts at `users/<user-id>/accounts/<account-sha256>.json`; concurrent workers
publish complete records without overwriting other observations. These are durable
Host records and enter encrypted backups; unfinished receipt files are excluded.
Channel receipts retain source message and time evidence. `users list` distinguishes
configured bindings from historical observations and never prints profile text.

The legacy owner uses audience `owner`. Other private users use `user:<user-id>`;
groups and meetings keep their existing shared audience identities. Existing
Agent, audience, source and project partitions still constrain memory, history,
Skills and native sessions. A verified cross-channel profile binding does not
merge separate source stores or import old memory. Account rebinding makes a new
future partition; incompatible queued audience bindings fail without task replay.
The configured channel owner cannot be rebound to another person or acquired by
a non-owner account through a user alias.

Only registered user IDs can load conventional `config/users/<id>/USER.md` files.
A missing configured file uses the Host-created default described below; loading
never copies another person’s profile.
Existing profile files use the same 16 KiB, UTF-8, private ownership and no-symlink
checks as legacy owner profiles. Agent `user_file` remains an owner-only fallback;
a registered owner profile overrides it. Profiles are startup snapshots, and their
content participates in runtime compatibility so edits after restart rotate native
sessions. All private channel users currently use `bypassPermissions` with
Owner-equivalent execution; owner approval gating is paused. Only managed native
settings are copied to their separate profile directories, not owner instructions
or conversation history. Directory
layout and prompt policy do not provide OS filesystem sandboxing.

Task manifests record the private `user_id`; group or meeting provenance rejects
USER injection even if a caller supplies an audience resembling a private user.
Learned preferences in a private task can appear in its derived USER view, but
normal learning still publishes only versioned local memory. A statement about
another person remains attributed to the source speaker/task; it cannot silently
edit that person’s explicit profile.

### Explicit profile corrections

An operator edits the intended person’s USER.md, checks configuration and restarts
the service. To withdraw a previously learned preference, also use the existing
`memory invalidate --agent AGENT --audience user:ID --home CHANNEL_STATE
--reason REASON MEMORY_ID` path for each conflicting record in each affected
source. Use audience `owner` for legacy owner records. This preserves historical
evidence and excludes the record from recall; the associated memory generation
and explicit profile change invalidate compatible native-session reuse.
Automatic identification of natural-language conflicts, runtime profile-edit
MCPs and hot reload are not implemented. Deleting profile text alone is not a
request to delete independently learned records or old execution evidence.

### External retrieval and grants

`knowledge_sources` authorizes exact names such as `confluence:team-notes`.
`group_knowledge_sources` authorizes the shared conversations in multi-user mode.
Both default to no sources for non-owner/shared audiences. The owner retains
existing source access; existing installations without users.json retain their
legacy source policy. New grants intersect existing source selection, filtering
before credential resolution. Missing/ungranted sources do not grant access to
another source. Normal source pinning, fingerprints and execution-time validation
still apply. Email, DWS and owner-context tools remain owner-only.

A grant authorizes the entire configured source reachable through its credential.
There is no per-person Confluence impersonation or additional remote page ACL.
Operators must grant only sources whose complete reachable contents are suitable
for the recipient, using appropriately scoped remote credentials. Tools keep their
existing explicit write behavior; profiles, retrieval and generated reports do not
authorize automatic publication.

No full external collection or AGH-authored blog is automatically injected or
imported. Existing on-demand MCP search/read/write and source receipts remain.
Optional version-aware external-page caching is not implemented. Future caches
must retain identity/source/version boundaries and remain distinct from durable
evidence snapshots. Useful selected local lessons may cite a report, but the
report cannot count as independent confirmation of the originating task.

### Validation and remaining scope

Automated tests cover two private users with independent profiles/native paths,
unknown account stability across reloads, explicit cross-channel account binding,
owner compatibility, forged-audience rejection, duplicate/unsafe profile rejection,
group exclusion, source grants before credentials, local inspection, concurrent
identity receipts and encrypted backup/restore. Memory isolation, fresh-session
recall and explicit correction/invalidation are exercised with file and SQLite
backends. Both channel parsers test opt-in private intake and foreign-tenant denial.

Live multi-person DingTalk/Feishu round trips, broad-scale identity lookup and
remote source-permission audits remain unverified. Automatic account merging,
profile-management MCPs, automatic preference-conflict matching, shared-project
cross-user memory publication and external-page cache lifecycle remain outside
this increment. No historical owner data is migrated or deleted.

## Automatic first-contact profile

When `users.json` enables multi-user intake, verified DingTalk/Feishu selection
resolves a stable Host ID, initializes `users/<id>/USER.md`, then records the
account receipt. Unknown accounts use the existing application/tenant/sender
digest; configured owners retain `owner`. Chat names and text do not select paths.

The template contains only the Host ID, empty background/interests/preferences
sections and a statement that profiles cannot grant permissions. A private
temporary file is synced and atomically linked without replacing an existing
profile. Existing files must pass private regular-file and size checks; linked
profiles fail explicitly. Repeat/concurrent messages preserve operator edits.
Configuration stays read-only. Explicit configured profiles override the runtime
default. The default is read into a bounded per-task snapshot; groups/meetings
never receive it. `users list` reports the effective profile path without text.

Regression checks cover both channel identities, owner/new-user creation,
concurrent publication, configured override, edit preservation, group exclusion,
symlink rejection and unchanged configuration. Multi-person live acceptance
remains separate from owner-account live checks.
