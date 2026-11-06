# Dream 统一 HOT 与身份分区：详细设计

主方案见[设计主文档](dream-unified-hot.md)。状态：**第一阶段已在源码实现，尚未部署；第二、三阶段仍仅设计。** 本稿区分当前单精确分区投影与尚未交付的身份、挂载和协调能力。

## 0. 第一阶段源码行为

- Dream 配置新增可选 `hot_root`。它必须是已存在、canonical、owner-private 的绝对目录；省略时旧发布、清理和模型注入行为完全不变。managed service 与 CLI workbench 从已知应用 ROOT 派生推荐路径 `ROOT/dream/hot`，不会创建缺失 root。
- 原有 `<cache-root>/dream-hot/...` 发布成功后，Host 才把同一 bundle 发布到 `<hot_root>/<partition-hash>/versions/<digest>`，然后原子切换相对链接 `current -> versions/<digest>`。`partition.json` 固定记录四维分区、可信 state home、Dream config ID 和各 manifest 原始字节摘要；同一分区出现不同来源会失败，不合并。
- 发布按分区文件锁串行，验证 owner、权限、普通文件、硬链接、固定文件集、manifest、文档字节和分区摘要。链接替换前的失败保留旧 `current`；链接替换后若父目录同步失败，发布结果明确标为不确定并要求重新核验，不能误报回滚。现有模型仍使用内存中的已验证 bundle；统一入口只是查看投影，不授予注入权限。
- 工作台提供 GET-only `/api/dream/partitions` 与 `/api/dream/partition?id=<opaque-id>`。它只读取启动时固定 root，每次请求重新验证 root 的 canonical、owner-private 属性，且 metadata 的 state home 必须已经挂载。cycle/work 返回最近 50 条，candidate/cleanup 在没有可信时间列时只承诺有界的最多 50 条；各类均明确 `has_more/truncated`，cycle 只按该来源 config ID 查询。记录读取同时执行单条与整次响应字节上限，超限显式报错。
- 第一阶段不清理 unified `versions`，避免误删 `current`。旧 cache 清理器保持原样。工作台把未过期且通过校验的内容标为“快照有效”，把过期内容标为“快照过期”；这不是运行时注入授权，授权仍由运行时独立检查。工作台不提供审核、发布、启用或路径选择动作。

## 1. 现有接口证据与缺口

### 1.1 Dream 分区和来源证据

- `DreamPartition` 已明确包含 `PersonID + AgentID + AudienceID + Scope`，收据、候选、work item 和游标也按这四项建索引或唯一键（[`internal/state/dream.go:13-32`](../internal/state/dream.go#L13-L32)、[`internal/state/dream_work.go:25-54`](../internal/state/dream_work.go#L25-L54)）。这可以作为统一分区的现有基础。
- work item 已保存 `SourceKey`、run/session、当前/前次 evidence digest、策略版本、缺失原因、Skill 证据和候选/收据引用；相同 evidence digest 与策略版本不会重复处理（[`internal/state/dream_work.go:25-46`](../internal/state/dream_work.go#L25-L46)、[`internal/state/dream_work.go:70-120`](../internal/state/dream_work.go#L70-L120)）。
- canonical 来源摘要覆盖 run/session/native session、执行与验收状态、输出、事件和工具证据，并形成 `host-run:<run-id>` 稳定来源；提炼输入保留 `host_observed` provenance（[`internal/dream/canonical.go:125-166`](../internal/dream/canonical.go#L125-L166)）。外部 handoff 仍明确标为 `external_agent_supplied`，不会伪装成 Host 观察（[`internal/dream/lifecycle.go:129-142`](../internal/dream/lifecycle.go#L129-L142)）。
- 渠道 Run/Message 已保存 channel、account、tenant、conversation、sender、session 和 run 等原始身份字段（[`internal/state/types.go:181-196`](../internal/state/types.go#L181-L196)、[`internal/state/types.go:212-243`](../internal/state/types.go#L212-L243)）。因此统一 person 不需要、也不应抹除来源身份。

缺口：当前 Dream `PersonID` 是调用方提供的精确值，尚没有 Dream 专用的“canonical person + 共享挂载 + 授权代次”投影层；现有 work 幂等键也没有身份映射版本或共享策略摘要。

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

缺口：当前摘要不包含独立的 identity/mount authorization generation，撤销授权但内容摘要恰好未变时，不能仅依赖文档 digest 证明旧会话已失效。第一阶段固定入口只是只读投影，尚未成为带授权代次的 runtime loader。

### 1.4 CLI 与本地工作台

- CLI 已有精确分区的 `status`、`list/show`、`work-list/work-show`、`cleanup-list` 和 `export`，其中查询路径以只读 Store 打开（[`internal/cli/dream_lifecycle.go:19-81`](../internal/cli/dream_lifecycle.go#L19-L81)、[`internal/cli/dream_lifecycle.go:120-200`](../internal/cli/dream_lifecycle.go#L120-L200)）。
- 工作台监听 `127.0.0.1`，GET 要求 loopback Host；静态页面与 GET API 是显式 allowlist（[`tools/run-viewer/server.py:920-1007`](../tools/run-viewer/server.py#L920-L1007)、[`tools/run-viewer/server.py:1136-1176`](../tools/run-viewer/server.py#L1136-L1176)）。写请求另受显式路由 allowlist、same-origin、随机 token 和长度限制约束（[`tools/run-viewer/server.py:1020-1034`](../tools/run-viewer/server.py#L1020-L1034)）。服务启动器会移除 provider 凭据并校验 loopback URL（[`internal/cli/service_workbench.go:93-124`](../internal/cli/service_workbench.go#L93-L124)、[`internal/cli/service_workbench.go:147-164`](../internal/cli/service_workbench.go#L147-L164)）。

第一阶段已补齐 Dream GET/静态 allowlist和不透明分区选择；身份映射、共享挂载和授权代次仍未实现。

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

- 只有精确 binding 能解析 canonical person；姓名、alias 和未配置账户不能跨渠道合并。
- binding 不自动共享；只有显式 mount 允许的已审核个人偏好、个人事实或任务可跨渠道进入目标分区。
- 群聊、项目 scope 和 Agent 交叉测试均不泄漏；旧 DB 保持字节/主键不变，关闭适配器可回退。

### Phase 3：协调去重、撤销回收与验证

- 两渠道同时提交相同来源版本只产生一次整理/发布；相同幂等键不重复生成候选或快照。
- 撤销 binding/mount 后新加载立即失败或排除，旧 native session 因授权指纹变化拒绝继续。
- 重建失败不加载旧权限，清理失败保留最后有效且仍获授权的版本；原始证据和审核记录保持可追溯。

## 9. 未决风险（不扩展本期架构）

1. **canonical person 管理入口：** 现有 `users.json` 可提供显式绑定基础，但是否直接演进该格式或增加 Host-owned binding ledger，需要实现阶段依据可回退性决定；无论选择哪种，不能用自动摘要 ID 证明跨渠道同一人。
2. **`current` 的跨平台原子表示：** 第一阶段使用 Unix 受控相对 symlink；其他平台仍需验证等价目录指针，验收标准是读者只能看到完整旧版或完整新版。
3. **统一版本保留：** 第一阶段故意不清理 `hot_root` 下的 immutable versions。后续清理器必须解析并保护已验证的 `current` 目标后才能启用。
4. **撤销后的在途请求：** 已冻结并正在执行的请求无法从 native model 上下文中擦除内容；最小安全语义应是阻止后续 continuation 和新派发，并在撤销审计中记录可能已暴露的 run/session。是否主动取消在途执行需要单独策略决定。
5. **多 DB 协调 lease 的持久位置：** 必须有一个 Host-owned 权威协调点，但不应通过合并旧 DB 实现。实现前需选定已有服务 Store 或独立派生状态目录，并验证崩溃恢复和单写者行为。
