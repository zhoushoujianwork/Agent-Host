# 梦境记忆轮转：实现设计

状态：设计提案，本文所有新增 schema、状态、接口、命令和 worker 均为 **NOT DELIVERED**。
现有实现仍以[架构](architecture.md)、[文件记忆细节](file-memory-design-detail.md)、
[执行经验细节](experience-memory-detail.md)和[托管 Skills 细节](managed-skills-design-detail.md)
为准。本文不改变当前生产配置、服务或数据。

## 1. 设计原则

1. Host 是唯一业务与记忆策略主体。Offload package 只执行有界、无业务工具的提炼调用；
   原生引擎继续拥有模型/tool loop 与原生会话状态。
2. 原始执行记录是证据，不是知识。派生候选只能引用来源，不能改写、替换或提升来源语义。
3. 执行成功、业务接受、记忆准入、Skill 审核、发布、启用和退役分别记录。
4. 先做最小离线单次路径，再考虑后台调度；所有失败显式，不切换弱提供者、不重放任务。
5. 热记忆是正式版本上的有界检索集合。权威内容仍进入现有版本化文件记忆；SQLite 只作运行状态
   与可重建索引，不建立 `dream_truth` 一类平行真相表。

## 2. 来源与不可变性

### 2.1 可接收来源

首版只读取显式配置的 owner-private handoff root，不递归发现其他仓库、备份或凭据目录。
每个交接目录至少包含 `launch.json`、`request.txt` 和原始 stdout/events；可选 `run.json`、
`result.json`、`execution.md` 只在存在时纳入摘要。扫描器不执行其中命令、不打开外链。

只有满足以下条件的目录进入提炼：

- `launch.json.exit_code` 非 null，且 `finalized_at_utc` 存在；退出码只是进程观察，不是验收；
- 目录由当前用户拥有且私有，每个输入为当前用户拥有的私有普通文件，路径无符号链接、硬链接替换或根目录逃逸；
- 规范化 manifest 列出相对路径、字节数、SHA-256 和记录定稿时间；
- manifest 构建前后两次 stat/读取身份一致，摘要稳定；任何变化均标记 `source_changed`；
- 有 Run ID 时，规范 Store 中的 Run/事件优先于 `run.json` 副本；副本只用于差异诊断。

首版允许扫描器生成自己的只读观察 manifest，但不得把它写回原交接目录。manifest 与来源目录
引用保存在梦境收据中；物理原件继续由独立保留策略管理。

### 2.2 幂等身份

拟议幂等键（NOT DELIVERED）：

```text
sha256(destination_identity || canonical_run_id_or_empty ||
       artifact_manifest_digest || dream_policy_version)
```

`destination_identity` 是已配置 Host/Agent/audience/scope 绑定的稳定标识，不含凭据。相同键与相同
payload 返回原收据；相同键不同 payload 明确失败。策略升级产生新尝试，但必须关联先前收据，
不能覆盖历史决定。若规范 Run 仍可读，必须复用其 `ToolEvidence`；不得把副本重新标为 Host-observed。

## 3. 提炼边界

Host 组装结构化来源：原请求、执行/接受/记忆状态、成对工具证据、Agent 报告及其归属、当前同范围
记忆邻域和禁用锚点。报告只提供候选线索；成功事实必须由相符的结果证据支持。孤立交接的工具对
标为 `external_agent_supplied`，保留“调用者提供、非 Host 观察”的限制。

Offload package 启动独立干净提炼 Agent：空运行工作区、无 MCP、无插件、无 native Skills、
无业务工具、无网络工具、无 continuation；只允许结构化输出。配置必须显式选择兼容模型，失败不
回退。提炼调用有输入字节、候选数、调用次数和时间硬上限；token 与成本仅在 provider 提供可验证计量/限制时使用。缺失成本为 unknown，不以零成本继续预算判断，也不声称具备美元硬限额。超限产生诊断，不截断后假装完整。

**实现前可行性门槛：** 干净工作区不等于工具隔离。必须先探测所选 Offload/backend 是否真正支持禁用工具；不支持时 fail closed，不能靠提示词宣称无工具或悄悄换模型。当前调查未证明所有默认 Codex 配置满足此条件，需在 Phase 1 前完成 package 能力探针；若不支持，应先扩展 Offload 能力或由操作者明确选择支持的 backend。

提炼器最多输出三类候选：

- `knowledge`：有来源、适用条件和范围的事实/决定；
- `procedure`：有条件、步骤、avoid 和精确证据引用的可复用方法；
- `diagnostic`：阻塞、失败、未知、被拒或支持不足的信息，仅供检查。

不得输出全局用户偏好。偏好只有在规范 Run 包含已认证、明确归属的用户陈述且目标范围相符时，
才复用现有 user-statement policy；普通交接 brief、外部 Agent 转述或报告措辞不满足此条件。

## 4. 拟议状态模型（NOT DELIVERED）

### 4.1 Dream receipt

```go
type DreamReceipt struct {
    ID, IdempotencyKey, PolicyVersion string
    DestinationID, AgentID, AudienceID, Scope string
    CanonicalRunID, ArtifactManifestDigest string
    SourceRef, SourceAttribution string
    Status string // discovered, validated, extracting, assessed, committed,
                  // diagnostic_only, failed, paused, canceled
    LeaseOwner string
    LeaseExpiresAt, CreatedAt, UpdatedAt time.Time
    Budget DreamBudget
    Items []DreamItemResult
    Error string
}
```

`canceled` 只取消提炼/发布，不取消或改变原业务 Run。`paused` 由操作者或预算策略触发；恢复继续
同一收据的未提交阶段。终态不得自动重新排队。

### 4.2 Candidate and hot lifecycle

```go
type DreamCandidate struct {
    ID, ReceiptID, Kind, Scope, Status string
    // pending_review, hot, excluded, diagnostic_only, promotion_pending,
    // retired, expired, failed
    SourceRefs []EvidenceRef
    DecisionActor, DecisionKind, DecisionReason string
    PolicyVersion string
    ExpiresAt *time.Time
    MemoryID string
    MemoryRevision int
}
```

`DecisionKind` 区分 `host_policy`、`human_review` 和 `model_assessment`；三者不能互相冒充。
模型评估只是准入输入。自动 hot 需要 Host policy 完整通过；其他可用候选进入 `pending_review`。

`hot` 指向现有正式 Memory ID/revision，并通过现有 scope/Agent/audience、status、expiry 和适用性检查
进入有界召回。TTL 到期将正式版本标为 expired/ineligible 或产生等价的新状态版本；索引遗漏不能
绕过有效期。热层不保存另一份可独立修改的正文。

### 4.3 Skill promotion and retirement

`procedure` 候选可形成托管 Skill 候选引用，但不自动成为 authored Skill。它沿用现有经验准入，
再沿用 managed catalog 的 review → publish → enable 生命周期：

- review 记录真实 reviewer、证据说明、候选/记忆 revision 与 digest；
- publish 固定正文、引用、provenance 和条件；
- enable 是独立显式决定，并重新检查 Agent/audience/scope、插件、工具 contract 与 expiry；
- native Skills 仍来自 Agent 配置的精确 allowlist；晋升不能修改 `native_skills`；
- Skill 指令永远不授予工具。所需工具必须已由本次可信运行配置提供。

退役分两层：Memory 使用现有 invalidate/restore 语义；托管 Skill 使用 disable/withdraw/rollback。
拟议 `retired` 只是跨两者的投影状态，不能成为第三套生命周期权威。冷归档保留全部 revision、
来源、审核、发布、启用和退役原因。恢复必须显式、校验当前冲突与依赖，并产生新审计记录。

物理删除不在首版。未来清理器必须先构建反向引用：Run、候选、Memory revision、Skill revision、
审计或法律保留仍引用的证据不可删除；其余也必须满足显式保留期限和已授权删除策略。

## 5. 拟议 Host 接口（NOT DELIVERED）

首版优先内部库与 CLI，不提供公共插件 API：

```text
agent-host dream scan --source ABS --destination ID --policy VERSION --dry-run
agent-host dream run  --source ABS --destination ID --policy VERSION --budget FILE
agent-host dream show --receipt ID
agent-host dream pause|resume|cancel --receipt ID --reason TEXT
```

内部边界建议：

```go
type DreamSourceReader interface { ValidateAndSnapshot(context.Context, SourceRef) (Manifest, error) }
type DreamExtractor interface { Extract(context.Context, DreamInput, DreamBudget) (DreamDrafts, error) }
type DreamPolicy interface { Assess(context.Context, DreamDraft, EvidenceSet) (DreamDecision, error) }
type DreamStore interface { Receive, Claim, CommitItem, Pause, Cancel, Recover }
```

`DreamSourceReader` 不写原件；`DreamExtractor` 只包装 Offload package；`DreamPolicy` 和
`DreamStore` 属于 Host。所有 commit 检查目标分区、来源摘要、策略版本、比较 generation 和预期
revision。网络传输、模型协议和原生事件保持在 harness/Offload adapter 后面。

## 6. 单写者、事务与恢复

手动单次 worker 先复用一个 Host Store 实例。后台版只能由明确启用的 Host 服务把 due job 放入
同一单写者队列；不能让 cron 进程直接打开第二个可写数据库。调度配置记录 timezone、窗口、
最大批量、总预算和下一次时间，默认关闭。

队列使用有期限 lease，claim 与状态更新在本地事务中完成。提炼期间不持有写事务。发布拟复用并补足现有
文件记忆事务恢复机制，增加梦境发布 outbox 协议：先记录 intent，原子发布权威文件，再恢复 SQLite receipt/index；
崩溃后以已发布 manifest 为准补齐收据，绝不重复业务任务或盲目重复记忆发布。

每轮限制来源数、总输入字节、候选数、模型调用、时间和成本。达到限制后状态为 `paused` 或
`budget_exhausted` 诊断，并保留 cursor。取消停止新 claim，等待/终止当前提炼调用后释放 lease；
已提交项目不回滚，未提交项目可从同一收据恢复。来源变化、分区变化、比较 generation 变化或
Skill digest 变化都要求重新评估，不以旧决定提交。

## 7. 决策规则

自动 hot 必须同时满足：来源校验通过；证据支持全部事实；非正面失败伪装；目标 scope 唯一；
未发现冲突或禁用等价项；类型允许自动准入；TTL 在策略范围；无秘密/授权指令；比较快照仍有效。

以下一律不自动进入主动召回：

- `BLOCKED`、拒绝、失败、取消、未知执行，或 exit 已观察但结果未证实；
- 只有命令输入、Agent 自述、文件时间或工具名，没有配对结果；
- 冲突、不确定关系、缺少负责人、跨 scope、全局偏好推断；
- 候选试图增加权限、启用插件、改变工具 allowlist 或扩大文件访问；
- 需要合并多个程序才能成立的泛化方法。

失败可以产生 `diagnostic_only`；若精确失败结果支持一条狭窄的“不要这样做/先检查”经验，可进入
负向 procedure 的正常评估，但不得声称失败操作成功。重复候选优先关联现有 Memory，不合并来源
类型、不延长 TTL、不复活 invalidated/expired 内容。冲突双方保持可检查且不主动召回。

## 8. 分阶段时间线

### Phase 0：只读夹具与 manifest

交付扫描、稳定摘要、symlink/变化拒绝、幂等键和预览 JSON。停止条件：零 Store mutation，且同一
夹具重复扫描字节一致。

### Phase 1：手动单次候选

接入规范 Run 优先取证、孤立来源归属、干净提炼 Agent、预算和诊断收据。只提交候选/诊断；默认
不自动 hot。停止条件：中断恢复不重复模型已提交结果，不重放任务。

### Phase 2：TTL hot memory

允许策略白名单类型自动 hot，复用文件记忆事务、scope、禁用锚点、expiry 和检索预算。停止条件：
过期/禁用后所有入口均排除，缓存重建不改变结果。

### Phase 3：Skill 候选与退役

复用现有 review/publish/enable 和 disable/withdraw/rollback；增加跨层只读投影。停止条件：晋升、
回滚和恢复均不改变工具、插件或 native Skill allowlist。

### Phase 4：可选后台调度

增加默认关闭的 Host queue schedule、lease、pause/cancel 和运行状态。只有此前阶段通过隔离故障
恢复及生产外 acceptance，才允许单个非生产 runtime 试运行；生产启用另行授权。

## 9. 可复现隔离验证计划

全部测试使用临时 home、临时 handoff root、假 Offload runner/模型、固定时钟和本地文件；禁止网络、
真实服务和生产绑定。

| 夹具 | 必须证明 |
| --- | --- |
| 未定稿：`exit_code=null` 或缺 manifest | 保持 discovered/diagnostic，不提炼、不写记忆 |
| 虚假成功：报告称 DONE，工具结果失败 | 不生成正向 procedure；原报告仍可检查 |
| 规范成功：成对调用/结果与完成状态 | 只产生证据实际支持、范围受限的候选 |
| 重复：相同/改写候选与相同幂等键 | 返回原收据或关联现有记忆，不重复发布/延长 TTL |
| 冲突与不确定 | `pending_review`/excluded，双方不主动召回 |
| scope/Agent/audience 交叉 | 不可比较、读取、晋升或恢复到其他分区 |
| TTL 到期与禁用 | 所有 recall、context、Skill assembly 排除；历史仍可检查 |
| Skill 晋升 | review/publish/enable 分离，未显式 enable 不进有效 catalog |
| allowlist | promotion 前后 native Skills 与工具集合完全相同 |
| SQL/文件发布间崩溃 | outbox 恢复同一结果，不产生双版本 |
| 发布失败或预算耗尽 | 原执行不重放，错误/暂停原因和 cursor 保留 |
| 来源文件或 symlink 变化 | `source_changed`/拒绝，旧摘要不提交 |
| 冷退役与恢复 | revision/source/actor/reason 不丢失，恢复重新检查冲突与 expiry |

最小门禁依次运行定向 Go/Python 测试、相关 package tests，再运行 `make check`。端到端 acceptance
还必须用隔离的本地真实 Offload package 验证干净 Agent 确实没有业务工具，并检查实际事件中的
模型、Skill、MCP、工具集合和零任务重放。文件存在或单元测试通过不能替代该观察。

## 10. 当前可行性证据与未验证项

2026-11-05 的只读调查运行了：

```text
go test ./internal/state ./internal/experience ./internal/managedskills ./internal/assembly ./internal/host
python3 -m unittest chat_test.py   # 在 skills/agent-host-tasks/scripts 下
```

Go package tests通过（输出为 cached）；交接导出器 37 个测试通过。首次从仓库根运行 Python 测试因
脚本的本地 `results.py` 不在模块路径而导入失败，改在脚本目录运行后通过。这些结果支持复用现有
收据、外部证据、经验判断、文件记忆和 Skill 生命周期，但没有执行本文拟议 scanner、worker、
scheduler、TTL hot rotation 或 merger。

当前未交付、未验证：原始交接 scanner；稳定 manifest 协议；dream schema/CLI；通用后台调度；
跨程序合并；自动 retirement policy；真实模型提炼质量；长期召回收益；大规模性能；生产延迟与
成本；物理删除安全性。任一后续实现都必须把源码验证、隔离 acceptance 和生产采用分别记录。
