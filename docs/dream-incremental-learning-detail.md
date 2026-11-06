# Dream 增量整理与 Skill 演进：实现细节

主方案见[设计主文档](dream-incremental-learning.md)。本稿描述本次实现；上线状态以部署验收记录为准。

## 证据和检查点

`dream_work_items` 按精确 person/Agent/audience/scope 与稳定 source key 保存当前/前次证据 digest、策略版本、run/session、缺失原因、Skill 证据、收据/候选引用、尝试次数和有上限的退避时间。`dream_scan_cursors` 保存分区游标；到达末尾后轮转回旧 run，因此旧记录的终态、验收或事件变化会再次进入轻量 digest 检查。

维护按 person、Agent、audience、scope 隔离的持久处理账本。每个逻辑来源保存稳定 source key、证据 digest、关联 Host run/session、提炼策略版本、处理状态、缺失证据、上次处理时间和候选/产物引用。处理状态至少区分待处理、已处理、待补证据、可重试失败、已淘汰；数据库记录是检查点，模型自行撰写的“上次记忆”不是权威状态。

canonical Host run 优先使用权威 run/session、终态结果和有界事件证据。采集必须匹配可信 sender 或本地 owner，且匹配 Agent/audience/scope；不能把所有本地 run 归给当前用户。仅有外部交接文件时继续保留 external_agent_supplied 来源等级。

实现只接受 `Run.Source.SenderID` 与 person 相同的渠道 run；无渠道来源的 run 仅允许 `local-owner`。同一交接引用到匹配的 canonical run/session 时收敛为 `host-run:<run ID>`，不再按文件路径重复提炼；不匹配或不可验证的交接仍是 external evidence。

升级时，内容与精确分区都未变化、且旧收据已 extracted 的外部交接复用原收据与候选，补建新账本；不复用旧 diagnostic_only 结论。首次加入 canonical 终态、验收和实际 Skill 事件属于证据版本变化，会重新评估一次，不删除原候选。后续相同 canonical digest 不再提炼。

Skill 证据区分 catalog 提供、实际加载和引用读取，保留 Skill ID、revision、content digest、事件 seq、成功/失败状态。不能把 catalog 中的全部技能写成“已使用”。摘要包含必要的用户请求和最终执行/验收结果；完成结果优先于原始待办请求，防止已完成任务重新进入 TASKS。

证据 digest 覆盖影响结论的终态结果、验收状态、事件证据和 Skill 版本。文件内容没有变化但 Host 验收或证据补齐时也必须重评。相同分区和 digest 的已处理/待补证据项不重复提炼；待补证据仍可做轻量变更检查。新 digest 与旧记录链接，保留旧结论。删除或拒绝后的来源保留去重记录。

扫描使用有界分页与持久游标；处理完成后才推进对应检查点。旧记录的状态更新要能被发现，不能只用“最新 run ID”永久跳过旧记录。中断不能把未完成提炼标为成功。模型失败使用有界退避；业务任务永不因 Dream 重放。

handoff 文件使用独立的持久名称游标，每轮读取最多 64 个目录的内容；目录名称流式遍历，到达末尾后轮转，不受手动 `dream scan` 的 256 个交接目录总量限制。handoff 扫描失败会记录错误，不阻止同轮可验证的 canonical 记录。

每轮 canonical 分页上限为 64（较大的显式 batch 可提高到 batch，最大仍受 Store 200 条限制）。canonical 与 handoff 共用同一 `Batch` 模型尝试预算；已处理、待补证据和仍在退避的条目不消耗预算。提炼失败先持久化为 `retryable`，退避从一分钟开始并封顶一小时；Dream 不重放原业务任务。

提炼输入把 `canonical-outcome.json` 放在原请求之前，并保留最终 execution/acceptance、输出与错误，避免仅因历史请求文本把已完成事项复活为 TASKS。模型只能引用 Host 建立的 artifact/event/tool allowlist。

检查命令：

```text
agent-host dream work-list --home PATH --person ID --agent ID --audience ID --memory-scope SCOPE --status all --format json
agent-host dream work-show --home PATH --person ID --agent ID --audience ID --memory-scope SCOPE --id WORK_ID --format json
```

## 候选与 Skill 修订

修订保存目标 Skill ID/revision/digest，并且必须由实际完成的加载/引用证据支持。生成版本保持未审核、未发布，不改变原先 Selected/Enabled。

候选必须携带完整来源引用；模型输出不能指定任意磁盘路径、权限或账户。模型可提出新建或修订建议，Host 核对目标 ID、当前 revision/digest、归属和适用范围后才创建草稿。针对现有 Skill 的建议必须关联真实读取/使用证据，不能仅凭名字相似修改技能。

修订基于精确旧版本，新建不可变 revision；旧 revision 在审查、发布、启用完成前继续有效。过期版本建议不得覆盖较新人工修改。一个来源版本/候选对应的操作有稳定幂等键，重复醒来或崩溃恢复不能创建多个相同草稿。全局 native Skill 原目录不会被 Dream 写入；需要演进时使用显式受管版本。

当前不新增自动 publish/enable。审核、发布和启用仍使用现有 managed catalog 的 actor、revision 和 digest 校验。未来自动生效策略必须独立表达风险和范围，不将模型评估伪装成人工审核。

### 空闲时自动生成草稿

在已经启用的 Full Host 渠道 Dream 配置中可增加：

```json
{
  "draft_skills": true,
  "skills_root": "/absolute/app-data/managed-skills",
  "cleanup_retention": "720h"
}
```

这是现有配置的补充字段，`skills_root` 必须是已存在的私有规范目录。`draft_skills` 默认 false。启用时，启动过程须通过渠道 owner、Agent、audience 和 scope 校验；分区令牌与重新读取的配置不一致即拒绝启动。CLI chat 和旧 offload-channel 路径没有此令牌，不能启用后台自动草稿。

自动草稿只处理 Host 观察到、已完成且验收通过的 canonical 证据，创建前再次计算当前证据 digest。外部交接或未注册的目标不绕过校验，候选保留 pending 并记录 blocked 原因。重复且证据/目标版本未变化的阻塞建议不重复写结果；独立轮转游标避免一条阻塞建议占满后续每轮 batch。

`dream list/show` 可查看 `TargetSkillID/Revision/Digest`、`SkillID/Revision/Digest`、`SkillDraftStatus/Reason`。新版本包含原 Skill 的文件与适用范围，另附 `references/dream-change-<candidate>.md` 改进草稿；审核者处理草稿后，按原有流程审核、发布和启用。自动创建草稿不等于改进已生效。

## 清理

`dream_cleanup` 保存计划与完成记录；后台在成功导出当前 HOT 后执行有界清理。`cleanup_retention` 默认 `720h`，配置最小 `24h`。

第一阶段只物理清理已识别为 Dream 所有的派生 HOT 缓存，默认保留 30 天；这是缓存保留期限，不是知识有效期。保持当前有效投影及依赖中的版本，不物理删除权威记忆、Skill catalog/revision、用户维护技能、原始证据或审核账本。

清理记录包含分区、规范绝对路径、内容 digest、来源/产物引用、原因、计划和完成时间。路径必须位于配置的专属缓存根目录，拒绝 symlink、目录逃逸、未知文件和 digest 不匹配；无法证明安全则跳过并说明原因。先持久记录含完整 manifest、设备/inode 的删除意图，再移到同分区 `.retire-<digest>`、复核剩余文件并逐个删除，最后记录完成。部分删除可在下次唤醒继续，替代目录或修改后的文件不会被沿用旧意图删除。重试先核对文件是否已不存在或已变化，不能盲删替代文件。

到期的 pending 候选标记 expired，保留候选、收据与处理账本；相同来源不会因此重新提炼。记忆到期继续走现有版本化失效/归档流程；Skill 淘汰继续走 disable/withdraw，保留历史以便追溯和回滚。物理清理 Skill 历史或原始执行证据不属于本期范围。

## 验收

- 同一证据连续两轮、重启后再运行：提炼和草稿创建均不重复。
- 原记录未完成或验收未决，后续补齐：能发现变化并重新评估。
- 同一 run 同时有交接记录：优先关联 canonical 来源，不凭文件路径重复学习。
- 配置但未读取的 Skill 不标为已使用；失败读取不标为成功；跨人/跨受众证据不泄漏。
- 已完成任务不因原始请求再次生成未完成待办。
- Skill 目标版本变更后旧建议失效；重复晋升不追加同样 revision；生成草稿不改变启用版本。
- 清理当前/依赖中/被修改/非 Dream 所有路径被拒绝；删除中断恢复不重复删除、不重生候选。
- 空闲调度与前台取消保持不变，控制面可查看处理结论和缺口。

## 当前边界

- 最多采集每个执行最后 512 个事件、256 对工具证据；超过覆盖窗口的执行标记待补证据，不冒充完整学习。
- 缓存分区超过 4096 个目录项时清理明确报错，保留原文件；不会扩大删除范围。
- 只生成 Skill 草稿，审核、发布、启用仍显式操作。当前没有自动删除 Skill 历史或原始执行资料。
- `dream cleanup-list --home PATH --person ID --agent ID --audience ID --memory-scope SCOPE --status all --format json` 查看淘汰记录。
