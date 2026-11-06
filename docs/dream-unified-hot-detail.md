# Dream 统一 HOT 与身份分区：详细设计

主方案见[设计主文档](dream-unified-hot.md)。状态：**第一至第三阶段源码切片已实现，尚未部署。** 第三阶段包含 unified versions 回收、持久 intent 恢复、Python 共享读锁，以及授权/回收只读展示。

## 0. 第一阶段源码行为

- Dream 配置新增可选 `hot_root`。它必须是已存在、canonical、owner-private 的绝对目录；省略时旧发布、清理和模型注入行为完全不变。managed service 与 CLI workbench 从已知应用 ROOT 派生推荐路径 `ROOT/dream/hot`，不会创建缺失 root。
- 原有 `<cache-root>/dream-hot/...` 发布成功后，Host 才把同一 bundle 发布到 `<hot_root>/<partition-hash>/versions/<digest>`，然后原子切换相对链接 `current -> versions/<digest>`。`partition.json` 固定记录四维分区、可信 state home、Dream config ID 和各 manifest 原始字节摘要；同一分区出现不同来源会失败，不合并。
- 发布按分区文件锁串行，验证 owner、权限、普通文件、硬链接、固定文件集、manifest、文档字节和分区摘要。链接替换前的失败保留旧 `current`；链接替换后若父目录同步失败，发布结果明确标为不确定并要求重新核验，不能误报回滚。现有模型仍使用内存中的已验证 bundle；统一入口只是查看投影，不授予注入权限。
- 工作台提供 GET-only `/api/dream/partitions` 与 `/api/dream/partition?id=<opaque-id>`。它只读取启动时固定 root，每次请求重新验证 root 的 canonical、owner-private 属性，且 metadata 的 state home 必须已经挂载。cycle/work 返回最近 50 条，candidate/cleanup 在没有可信时间列时只承诺有界的最多 50 条；各类均明确 `has_more/truncated`，cycle 只按该来源 config ID 查询。记录读取同时执行单条与整次响应字节上限，超限显式报错。
- 旧 cache 清理器保持原样。第三阶段另在 sibling `coordination/cleanup/<target-key>/` 保存统一版本回收 intent；它只处理固定 partition 下已验证的 immutable version，不打开原始业务 DB，也不删除 memory、Skill 或授权账本。工作台把未过期且通过校验的内容标为“快照有效”，把过期内容标为“快照过期”；这不是运行时注入授权，授权仍由运行时独立检查。工作台不提供审核、发布、启用或路径选择动作。

## 0.1 第二阶段源码行为与配置

- `sharing_policy` 是 Dream 配置中的显式可选绝对路径；使用它必须同时启用 `hot_enabled` 并配置 `hot_root`。策略和 `users.json` 均须是 canonical、owner-private 文件，并在每次请求/刷新/后台发布时从可信配置位置重读。删除、变化、歧义、来源不可读或绑定错位都会失败，不回退本地 v1 或磁盘旧 `current`。
- 来源只读 API 精确读取 `person + Agent + audience + scope` 下 `active`、未过期、`source_type=dream_reviewed` 的当前 memory revision。每个显式来源按 ID 确定顺序最多读取 200 条，再按策略 kinds 过滤；这个读取预算独立于 Dream 提炼 `batch`，因此较小的提炼批次不会让未挂载 kind 挤掉后续可共享项。SQLite 以 `mode=ro`、`query_only` 打开且不迁移；file-memory 直接有界验证 marker/manifest/current revision，domain 元数据和选中 revision 共用 32 MiB 总预算，不取锁、不创建 cache、不回写来源。
- `dream-hot-v2` 在 v1 文档与 memory manifest 外保存 canonical person、mount、generation、策略/绑定摘要、来源账户/分区/kinds 和 memory revision 引用。正文先按完全相同的 `kind + text` 分组，在实际 `USER/MEMORY/TASKS` 行中以 `[source:<id,...>]` 标注全部来源，再按确定顺序套用 12 KiB 总正文预算；不同文本不做语义融合或冲突裁决。目标仍是策略指定的既有四维分区，旧 DB 的 raw person 和主键不改名。
- 共享仅限私人入口。渠道身份必须逐项等于准入得到的 provider/account/tenant/sender；群聊无 HOT。CLI 只承认硬编码 `local-owner`、`owner` audience 和 canonical `owner`，不能自报其他 person。来源与目标必须同 Agent；`project_fact`、`active_task` 必须同 scope，只有显式列出的 `preference` 可以跨 scope。
- 多来源读取不是跨 DB 全局事务：每个来源分别取得经过前后 manifest 校验的只读快照，然后再次核验策略与绑定摘要。任何一步变化都会中止本次发布。正文、generation 或 binding digest 的任一变化都会改变 runtime fingerprint；已经在途的模型上下文无法被擦除。

最小配置示例（路径必须替换成当前安装的真实 canonical 私有目录）：

`<agents-config-directory>/users.json`：

```json
{
  "version": 1,
  "users": [{
    "id": "alice",
    "accounts": [
      {"channel":"dingtalk","account_id":"ding-app","tenant_id":"tenant-a","sender_id":"ding-alice"},
      {"channel":"feishu","account_id":"fei-app","tenant_id":"tenant-a","sender_id":"fei-alice"}
    ]
  }]
}
```

`/private/config/dream-sharing.json`：

```json
{
  "version": 2,
  "generation": 1,
  "mounts": [{
    "id": "alice-personal",
    "canonical_person_id": "alice",
    "target_state_home": "/private/state/dingtalk",
    "target": {"PersonID":"ding-alice","AgentID":"assistant","AudienceID":"user:alice","Scope":"personal"},
    "target_account": {"channel":"dingtalk","account_id":"ding-app","tenant_id":"tenant-a","sender_id":"ding-alice"},
    "sources": [
      {"id":"ding","state_home":"/private/state/dingtalk","partition":{"PersonID":"ding-alice","AgentID":"assistant","AudienceID":"user:alice","Scope":"personal"},"account":{"channel":"dingtalk","account_id":"ding-app","tenant_id":"tenant-a","sender_id":"ding-alice"},"kinds":["preference","project_fact","active_task"]},
      {"id":"fei","state_home":"/private/state/feishu","partition":{"PersonID":"fei-alice","AgentID":"assistant","AudienceID":"user:alice","Scope":"personal"},"account":{"channel":"feishu","account_id":"fei-app","tenant_id":"tenant-a","sender_id":"fei-alice"},"kinds":["preference","project_fact","active_task"]}
    ]
  }]
}
```

Dream runtime config：

```json
{
  "hot_enabled": true,
  "cache_root": "/private/app/dream/cache",
  "hot_root": "/private/app/dream/hot",
  "sharing_policy": "/private/config/dream-sharing.json",
  "person_id": "ding-alice",
  "agent_id": "assistant",
  "audience_id": "user:alice",
  "scope": "personal",
  "batch": 50
}
```

策略恢复、修改 policy/users 中任何相关内容或重新授权时必须递增 `generation`；完整 policy/users 摘要采用保守语义，同代次任何摘要变化都会持久失效。version 1 共享策略被明确拒绝。canonical owner 的 CLI 策略必须使用 `target_local_owner/source.local_owner=true`、分区 person `local-owner`、audience `owner`，且 `users.json` 中显式存在 ID `owner`。

## 0.2 第三阶段持久协调与撤销源码行为

- 协调根固定从 `hot_root` 的 canonical、owner-private 父目录派生为 sibling `coordination`；不安全或无法派生时失败，不选择其他根。global policy key 是规范策略绝对路径摘要，target key 是现有精确四维分区摘要。每个 target state home 保存 `.dream-coordination/targets/<target-id>.json` anchor，固定绑定 coordination root、hot root、config、policy、state home 和分区；anchor 存在而账本丢失或损坏时 fail closed。
- 锁顺序是 target anchor → global policy → target authorization → partition publication。锁使用 no-follow、owner-private、单硬链接和 inode 重验，并以非阻塞 flock、context 取消及五秒上限等待。锁内先持久接受 policy/target generation 高水位，再读取有界来源快照，随后重读完整 Dream config、policy 和 users，最后生成 bundle 并发布；临时来源错误不会丢失已经接受的高水位。模型执行和提炼不在锁内。
- policy 与 target 账本用连续不可变事件保存完整 next state、前态摘要和单调序号；事件先 fsync，再原子替换并 fsync head。读取逐事件验证 ledger 类型与 anchor 身份，head 必须精确对应其 sequence 的事件。head 落后时只读查询报告 `needs_recovery`，写路径会恢复 head 而不伪造新事件。低 generation 只拒绝；同 generation 的 policy、binding、mount 或 canonical 摘要变化会同时持久 invalid global policy 与 target，恢复旧文件也不能激活，只有更高 generation 可以重新授权。
- policy 删除/解析失败、binding 撤销、关闭 sharing 或显式 `dream revoke` 会使 global policy 与当前 target 失效；global 状态对该 policy 的其他 targets 同样生效。来源数据库临时不可读只使本次发布失败，不永久撤权。目标仍是各自既有分区，账本不合并 audience/scope，也不提供跨 DB 原子快照或 canonical person 目录迁移。
- `agent-host dream authorization --home ... --person ... --agent ... --audience ... --memory-scope ... --format json` 查询有效状态；`dream revoke` 另要求 `--actor` 与 `--note`。两者只从 target home 的受保护 anchor 定位权威账本，不打开业务 DB、不运行模型、不接收 coordination/policy 任意路径。
- shared HOT 顶层 `coordination_authority` 保存 target authority ID/revision 并进入新协调发布的 v2 digest；authority 为空的第二阶段旧 v2 bundle 保持原摘要兼容。native 仍只注入本次锁内验证的内存 bundle，不读取磁盘 `current`。已经在途的 native 模型上下文不会被声称擦除。

## 1. 现有接口证据与缺口

### 1.1 Dream 分区和来源证据

- `DreamPartition` 已明确包含 `PersonID + AgentID + AudienceID + Scope`，收据、候选、work item 和游标也按这四项建索引或唯一键（[`internal/state/dream.go:13-32`](../internal/state/dream.go#L13-L32)、[`internal/state/dream_work.go:25-54`](../internal/state/dream_work.go#L25-L54)）。这可以作为统一分区的现有基础。
- work item 已保存 `SourceKey`、run/session、当前/前次 evidence digest、策略版本、缺失原因、Skill 证据和候选/收据引用；相同 evidence digest 与策略版本不会重复处理（[`internal/state/dream_work.go:25-46`](../internal/state/dream_work.go#L25-L46)、[`internal/state/dream_work.go:70-120`](../internal/state/dream_work.go#L70-L120)）。
- canonical 来源摘要覆盖 run/session/native session、执行与验收状态、输出、事件和工具证据，并形成 `host-run:<run-id>` 稳定来源；提炼输入保留 `host_observed` provenance（[`internal/dream/canonical.go:125-166`](../internal/dream/canonical.go#L125-L166)）。外部 handoff 仍明确标为 `external_agent_supplied`，不会伪装成 Host 观察（[`internal/dream/lifecycle.go:129-142`](../internal/dream/lifecycle.go#L129-L142)）。
- 渠道 Run/Message 已保存 channel、account、tenant、conversation、sender、session 和 run 等原始身份字段（[`internal/state/types.go:181-196`](../internal/state/types.go#L181-L196)、[`internal/state/types.go:212-243`](../internal/state/types.go#L212-L243)）。因此统一 person 不需要、也不应抹除来源身份。

第二阶段的兼容投影保留现有 Dream `PersonID`，另把 canonical person、共享 mount 和授权摘要写入 v2 provenance；它没有把全部旧分区改名为 canonical，也没有给现有 work 幂等键增加全局协调代次。

### 1.2 身份映射

- `users.json` 已用完整 `channel/account_id/tenant_id/sender_id` 作为 operator-verified account binding，并明确禁止从模型输出或聊天姓名推断；同一账户不能重复绑定（[`internal/cli/users.go:21-47`](../internal/cli/users.go#L21-L47)、[`internal/cli/users.go:78-103`](../internal/cli/users.go#L78-L103)）。
- 解析会阻止 owner 被改绑、阻止非 owner 获取 legacy owner 分区；未配置账户会退化为账户摘要生成的本地 ID（[`internal/cli/users.go:143-160`](../internal/cli/users.go#L143-L160)）。观察到的绑定以不可变 receipt 独立留存，改绑不覆盖旧证据（[`internal/cli/users.go:177-227`](../internal/cli/users.go#L177-L227)）。
- 当前私聊 audience 直接由解析出的用户 ID 形成 `user:<id>`，并可选择该用户的 `USER.md`（[`internal/cli/users.go:271-285`](../internal/cli/users.go#L271-L285)、[`internal/cli/users.go:288-329`](../internal/cli/users.go#L288-L329)）。

缺口：当前映射和私聊 audience/profile 选择仍较紧密，没有独立的共享挂载授权对象；未配置账户自动生成摘要 ID 也不能作为跨渠道同一 person 的证明。本设计要求只有显式绑定可以让多个原生身份收敛到同一 canonical person，显示名和 aliases 仅供展示/通知检索，永不参与绑定。

### 1.3 HOT 发布与会话失效

- HOT export 已从有效记忆生成 `USER.md`、`MEMORY.md`、`TASKS.md`，manifest 记录 memory ID/revision/kind/expiry，并对完整 bundle 求摘要（[`internal/dream/lifecycle.go:276-324`](../internal/dream/lifecycle.go#L276-L324)）。
- 旧 `PublishHOT` 继续在 `<cache-root>/dream-hot/<partition-hash>/<digest>/` 创建并验证不可变快照；配置 `hot_root` 时，新增 `PublishCurrent` 再发布统一版本和稳定 `current`。旧清理器仍只处理 cache 派生快照，并保护当前摘要、记录 durable cleanup intent（[`internal/dream/cleanup.go:22-39`](../internal/dream/cleanup.go#L22-L39)、[`internal/dream/cleanup.go:76-120`](../internal/dream/cleanup.go#L76-L120)）。
- Full Host 把 person 与 HOT digest 纳入 runtime fingerprint，并为一次已准入请求冻结文档（[`internal/host/host.go:86-136`](../internal/host/host.go#L86-L136)）。Offload channel 同样把 HOT digest 纳入 session fingerprint，并在摘要变化时拒绝继续旧 session（[`internal/offloadchannel/host.go:141-182`](../internal/offloadchannel/host.go#L141-L182)、[`internal/offloadchannel/host.go:425-448`](../internal/offloadchannel/host.go#L425-L448)）。
- 运行时只在 person、Agent、audience、scope 全部匹配且是私人入口时绑定 HOT；不匹配和群聊不加载（[`internal/cli/dream_runtime.go:289-345`](../internal/cli/dream_runtime.go#L289-L345)）。

共享摘要包含 policy、binding、generation 和持久 authority revision；因此正文不变时授权变化仍会轮换会话。持久单调撤销账本和跨进程协调已在第三阶段首个切片实现；已在途执行仍不声称能擦除上下文。

### 1.4 CLI 与本地工作台

- CLI 已有精确分区的 `status`、`list/show`、`work-list/work-show`、`cleanup-list` 和 `export`，其中查询路径以只读 Store 打开（[`internal/cli/dream_lifecycle.go:19-81`](../internal/cli/dream_lifecycle.go#L19-L81)、[`internal/cli/dream_lifecycle.go:120-200`](../internal/cli/dream_lifecycle.go#L120-L200)）。
- 工作台监听 `127.0.0.1`，GET 要求 loopback Host；静态页面与 GET API 是显式 allowlist（[`tools/run-viewer/server.py:920-1007`](../tools/run-viewer/server.py#L920-L1007)、[`tools/run-viewer/server.py:1136-1176`](../tools/run-viewer/server.py#L1136-L1176)）。写请求另受显式路由 allowlist、same-origin、随机 token 和长度限制约束（[`tools/run-viewer/server.py:1020-1034`](../tools/run-viewer/server.py#L1020-L1034)）。服务启动器会移除 provider 凭据并校验 loopback URL（[`internal/cli/service_workbench.go:93-124`](../internal/cli/service_workbench.go#L93-L124)、[`internal/cli/service_workbench.go:147-164`](../internal/cli/service_workbench.go#L147-L164)）。

第一阶段已补齐 Dream GET/静态 allowlist和不透明分区选择；第二阶段页面验证并展示 v2 canonical person、来源账户/分区和 memory revision 引用，但这些路径只作 manifest 元数据，绝不用于打开额外 DB。

## 2. 规范数据模型

### 2.1 四维分区

逻辑键固定为：

```text
Partition = canonical_person_id + agent_id + audience_id + scope_id
```

- `canonical_person_id`：Host 管理的稳定、不透明 ID，不使用显示名。
- `agent_id`：业务 Agent 身份；同一 person 的不同 Agent 默认隔离。
- `audience_id`：owner 私聊、指定用户私聊、群聊或其他受众；群聊绝不退化为 person 私聊。
- `scope_id`：项目/工作范围的稳定身份，不以路径字符串相似性推断相同 scope。

磁盘 `opaque-partition-id` 是四维键加命名空间版本的不可逆摘要或 Host 分配 ID。manifest 必须同时保存四维逻辑键和生成算法版本；目录名命中但 manifest 不匹配时 fail closed。

### 2.2 身份绑定与共享挂载分离

身份绑定记录至少包含：

```text
binding_id, channel, account_id, tenant_id, native_user_id,
canonical_person_id, status, generation, actor, created_at, revoked_at
```

完整四元组必须精确匹配；重复绑定、歧义绑定、被撤销绑定或只给姓名/昵称时拒绝解析。历史绑定记录不可覆盖，用状态和新 generation 表达撤销或替换。

共享挂载另存：

```text
mount_id, canonical_person_id, target_agent_id, target_audience_id,
target_scope_id, source_partition, allowed_kinds, status,
generation, actor, policy_digest
```

约束如下：

1. binding 不自动创建 mount；知道“是谁”不等于允许读取任何其他来源。
2. `working_preference` 以及个人事实/任务只有经现有审核接受，且 mount 明确列出允许的种类与来源时才可跨渠道投影。个人事实/任务是产品范围描述，落地时映射现有 memory kind，不预设新增枚举。
3. knowledge/task 默认不因 person 相同而共享；只有明确授权的个人范围条目可以共享，项目知识必须保持原 scope，群聊内容保持原 audience。
4. mount 必须指定目标 Agent；不支持“该 person 的所有 Agent”。
5. 来源文本、模型输出、页面选择和 Skill 都不能创建或扩大 binding/mount。

## 3. 来源、投影与兼容

### 3.1 保留来源

统一投影中的每个条目必须能回到：原 channel/account/tenant/native user、原 audience/scope、Host run/session/native session（如有）、receipt/work/candidate/memory revision、来源 digest、provenance 等级、binding generation 和 mount generation。显示层可以折叠这些字段，但不得丢弃或改写。

同一事实来自多个渠道时，只在投影层合并展示/去重；来源集合保持逐条可检查。执行完成、业务接受、候选审核、记忆发布、Skill review/publish/enable 仍是不同状态。

### 3.2 不物理合并旧 DB

每个现有渠道 DB 和 CLI state 保持原地、原 schema 和原主键。兼容适配器只读各来源：

1. 读取现有精确 person/Agent/audience/scope 记录和来源身份；
2. 用当前有效 binding 解析 canonical person；
3. 用当前有效 mount 判断能否投影到目标分区；
4. 生成带来源引用的统一候选集合；
5. 将选中版本发布为派生 HOT，不回写或重标旧行。

适配器必须可关闭。关闭后运行时回到当前渠道本地读取路径，旧 DB 无需反向迁移。若来源 schema、binding 或 mount 无法验证，跳过并显式记录 missing/blocked，不使用较弱身份来源兜底。

## 4. HOT 文件布局和发布协议

第一阶段固定在显式 `hot_root`（推荐应用数据根的 `dream/hot`）内：

```text
dream/hot/<opaque-partition-id>/
  partition.json
  versions/<hot-digest>/
    MEMORY.md
    USER.md
    TASKS.md
    manifest.json
  current -> versions/<hot-digest>/
```

`current` 是 Host 管理的内部原子指针；调用方始终读取 `current/{MEMORY,USER,TASKS}.md`，不能提交或覆盖其目标。具体平台若不使用符号链接，应提供等价的原子目录指针，但不能逐文件切换而产生混合版本。

发布顺序：

1. 在同一文件系统写入新的 `versions/<digest>` 临时目录，逐文件 `fsync`，生成含完整分区、来源版本、策略摘要、binding/mount generation、expiry 和内容 digest 的 manifest。
2. 校验 owner、权限、普通文件、无越界链接、manifest 与正文摘要；将完整目录原子发布为不可变版本。
3. 再次读取当前授权和来源版本；任何变化都放弃切换。
4. 原子替换 `current` 指针并同步父目录。
5. 写入 cycle/cleanup 观察记录。观察记录失败必须显式报告；不得声称未能证明的发布成功。

快照发布后禁止修改。重复 digest 必须逐字验证后复用；内容不一致视为损坏。写入、校验或链接替换前失败时保留上一个已验证 `current`。链接替换完成后若父目录同步失败，磁盘状态可能已提交，错误必须区分为“结果不确定、必须重新核验”，不得声称旧版仍在或已回滚。不存在有效旧版本时应无 HOT，而不是退回陈旧、越权或未审核内容。

现有 `<cache-root>/dream-hot/<partition-hash>/<digest>` 目录只作为旧格式来源。Phase 1 可从它生成新固定入口，但不把任一渠道的“current”当作合并权威；统一 `current` 必须由统一投影重新验证并发布。

## 5. 单协调者、幂等和冲突

一个逻辑协调者拥有某个目标分区的共享写入 lease；不同渠道采集器只能提交带 provenance 的来源观察，不能各自发布统一 `current`。协调范围是四维 Partition，不是渠道。

建议幂等键：

```text
sha256(target_partition || sorted(source_identity + source_version) ||
       projection_policy_digest || binding_generation || mount_generation)
```

相同键返回既有结果；相同键不同 payload 明确冲突。来源版本、策略、映射或挂载代次变化才产生新投影尝试。协调者串行执行候选比较、冲突检查和发布；并发唤醒只能等待或观察同一结果，不能双写。

内容冲突、不确定归属、跨 scope、证据不完整或 provenance 降级时保持 pending/missing/conflicted，并进入人工审核。现有候选审核与 Skill review → publish → enable 门槛不变；统一投影不直接接受候选、不发布或启用 Skill，也不把 UI 查看当审核。

## 6. 撤销和 native 会话回收

撤销 binding 或 mount 时：

1. 先持久记录撤销和新 generation；之后所有新解析/加载立即排除该授权。
2. 标记受影响分区待重投影；协调者从仍有效来源生成新快照并切换 `current`。重建失败时，旧 `current` 可以保留作审计文件，但 loader 必须因 generation 过期拒绝加载，不能为可用性继续注入。
3. runtime fingerprint 同时包含 HOT digest、binding generation、mount generation 和授权策略摘要。即使正文 digest 未变，撤销也改变指纹。
4. 继续已有 Host/native session 时重新比对指纹；不匹配即拒绝 continuation，要求新会话。不能依靠“下一轮自然刷新”让已撤销上下文继续存在。
5. 后续清理只回收已无引用、超过保留期且 manifest 完整验证的派生版本；原始 DB、run/session、binding receipt、审核和 provenance 不随快照删除。

## 7. Dream 只读页面和接口

在现有本地工作台增加 Dream 页面，但第一阶段只读。页面展示：

- 可用分区选择器（显示安全标签，提交 opaque partition ID）；
- 当前 HOT digest、生成时间、授权代次和各文档内容/摘要；
- 版本及来源：channel、run/session、receipt/work/candidate/memory revision、provenance；
- cycle、work、missing/retryable、cleanup 记录；
- 关联 Skill 草稿的现有只读详情链接。

接口只接受服务启动时配置并验证过的 `dream_root`，例如只读 `GET /api/dream/partitions`、`GET /api/dream/partition?id=<opaque-id>`。请求不接受 `path`、`root`、`home`、相对路径或文件名参数；服务端以 allowlist ID 查 manifest，再验证解析结果仍位于固定根目录。响应有条数/字节上限，读取错误和陈旧授权显式返回。

页面沿用现有 loopback Host、CSP、no-store 和本地 owner 边界。Dream GET 能查看记录不代表 runtime loader 可以注入该分区；loader 仍独立检查可信调用身份、Agent/audience/scope、binding/mount generation 和策略摘要。

本设计不新增 `review/accept/reject/publish/enable` POST。已有 CLI 或既有受控流程继续负责审核；若未来确有写需求，应单独设计，而不是把只读页面的 mutation token 当作审批授权。

## 8. 分阶段验收与回退

### Phase 1：单分区 `current` 与只读页面

- 从一个现有精确分区生成 immutable version 并原子切换统一 `current`。
- 页面只接收 opaque partition ID，能查看 HOT、版本来源、cycle/work/missing/cleanup，以及 manifest memory 版本和 Skill 草稿 ID/revision/status。当前快照的更新时间只展示文件系统 mtime，并明确它不是内容生成时间。
- 证明链接替换前失败、损坏版本和超界路径不会替换旧 `current`，并证明替换后同步失败会报告不确定状态；关闭功能即可回到现有 HOT 路径。

### Phase 2：显式身份、共享挂载与跨源投影

- 源码仅用精确 binding 解析 canonical person；姓名、alias 和未配置账户不能跨渠道合并。
- binding 不自动共享；只有显式 mount 允许的已审核个人偏好、个人事实或任务可跨渠道进入目标既有分区。
- 群聊、项目 scope 和 Agent 交叉测试均 fail closed；只读适配器不迁移旧 DB 或创建 file-memory cache。

### Phase 3：协调去重、撤销回收与验证（源码已实现，未部署）

- 同一既有 target 的并发共享发布按高水位串行，旧请求不能覆盖较新 generation，幂等 bundle 不重复写账本事件。不同 targets 仍各自生成兼容投影，不声称是一次跨 DB/target 发布。
- 撤销 binding/mount 后新加载立即失败或排除，旧 native session 因授权指纹变化拒绝继续。
- 重建失败不加载旧权限，清理失败保留最后有效且仍获授权的版本；原始证据和审核记录保持可追溯。

## 9. 未决风险（不扩展本期架构）

1. **canonical person 全局目录：** 当前 Host-owned 单调账本协调既有 target 投影，但不把旧分区重命名或合并成一个 canonical person DB/目录；自动摘要 ID 仍不能证明跨渠道同一人。
2. **`current` 的跨平台原子表示：** 第一阶段使用 Unix 受控相对 symlink；其他平台仍需验证等价目录指针，验收标准是读者只能看到完整旧版或完整新版。
3. **统一版本保留：** 清理器保护 `current` 和未满保留期的版本，严格验证 manifest、文件、owner、权限、硬链接数及 inode/dev 后才隔离并逐文件删除；异常版本只报告跳过。intent 或完成状态同步失败会报告不确定，不捏造成功。
4. **撤销后的在途请求：** 已冻结并正在执行的请求无法从 native model 上下文中擦除内容；最小安全语义应是阻止后续 continuation 和新派发，并在撤销审计中记录可能已暴露的 run/session。是否主动取消在途执行需要单独策略决定。
5. **读者与跨库边界：** Python 工作台只打开既有 `.publish.lock` 并短时持有共享锁，将 metadata、current manifest 和三份文档读入内存后释放；发布和回收持有排他锁。授权展示按 policy→target 顺序进行稳定双采样，并按 Go 账本规则计算 effective invalid；缺失或落后的 head 仅在完整事件链验证后显示 `needs_recovery`。回收恢复若发现 source 与 quarantine 均已缺失，只记录“既往删除结果未知”的 skipped 状态，不伪报 completed。该锁不是跨多个来源 DB 的事务或长期租约，records 查询在释放文件锁后独立执行并可独立报错。
