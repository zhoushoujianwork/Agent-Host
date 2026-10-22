# 执行经验与原生 Skill

状态：2026-10-13 文件后端已实现并完成本地切换，真实 Claude 已发现并调用历史 Skill。
存储与迁移见[文件记忆方案](file-memory-design.md)，装配细节见[详细稿](experience-skills-detail.md)。

2026-10-22 target direction: [Host-managed Skills](managed-skills-design.md)
makes Skill the final reusable form of handling procedures, with agh registration
and proactive loading by the default Agent. This includes authored procedures
and enabled-plugin usage guidance as distinct sources. The existing reviewed
experience path described here remains one publication source; the unified
catalog and loading policy are proposed, not implemented by this documentation.

Issue [#1](https://github.com/zhoushoujianwork/Agent-Host/issues/1) status reviewed
on 2026-10-20: scoped Skill publication, fresh-task mounting and withdrawal are
implemented. The separate [learned personal preferences](person-profiles.md)
proposal remains open. The issue's original SQLite-authoritative comparison is
historical; published files now own migrated knowledge.

## 使用方式

有证据支持的处理方法保存为正式 `SKILL.md`，新任务检索到适用方法后，
Host 生成本次任务的私有包，交给 Claude 按需加载。用户可以查看来源、禁用或显式恢复。

文件后端会自动发布通过筛选的经验；旧 SQLite 后端仍采用人工接受流程。
迁移不自动接受 pending 候选，历史人工审核与原证据保持不变。

## 核心约定

- 正式 Skill 在 `memory/domains/<domain-id>/skills/`；原生包在 `cache/workspaces/<run-id>/native-skills/`。
- 只装配当前项目、Agent 和 audience 允许的有效方法，保留条件、步骤与证据。
- 不生成脚本、工具许可或 MCP 配置，不改用户手写 Skill 和共享 native 配置。
- 纠正、禁用或到期影响后续任务；会话变化不能抹除已经执行的动作。

事实、偏好与决策单独保存为知识文件，不为了使用 Skill 强行转换成处理方法。
执行完成、经验发布、Skill 被加载和业务结果分别记录。

## 验证与后续

历史 Skill 已在隔离真实 Claude 中被发现并成功调用；该任务只总结来源，
有 1 次 Skill 加载、0 次业务工具执行，没有新增记忆。这证明装配与调用可用，
尚未证明一般任务收益或完整的新经验学习循环。

手写 `config/skills` 新挂载、Codex 适配、方法自动合并、生成脚本与公开发布未交付。
