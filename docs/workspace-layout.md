# Host 目录与 workspace 文件约定

状态：2026-10-13 本期已实现并完成本地迁移。
2026-10-14 新增共享工程登记与执行回执；worktree/PR/MR 生命周期仍待实现。
配套[文件记忆方案](file-memory-design.md)；实现细节见[详细稿](workspace-layout-detail.md)。
沿用名称 `.agent-host/`。

## 配置、学习结果与任务副本

用户定义角色和配置，Host 学习事实与处理方法；每次任务再装配需要的内容。
`config/` 运行期只读，生成内容进入 `memory/` 或 `cache/`，外部资料归数据源。

```text
.agent-host/
├── config/
│   ├── host.json、agents.json、channels.*.json、datasource.plugins.*.json
│   ├── agents/default/              # 用户维护 AGENTS、SOUL、IDENTITY
│   ├── agents/ops/                  # 用户复制模板后注册的运维角色
│   ├── users/owner/USER.md           # 显式绑定的私人资料，可选
│   ├── skills/                      # 手写 Skill 保留位置；新挂载入口待实现
│   └── credentials/
├── memory/
│   ├── sources/<source-key>/         # current 发布权威、发布日志和应用回执
│   └── domains/<domain-id>/          # 范围信息、查看投影、entries 和 skills
├── cache/
│   ├── memory/<source-key>/index.db  # 可重建 SQLite 检索索引
│   └── workspaces/<run-id>/          # 本次上下文与 native-skills 包
├── channels/<channel>/              # state、workspace、native、logs；钉钉可含 dws
├── datasource/plugins/<plugin>/     # 插件运行数据及来源持久快照
├── projects/                        # 共享工程登记与执行回执；源码保持原位
├── cli-state/
├── backup-state/                    # Automatic backup lock and health receipt
└── backups/                         # Automatic archives and historical rollback data
```

Automatic `.tar.gz.age` archives and their checkpoint index use `backups/`
directly; no `workspace/` subdirectory is required. Backup storage is excluded
from exports. See the [backup and recovery guide](workspace-backup-design.md).

**学习后的 Skill 放在 `memory/domains/<domain-id>/skills/<id>/revisions/<revision>/SKILL.md`。**
正式事实、偏好和决策放同域的 `entries/`。当前版本由来源发布清单决定，
域的 `current.json` 只是便于查看的投影。禁用与纠正保留为历史版本。

顶层 `knowledge/` 已退出本地运行布局。owner_notes 的 14 份原文已迁入
local-documents 插件快照，摘要及原授权保持一致。数据源读取不会自动把资料变成记忆。

## Proposed: multiple users and external knowledge

Design agreed on 2026-10-15; not yet implemented. Current profile loading remains
limited to an explicitly bound trusted owner. This proposal does not enable the
unfinished Confluence memory backend: durable memory stays local.

AGH identifies the person independently of the selected Agent. Each person has
one Host user ID and a profile at `config/users/<user-id>/USER.md`; Agent role
instructions stay under `config/agents/<agent-id>/`. Hobbies, background and
explicit communication preferences belong in the user profile. Several Agents
may serve the same person without copying that profile into each Agent.

| Record | Where it belongs | How it is used |
| --- | --- | --- |
| Channel account to Host user mapping | Host-managed identity records; exact storage to be decided | Resolve the sender before selecting private context |
| Explicit user profile | `config/users/<user-id>/USER.md` | Load only for that identified user in an allowed private task |
| Learned facts, preferences, decisions and procedures | Local versioned `memory/` | Recall by authorized user, group or project scope and existing Agent boundaries |
| Detailed findings, verified experience and task reports | Confluence pages/blogs, when authorized | Search/read through MCP as needed; retain source-task links |
| Retrieved source copies and task context | Rebuildable cache and task views | Keep source/version metadata; never become memory merely by being cached |

Use a stable platform account ID within its channel/tenant namespace, never a
display name, to identify a sender. Link accounts across channels only through a
verified binding. Unresolved identities receive no existing private profile;
they can still be answered using context allowed for the current conversation.
A statement about another person is a sourced claim, not a confirmed profile edit.

Private user memory, group knowledge and project/team knowledge have distinct
visibility. Being in the same group or using the same Agent does not merge users.
Group tasks exclude private USER content by default. Host enforces these rules
before retrieval, context assembly and publication; Markdown cannot grant access.

External knowledge is retrieved on demand. Do not preload all Confluence posts or
automatically re-import AGH-authored posts into memory. Keep a short local lesson
or reference only after deliberate selection based on evidence and usefulness;
publishing a report is not independent confirmation of its claims. A shared
Confluence credential does not grant every AGH user access to its full results.

Implementation order: first identity resolution and private-user isolation, then
explicit profile management and scoped learning, then permission-aware external
retrieval and bounded caching. Keep existing owner bindings until an explicit,
verified migration maps them; do not infer owners of historical records.
Acceptance requires separate private conversations for two people, safe group
context, verified account linking, profile corrections and source traceability.
See the [detailed recording rules](workspace-layout-detail.md#proposed-multi-user-recording-and-retrieval).

## 默认助手与运维模板

默认角色是 `default`。用户可以复制[运维模板](../config/agent-templates/README.md)
为 `config/agents/ops/`，修改后在 `agents.json` 注册；复制目录本身不启用角色。

`instructions_dir: "agents/default"` 显式加载 AGENTS/SOUL/IDENTITY。
可选 `user_file: "users/owner/USER.md"` 只向可信 owner 的私人任务提供资料，
群聊和公开会议排除 USER。配置修改后重启生效；运行期学习不修改这些原文。

本地默认角色已保留原 CLAUDE.md 全文并完成多文件配置迁移，没有自动建立私人 USER。
模板后续更新不会覆盖用户已经修改的配置。

## workspace 中的文件

任务仍在注册项目真实目录或渠道默认 workspace 执行。
Host 不把配置文件和生成 Skill 写进项目代码库。

| 缓存视图中的文件 | 内容 |
| --- | --- |
| AGENTS/SOUL/IDENTITY | 所选角色的配置副本 |
| USER.md | 当前私人任务的显式用户资料和适用学习偏好 |
| MEMORY.md | 当前任务召回的知识、版本和来源 |
| manifest.json | 范围、实际 cwd、输入文件摘要与记忆引用 |
| native-skills/skills/<name>/SKILL.md | 当前允许的原生 Skill 副本 |

这些文件是本次任务快照，不是另一套正式记忆。当前请求可覆盖普通回复默认值；
显式用户配置优先于冲突的学习偏好，任何文档都不能授予工具或扩大资料范围。
相同 Agent 进入群聊，不会携带 owner 私人 USER、MEMORY 或 Skill。

角色、用户资料或正式记忆变化影响下一次任务的会话选择；活动任务保留其输入快照。
缓存可重建，清理时应跳过活动任务。尚未提供自动缓存清理器、记忆编辑草稿导入、
手写 config/skills 新挂载入口或自动日记。

## Shared engineering records and future development workspace

[Engineering records](development-projects.md) now automatically enroll selected
local Git targets through existing CLI/channel routing. Bounded lookup prefers
existing `~/github`, `~/gitlab` and current-workspace checkouts; code stays in place
and no enable flag/manual registration is required. `projects/state.db` retains
shared project records and source-linked `tasks/<run-id>/` execution receipts.
Their generated JSON/Markdown views omit original conversation text and context;
Agent/audience evidence, native conversations and private memory stay separate.

Managed clones/new repositories under `projects/<project-id>/repository/`,
multi-run feature tasks, owned worktrees and PR/MR delivery remain proposed.
The current-worktree execution cwd remains separate from the registry primary
checkout. Shared engineering records are durable runtime material, not context
cache; ordinary cache cleanup must not remove them.
