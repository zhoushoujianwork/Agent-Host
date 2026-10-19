# Host 目录与 workspace 文件约定

状态：2026-10-13 本期已实现并完成本地迁移。
2026-10-14 新增共享工程登记与执行回执；worktree/PR/MR 生命周期仍待实现。
2026-10-15: Optional multi-user profiles, identity receipts and source grants implemented.
配套[文件记忆方案](file-memory-design.md)；实现细节见[详细稿](workspace-layout-detail.md)。
沿用名称 `.agent-host/`。

## 配置、学习结果与任务副本

用户定义角色和配置，Host 学习事实与处理方法；每次任务再装配需要的内容。
`config/` 运行期只读，生成内容进入 `memory/` 或 `cache/`，外部资料归数据源。

```text
.agent-host/
├── config/
│   ├── host.json、agents.json、users.json、channels.*.json、datasource.plugins.*.json
│   ├── agents/default/              # 用户维护 AGENTS、SOUL、IDENTITY
│   ├── agents/ops/                  # 用户复制模板后注册的运维角色
│   ├── users/<user-id>/USER.md       # 每个人独立的显式私人资料，可选
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
├── users/<user-id>/                # Host-created USER.md and verified account receipts
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

## Multiple users and external knowledge

Implemented on 2026-10-15 with automated isolation tests. Live multi-person channel
acceptance remains pending. Durable memory stays local; Confluence is a knowledge
source and publishing destination, not a replacement for memory storage.

AGH identifies each person independently of the selected Agent. Each person has
one Host user ID. First verified contact creates `users/<user-id>/USER.md` with
empty personal sections. An optional `config/users/<user-id>/USER.md` overrides
that default; Agent role
instructions stay under `config/agents/<agent-id>/`. Hobbies, background and
explicit communication preferences belong in that person’s profile. Several
Agents can use the same profile without copying it into their role directories.

| Record | Where it belongs | How it is used |
| --- | --- | --- |
| Verified account bindings | `config/users.json` | Explicitly associate platform accounts with a person |
| Observed account receipts | `users/<user-id>/accounts/` | Preserve identity provenance across restarts; included in backups |
| Automatic default profile | `users/<user-id>/USER.md` | Created once from verified identity; private tasks only, never overwrites edits |
| Explicit profile | `config/users/<user-id>/USER.md` | Select only for that person’s private tasks |
| Learned facts, preferences, decisions and procedures | Local versioned `memory/` | Recall within existing Agent, audience and project/source boundaries |
| Detailed findings, experience and task reports | Authorized Confluence pages/blogs | Search/read on demand; preserve source-task links |
| Task context and source copies | Rebuildable cache or explicit datasource snapshots | Keep source/version metadata; retrieval does not automatically create memory |

To enable multi-user private conversations:

1. Configure each execution profile’s `agents_config` to select `agents.json` or
   a separate `agents.<name>.json` in the same configuration directory. Separate
   catalogs preserve channel-specific Agent roles and project allowlists.
2. Place [users.example.json](../config/users.example.json) as `config/users.json`
   beside that file, replacing example accounts with verified account IDs. An
   empty `users` array also works: new same-enterprise/tenant senders get stable
   generated IDs and a new empty private profile.
3. Add optional `config/users/<user-id>/USER.md` files and register those user IDs
   in `users.json`. Keep directories private (0700) and files private (0600).
4. Run `agent-host users check --config-dir /absolute/path/to/config`, then the
   normal service check and restart. Use `users list` with the same flag to see
   configured and observed accounts without printing profile content.

Without `users.json`, existing owner-only private intake remains unchanged.
With it, DingTalk and Feishu accept other authenticated same-enterprise/tenant
private senders. Stable channel/application/tenant/account IDs determine identity;
display names and chat claims cannot link accounts. Explicit operator-verified
bindings can share a profile across channels; they do not merge historical memory.
The configured channel owner retains the legacy owner partition and privileges.
Other private users retain separate native profiles. All channel users currently
share Owner-equivalent execution with `bypassPermissions`; owner approval is paused.

Private memory and group/project knowledge have separate visibility. A shared
Agent or project does not merge users. Group/meeting tasks exclude private USER
content. During Beta, all users and Agents receive all enabled datasource, mailbox
and configured DWS tools. User/group source allowlists are suspended. Access uses
the configured connector account and its full reachable scope; it does not
impersonate the requesting user in the upstream service.

Confluence remains on-demand MCP search/read/write. Do not preload all posts or
automatically re-import AGH-authored reports into memory. Select a concise local
lesson or reference only when useful and supported by evidence. Publication is
not independent verification. Optional external-page caching remains future work;
there is no new full-corpus cache or automatic publication in this increment.

Profile edits are explicit operator maintenance. Configured profiles reload on
restart; generated defaults are read for each task. Neither file grants permissions;
ordinary learning never rewrites USER.md. When correcting a learned preference,
invalidate its conflicting local memory records as well. Automatic conflict
matching and account-link verification workflows remain future work. Existing
records are preserved; rebinding an account changes future selection without
silently migrating old memory or replaying queued work.
See the [recording and validation details](workspace-layout-detail.md#multi-user-recording-and-retrieval).

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
