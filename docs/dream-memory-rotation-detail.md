# 梦境记忆轮转：实现设计

状态：部分交付。Phase 0/1、私聊 HOT 注入与 Host-owned opt-in 周期已交付；策略自动准入和后续
review/promotion 自动化仍为 **NOT DELIVERED**。
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

### 1.1 可信分区与多文档投影

Host 从可信调用参数取得 person/Agent/audience/scope 分区；交接内容中的同名字段只是证据，不能改写
目标分区。未来 HOT 输出是现有权威 Memory 与 Skill revision 的有界、多文档投影：

| 投影 | 内容与所有权 |
| --- | --- |
| `USER.md` | 仅本人已认证并确认的稳定工作偏好；单次行为、转述和模型推断保持待审，不做心理画像 |
| `MEMORY.md` | 当前 scope 内有来源的事实、决定、约束和经验，服从 TTL、冲突、禁用与 revision |
| 当前任务文档 | 正在进行的目标、约束、证据状态和未决项；结束后不自动升级为长期偏好 |
| `AGENTS.md` | 操作者拥有的行为、职责与执行边界；梦境流程只读且永不覆盖 |
| `SECURITY.md` | 操作者拥有的权限与安全规则；不得从来源或行为推断、扩张或覆盖 |

投影可删除并从权威版本重建；正文不得在投影中独立修改。Skills 与等待退役的 archive 继续使用各自
现有版本和生命周期，不能由 HOT 文档成为第三套权威状态。

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

Phase 0 扫描键与 Phase 1 幂等收据均已交付：

```text
sha256(source_identity || destination_identity || canonical_run_id_or_empty ||
       canonical_session_id_or_empty || artifact_manifest_digest || dream_policy_version)
```

`source_identity` 是规范绝对交接目录引用；`destination_identity` 是已配置
Host/Agent/audience/scope 绑定的稳定标识，不含凭据。相同键与相同
payload 返回原收据；相同键不同 payload 明确失败。策略升级产生新尝试，但必须关联先前收据，
不能覆盖历史决定。若规范 Run 仍可读，必须复用其 `ToolEvidence`；不得把副本重新标为 Host-observed。

## 3. 提炼边界

Host 组装结构化来源：原请求、执行/接受/记忆状态、成对工具证据、Agent 报告及其归属、当前同范围
记忆邻域和禁用锚点。报告只提供候选线索；成功事实必须由相符的结果证据支持。孤立交接的工具对
标为 `external_agent_supplied`，保留“调用者提供、非 Host 观察”的限制。

Offload package 启动独立干净提炼 Agent：空运行工作区、无 MCP、无插件、无 native Skills、
无业务工具、无网络工具、无 continuation；只允许结构化输出。配置必须显式选择兼容模型，失败不
回退。提炼调用有输入字节、候选数、调用次数和时间硬上限；token 与成本仅在 provider 提供可验证计量/限制时使用。缺失成本为 unknown，不以零成本继续预算判断，也不声称具备美元硬限额。超限产生诊断，不截断后假装完整。

**已落实的可行性门槛：** 干净工作区不等于工具隔离。手动提炼只接受显式配置、支持严格
tool-free structured extraction 的 Claude profile；Codex 与其他 backend 在派发前 fail closed，不靠提示词
宣称无工具，也不静默换模型。

提炼器输出四类待审候选；失败另存收据诊断，不伪装成候选：

- `knowledge`：有来源、适用条件和范围的事实/决定；
- `working_preference`：本人历史工作方式；推断项保持 pending，显式接受前不能进入 HOT；
- `task`：仍在进行的目标或待办，只进入有 TTL 的 `TASKS.md` 投影；
- `procedure`：有条件、步骤、avoid 和精确证据引用的可复用方法；

不得输出全局用户偏好。偏好只有在规范 Run 包含已认证、明确归属的用户陈述且目标范围相符时，
才复用现有 user-statement policy；普通交接 brief、外部 Agent 转述或报告措辞不满足此条件。

## 4. 状态模型

### 4.1 Dream receipt

```go
type DreamReceipt struct {
    ID, IdempotencyKey, Source, SourceDigest string
    Partition DreamPartition // PersonID + AgentID + AudienceID + Scope
    CanonicalRunID, CanonicalSessionID, Attribution string
    Status string // scanned or extracted
    CreatedAt time.Time
    Error string
}
```

上述手动收据已交付。后台 lease、`paused`/`canceled`、预算 cursor 和自动队列仍未交付；未来取消只
取消提炼/发布，不取消或改变原业务 Run。

### 4.2 Candidate and hot lifecycle

```go
type DreamCandidate struct {
    ID, ReceiptID string
    Partition DreamPartition
    Kind, Text, Status string // pending, accepted, rejected, promoted
    SourceRefs []string
    ExpiresAt time.Time
    Inferred bool
    ReviewActor, ReviewNote, MemoryID, SkillID string
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

## 5. Host 接口

`dream scan/run/list/show/review/promote/export/archive/rotate/restore/status` 已交付。服务通过显式 `--dream-config`
启用同一 Lifecycle 的串行周期；`enabled` 控制后台提炼，`hot_enabled` 独立控制运行时 HOT。周期随 owning
context 取消并保存 status/error，每轮后重新投影 HOT；持久状态写入失败会停止服务。独立 pause/resume 命令仍未交付。
Full Host 每次创建或继续会话都从权威记忆重算 HOT 摘要，用 Host 副本形成运行指纹，不修改共享配置；摘要变化或
到期会拒绝旧会话。个人文档只进入认证本人的私聊，群聊、其他人和不匹配的 Agent/audience/scope 不继承个人原生状态。

首版优先内部库与 CLI，不提供公共插件 API。当前交付命令：

```text
agent-host dream scan --source ABS --person ID --agent ID --audience ID --memory-scope SCOPE --format json
agent-host dream run --home PATH --source ABS --memory-offload-config FILE --person ID --agent ID --audience ID --memory-scope SCOPE --format json
agent-host dream list|show|review|promote|export|archive|rotate|restore ... --format json
agent-host dream status --home PATH --dream-config /absolute/dream.json --limit 20 --format json
# NOT DELIVERED: background pause|resume|cancel
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

**DELIVERED。** 扫描只接受显式绝对私有 root 下的 `handoff-*`；要求 `launch.json`、`request.txt`
和与 stream format 对应的 `stdout.txt`/`events.jsonl`，允许缺少其他导出。它有目录、文件和总字节
上限，拒绝 symlink、路径逃逸、非当前用户、非私有、hard link 与扫描中变化；按文件名排序并摘要
全部文件。输出含可信目标分区、规范 Run/session 引用、manifest digest、策略版本和幂等键。非零
exit、未定稿与无效来源只产生诊断，不执行命令、不写 Store、不调用模型。

### Phase 1：手动单次候选与 HOT 导出

**DELIVERED。** 显式 `memory_offload_config` 只允许 Claude；严格 tool-free structured extraction 生成
带 TTL 和来源摘要的 knowledge/working-preference/task/procedure 候选。规范 Run 只有在可信 Store 中的
person、Agent、audience、scope、run/session 全部匹配时才标为 Host-observed，否则保留
`external_agent_supplied`。显式 accept 把非 procedure 写入现有版本化文件记忆；procedure 只晋升为
未审核、未发布、未启用的 managed authored revision。HOT export 从仍有效的权威 revision 生成有界
`USER.md`、`MEMORY.md`、`TASKS.md` 与摘要 manifest；Phase 2 将其接到运行时。

### Phase 2：运行时 HOT 与 Host-owned 周期

**DELIVERED。** `execution.Request.PresetDocuments` 在 clean CallerContext 下接收 `USERS.md` 与
`MEMORY.md`；后者包含明确标记的 active tasks。HostInstructions 保持独立，永不生成 AGENTS/SECURITY。
每轮从权威 file memory 重算并核对 person+digest；群聊不选择个人文档，digest/到期变化拒绝旧 session。
owner-private cache 使用 digest 目录原子发布文档与 manifest。后台周期默认关闭，随服务 Store writer
lock 启停，限制 interval、batch、timeout，自动归档到期项并只创建 pending candidates。

The source scanner remains independently capped at 4,096 root entries and 256 `handoff-*` directories.
`batch` limits new extraction attempts within that bounded scan; already extracted or diagnostic receipts do not
consume the batch, and skipped receipts are still returned for observability. An ordinary source or model error is
persisted as a failed cycle and processing continues on the next tick. Only cycle-status persistence failure or
parent cancellation terminates the background loop.

首版 review gate：任何 working preference（包括历史行为推断）和 knowledge/task 都必须由显式 actor
接受后才进入权威记忆；procedure 只能显式晋升为未 review/publish/enable 的 authored Skill revision。
未来策略自动准入仍为 **NOT DELIVERED**。

### 后续 A：策略自动准入（NOT DELIVERED）

允许策略白名单类型自动 hot，复用文件记忆事务、scope、禁用锚点、expiry 和检索预算。停止条件：
过期/禁用后所有入口均排除，缓存重建不改变结果。

### 后续 B：跨层管理视图（NOT DELIVERED）

复用现有 review/publish/enable 和 disable/withdraw/rollback；增加跨层只读投影。停止条件：晋升、
回滚和恢复均不改变工具、插件或 native Skill allowlist。

### 后续 C：独立暂停控制与生产启用（NOT DELIVERED）

已有默认关闭的 Host 后台周期、取消和运行状态；独立 pause/resume 控制尚未实现。生产启用不属于本次隔离验收。

## 9. 可复现隔离验证计划

自动化回归使用临时 home、临时 handoff root、假 Offload runner/模型、固定时钟和本地文件，不访问真实服务或生产绑定。下述补充模型验收仅使用独立测试目录和虚构记录。

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

## 10. 实现前的可行性证据（历史记录）

2026-11-05 的只读调查运行了：

```text
go test ./internal/state ./internal/experience ./internal/managedskills ./internal/assembly ./internal/host
python3 -m unittest chat_test.py   # 在 skills/agent-host-tasks/scripts 下
```

Go package tests通过（输出为 cached）；交接导出器 37 个测试通过。首次从仓库根运行 Python 测试因
脚本的本地 `results.py` 不在模块路径而导入失败，改在脚本目录运行后通过。这些结果支持复用现有
收据、外部证据、经验判断、文件记忆和 Skill 生命周期，但没有执行本文拟议 scanner、worker、
scheduler、TTL hot rotation 或 merger。

上述是实现前的基线，不代表当前交付状态。当前实现范围以主文档和 Phase 0–2 的 DELIVERED 标记为准。
Full Host catalog/project 路由现已接入；不匹配的 person、Agent、audience 或 scope 保持原运行行为且不注入个人 HOT。
长期收益、大规模性能、生产延迟、自动准入及物理删除仍未验证或交付。


## 11. 2026-11-05 隔离模型验收

- 使用独立应用数据目录 `validation/dream-20261105`，没有导入真实生产事项，也没有启用生产后台。
- 严格无工具提炼使用现有显式 Claude memory profile；首次暴露缺少 native session ID 的适配问题，修复后真实重试成功，生成一个带来源、范围和有效期的待审个人偏好。
- 显式测试 actor 接受后，版本化记忆与 HOT 导出包含该偏好；私有 cache 自动生成三份文档和 manifest。
- 后续干净 CLI Chat 使用默认 `gpt-5.6-sol`，实际回答保留了“仅合成验收测试、简洁中文、结果优先、2026-12-01 到期”的语义。Run `edf3e432-3a6c-4345-a94e-6dfa072e5db6` 完成约 22.2 秒，未观察到工具事件；费用不可用。
- 显式 retire revision 1 后 HOT 为空；以 revision 2 恢复未过期记忆成功。原始执行文件保留。
- Skill 晋升、批次前进、瞬时错误恢复、过期隔离和快照篡改拒绝由隔离回归测试覆盖；不声称已验证生产整理质量或长期收益。

本机原始证据保存在应用数据目录的 `validation/dream-20261105/acceptance.json`、各阶段 JSON 和 stderr 中，仓库仅记录去敏结论。


## 12. Runtime 配置示例

先用 `memory migrate` 为独立 home 启用版本化文件记忆，再把以下配置写入 owner-private 的绝对路径 JSON 文件。
路径、身份和 scope 必须替换为已确认的实际值，目录预先创建为 0700，配置文件为 0600：

```json
{
  "enabled": false,
  "hot_enabled": true,
  "cache_root": "/absolute/application-data/dream-cache",
  "person_id": "local-owner",
  "agent_id": "default",
  "audience_id": "owner",
  "scope": "project:example",
  "batch": 20
}
```

直接 CLI Chat 传入 `--dream-config /absolute/dream.json --memory-scope project:example`，并显式指定对应 home、workspace 和 Agent。
CLI 单次 Chat 只加载 HOT，不启动后台循环。业务模型不指定时继续采用 Offload 默认配置。

渠道后台将 `enabled` 改为 true，并补充 `source_root`、`memory_offload_config`、`interval`（例如 `30m`）、
`timeout`（例如 `5m`）。person 必须等于渠道配置中认证 owner ID；不能把共享原始目录无区别归给多个用户。
后台和 HOT 可分别开启，前者的模型配置位于这份 JSON 内，不要混用启用 Full Host 的 CLI `--memory-offload-config`。

统一服务配置可在所选 execution profile 中写 `"dream_config": "dream.json"`；相对路径按同目录 `host.json`
解析并作为 `--dream-config` 转发。Full Host 启动后台前会校验目标 person 是渠道 owner，且 Agent 与 scope 属于渠道 catalog。
本例只说明配置结构，不表示已在生产启用；个人 Skill 发布权限仍需后续适配。


## 13. 生产记录体积与周期观察

真实交接记录可能达到数 MB。提炼器保留原始文件和完整摘要，只向模型提供总计不超过 480 KiB 的确定性摘录；
优先包含请求、定稿元数据和输出，每份来源有单独上限。截断和省略会明确标记，UTF-8 边界保持完整。
引用不完整来源的 procedure 在结构化输出后被过滤，不能把摘录当成完整成功证据。提炼提示携带当前 UTC 时间。

`dream status --home PATH --dream-config ABS --limit N --format json` 直接查询最新 N 个周期，N 为 1–200。
历史超过 200 个周期仍可观察最新状态，不扫描或截断最早的记录来推断当前状态。
