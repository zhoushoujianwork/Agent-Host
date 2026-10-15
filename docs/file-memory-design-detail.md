# 文件记忆与经验 Skill：实现详细稿

配套[主文档](file-memory-design.md)与[workspace 详细稿](workspace-layout-detail.md)。
本页区分已实现行为、历史验证和后续范围；主文档决定产品范围。

## 后端与目录

`internal/state/file_memory.go` 实现内置文件后端。已迁移的状态目录通过
`state/file-memory.json` 选择它；此文件是系统迁移回执，属于运行数据，
不放入用户维护的 `config/`。只读 CLI、Host 查询与 viewer 都遵循该选择，
不会为了得到结果回读旧 SQLite 记忆表。

```text
.agent-host/
├── config/host.json
├── memory/
│   ├── sources/<source-key>/
│   │   ├── current.json                 # 唯一原子发布权威
│   │   ├── publications/<generation>.json
│   │   ├── applied/<generation>.json     # 运行状态应用回执
│   │   └── publish.lock
│   └── domains/<domain-id>/
│       ├── scope.json
│       ├── current.json                 # 便于查看的域视图，不是发布权威
│       ├── entries/<id>/revisions/<revision>/
│       │   ├── record.json
│       │   └── content.md
│       └── skills/<id>/revisions/<revision>/
│           ├── record.json
│           └── SKILL.md
├── cache/
│   ├── memory/<source-key>/index.db
│   └── workspaces/<run-id>/             # 上下文及 native-skills 包
└── channels/<channel>/state/
    ├── state.db                         # 正式任务、会话、候选及证据
    └── file-memory.json                 # 后端身份与路径回执
```

source-key 从明确的来源名生成。domain-id 同时包含来源、scope、Agent 与 audience；
旧绝对路径 scope 保留为历史身份。相同角色名、cwd 或共享根目录都不合并学习域。
一个来源的状态目录身份不能通过更改迁移参数静默替换。

`record.json` 保存完整 Memory 结构、来源身份和状态目录定位；正文文件可直接阅读。
所有历史版本、关联证据和失效状态都保留。禁用、恢复、冲突与纠正目前记录在
不可变版本及发布清单中，未增加独立 policies/drafts 编辑工作流。
`SKILL.md` 是正式方法正文，任务里的原生插件包装是派生副本。

## 发布与恢复

来源的 `current.json` 是唯一发布权威，原子替换它后才算发布完成。
域的 current 文件只是查看用投影，即使投影尚未更新，也不能代替来源清单判断生效。
读取者核对来源身份、所有引用版本的正文和元数据摘要，损坏或被直接修改时明确失败，
禁止静默使用旧索引或旧记忆表。

每个来源使用跨进程 publish 锁；版本文件与目录同步后写发布日志，再原子提交来源清单。
清单前中断留下未生效文件，重试可以重新生成；已被清单引用的版本不可覆盖。
运行状态修改以发布日志中的操作记录恢复，applied 回执标识已完成的应用。
文件发布与运行数据库并非跨介质 SQL 事务；恢复根据已发布事实补运行回执和索引，
不重复执行原业务任务。竞争或比较快照变化会显式失败，而非用旧决定覆盖新状态。

记忆索引数据库附加运行库用于已有事务接口，发布记忆来自文件快照；
运行记录仍进入 state.db。旧 memories/revisions/evidence 表在文件模式下保持历史，
不做记忆双写。回退转换器是更新这些旧表的显式管理操作。

## 沉淀、禁用与比较

用户陈述保留原始归属与证据性质。文件后端会自动比较并发布通过筛选的执行候选，
原始候选、工具证据和 Jev 结果仍在运行库。legacy SQLite 后端保留人工接受流程。
`reviewed_experience` 是沿用的来源类型名称；回执和审核元数据区分自动筛选与历史人工接受，
不能把自动筛选称为独立事实核验。

文件后端用精确重复、检索邻域、冲突记录及持久偏好组成有界比较集合，最多 100 项；
不再要求将整个范围全部送给模型。禁用集合进入独立准入检查，不能用于召回。
禁用条目过多或正文预算超限时，发布明确失败；已经完成的业务执行不因此重放。
模型判定仍可能漏掉改写后的等价内容，有限候选集不是“全域不存在重复”的证明。

`memory invalidate --reason REASON ID` 禁用；`memory restore --reason REASON ID`
显式恢复未到期的禁用或冲突记录。二者生成新版本，保留原因与历史。
`memory restore --undo --reason REASON ID` reverses only the latest explicit
restoration, returning to its previous disabled or conflicted status in a new
audited revision. Later changes prevent undo; original evidence remains intact.
等价禁用经验的重复准入在候选、陈述和发布检查中受阻；不确定关系不自动启用。

## 检索、任务视图与原生 Skill

SQLite FTS5、对象/别名/主题索引从正式快照重建。查询先限定 Agent、audience 和 scope，
再检索、判断适用性并控制上下文预算。每次使用前检查发布代次及摘要，
索引过期就重建；无法校验或重建时报告失败，不将其伪装成没有结果。
目前不是向量检索，也没有对外部 local-documents 做统一增量全文入库。

自动上下文沿用最多 8 项、12 KiB 记忆正文。文件快照完整校验与重建仍涉及全部保留版本，
消除全量模型比较不等于已经达到大型记忆库的性能目标。
增长与召回质量需要单独评估，不能用 SQLite 的理论容量作本项目性能承诺。

角色通过 `agents.json` 的 `instructions_dir` 显式读取 AGENTS/SOUL/IDENTITY，
可选 `user_file` 仅绑定 owner 私人 USER。目录和文件必须私有，不自动写入 config。
上下文视图保留配置摘要、当前记忆 ID/revision/source/evidence，执行 cwd 仍是实际项目。
配置资料优先于冲突的学习偏好，当前请求可覆盖普通回复默认值。

当前有效、未到期、来源为 reviewed_experience、具备条件/步骤/证据的 procedure
可以生成 Claude 的 skill-only plugin，放入视图的 `native-skills/`，由 `--plugin-dir`
显式挂载。最多 8 个 Skill，单个正文 16 KiB、合计 64 KiB；无 scripts/hooks/MCP
配置或 allowed-tools。提炼及 no-tools 请求不挂载。
不改写共享 native profile 或项目 .claude/skills，也不通过 add-dir 扩大读取授权。

Task/skill manifest 与内容摘要在调用前校验；提供、原生发现、Skill 调用与业务结果分别记录。
正式记忆快照、角色、USER 与本次 Skill 集合影响会话兼容性。变化会选新会话，
并在启动前再次检查；已经执行的操作和历史会话不会因禁用而被抹除。

## 迁移命令与回退

先停止有关 worker 并备份运行数据库、config 与现有文件记忆。以下以一个渠道为例：

```sh
make build
./bin/agent-host memory migrate --home "$PWD/.agent-host/channels/dingtalk/state" \
  --memory-root "$PWD/.agent-host/memory" \
  --memory-cache "$PWD/.agent-host/cache/memory" --memory-source dingtalk
./bin/agent-host memory verify --home "$PWD/.agent-host/channels/dingtalk/state"
```

其他渠道使用不同来源名；CLI 目录可独立迁移为 cli 来源。
迁移保留 ID、版本、scope、证据、无效状态与 pending 候选，不接受历史候选。
相同参数重复迁移校验现有发布；不同来源、状态目录或根目录组合被拒绝。

用户显式将 host.json 加入：

```json
"memory": {"backend": "files"}
```

然后 service check 并重新启动。files 必须已有匹配迁移回执并通过校验；
显式 sqlite 与文件回执并存时拒绝启动，要求先执行 rollback。
未指定 backend 的旧运行布局继续可用；存在回执的状态目录仍由文件后端读取。

```sh
./bin/agent-host memory rollback --home "$PWD/.agent-host/channels/dingtalk/state"
```

回退验证全部文件版本与证据后导入 SQLite，保留文件期间新增的版本与禁用记录，
成功后才移走后端回执。正式文件仍保留以供检查，不能恢复旧备份丢掉新知识。
旧 SQLite 派生注释仍有单范围 200 项限制；文件后端可保留并核验更大集合，但回退
无法在旧索引约束中表达时会明确失败并保留 marker，不通过删除记忆完成回退。
多个启用渠道完成回退后再显式选择 sqlite 并重启。

## 已完成验证与边界

2026-10-13 的隔离历史验证使用只读 SQLite backup，所有 CLI 操作只作用于副本，
没有平台连接、消息发送或业务任务。私有报告位于
`.agent-host/validation/file-memory-history-20261013-final/report.json`。

| 历史数据 | 对照结果 |
| --- | --- |
| 钉钉 3 条记忆、6 个版本、3 个证据关联；1 个有效 procedure、2 个失效记录 | 全部 show 与历史原文查询一致；迁移重试一致；文件模式旧 SQL 表原始摘要不变 |
| 飞书 10 次运行、0 条记忆 | 空库迁移、核验、缓存重建与回退通过 |
| 删除 cache/memory 后重新查询 | 从正式文件恢复，结果一致 |
| 回退副本 | 正式 payload、ID、范围、状态、版本、证据及候选语义元组一致 |

旧历史有一处 memories.text 冗余投影未随 procedure payload 更新；回退按不变的
正式 payload 正文修复了该列。报告单独保存修正摘要，未放宽正式正文或历史证据比较。

隔离真实 Claude 已发现并成功调用唯一历史 Skill，仅总结原有来源；1 次 Skill 加载、
0 次业务工具执行，execution completed、memory unchanged、experience empty。
Viewer 同范围看到 1 项有效、2 项失效；跨 Agent/audience 无结果。
这些记录证明此次历史迁移与有界原生调用，不能证明普遍任务收益或完整新经验学习循环。

本地切换已完成：钉钉、飞书和 CLI 使用同根 memory/cache 及独立来源身份；
host.memory.backend 为 files，两个渠道已重新连接，workbench API 返回 3 项记忆、
1 个候选和 2 个渠道。默认角色沿用原 CLAUDE.md 全文并通过 instructions_dir 加载，
未自动绑定私人 USER。owner_notes 的 14 份 Markdown 已迁入 local-documents 插件快照，
正文摘要与授权不变；旧顶层 knowledge 目录已退出运行布局。
迁移时，旧两库的 memory、revisions、evidence 及原派生索引表已与备份逐项核对一致。
The historical cutover archive was retired on 2026-10-15 after current recovery
verification; use the [workspace backup procedure](workspace-backup-design.md).
尚未交付直接编辑记忆的导入/草稿工作流、手写 config/skills 新挂载入口、向量检索、
多万条性能、Codex 适配、外部资料统一索引和自动日记/定时学习。
