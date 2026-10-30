# 平台状态技术说明

[使用说明](platform-status.md)。实现位于 `internal/stats/status.go`，复用
`stats` 的目录发现、半开窗口与错误分类。JSON `schema_version` 为 1；
消费者按 `checks[].id`、`scope` 和 `status` 判断，不解析消息文字。

- 总体状态优先级：`critical`（检查失败）、`attention`（警告）、
  `unknown`（有未知项）、`ok`。进程和部署模式当前始终未知。
- Host 存储要求当前 schema，采用 SQLite `mode=ro`、`query_only`、
  200 ms busy timeout。每个检查反映其查询时的观测，并非跨库原子快照。
- `runs.window`、`runs.failures` 使用窗口数据；不可读来源被排除并标记覆盖未知。
  `runs.stale_running` 与 `runs.unrecorded` 查询全部保留记录，示例最多 20 个。
  running 恰好等于阈值不算过期；unknown 执行状态表示未记录终态。
- intake 非 completed/failed 为未完成，unknown 为歧义 intake；失败的
  不支持输入已记录终态，不重复计为未完成。只显示数量和年龄。
- 根目录还发现 `channels/*/dws/host-operations.db`，completed、failed、
  partial_failure 为终态，其余报告为积压；年龄不自动推断业务操作过期。
- 反馈读取 `ROOT/feedback/state.db` 中 needs_recipient 项，年龄使用 updated_at；
  记忆候选读取 pending 的 created_at。缺表或不可读时为未知。
- 显式 home 符合 cli-state 或 channels/NAME/state 布局时，可推导关联根目录；
  其他 home 不猜测根目录，辅助检查为未知。
- 现有记忆验证器不保存只读验证记录，状态命令给出独立执行验证的建议，
  不调用验证器。验证命令自身可能重建缓存，不能视为状态查询的一部分。
- 备份调用 `backup.ReadStatus`，只输出 enabled、最后成功年龄和是否有错误；
  不输出目的地、归档路径或错误正文。
- 目录发现按字面路径处理，包括根目录名称中的方括号等 glob 字符。
- disk 输出数据库/WAL 大小与 mtime；扫描 `ROOT/logs/` 和
  `ROOT/channels/NAME/logs/`，服务日志合计最多 100 个文件元数据，
  不输出文件名、内容或增长率。不存在的文件证据为 null。
- HTTP 只请求 127.0.0.1，2 秒超时，不使用代理、不跟随重定向、不读取正文。
  返回状态码，非 200 或请求失败为警告；禁用时为未知。
- 回复交付、已读、提供方可达性、业务验收、缺测 usage/cost 和调度证据
  保留在 unverified。只读状态不提供修复、重放、部署或重启行为。

## 审计窗口与阶段覆盖

`--until` 为排他上界，默认当前时间；`--since` 支持 RFC3339、UTC 日期或
相对上界的时长，覆盖 `--window`。两者均按纳秒精度比较。
当前积压检查仍查询全部保留记录，不因历史窗口改变。

- `intake.window` 按消息 received_at 选取，分母为消息数；分别统计处理和
  reply_status。accepted 仅表示平台接受回复，不表示已读或业务验收。
- `runs.stages` 按 run created_at 选取，分母为执行数；分别统计 memory_status
  与 experience_status。缺失或不识别的状态进入 unknown，不推断成功。
  执行结果仍由 `runs.window` 提供；错误或不可读表返回 unknown/null。
- 两项检查的 error_observations 是非空错误字段的分类次数；同一记录可有
  两项错误，不能当作失败记录数或可靠性百分比。仅输出固定类别，不输出正文。
  HTTP 400/402 为 dependency_http_400/402；不触发路由、重试或提供方切换。
- `stages.latency` 统计窗口内创建的执行所保留的 phase.finished 事件。
  只允许 Host 定义的阶段名；每阶段独立报告 observations、timed_observations、
  total_seconds 和 max_seconds。重复阶段分别计数，缺失或负耗时不计入耗时分母；
  没有有效耗时为 null。此指标不是消息端到端延迟，也不覆盖无事件的阶段。
- `coverage` 明示中央 usage/cost unavailable、业务验收记录 not_implemented、
  调度配置与已读证据 unavailable。未读取调度配置，不能推断 not_configured；
  stats 的部分用量仍只是观测小计，不是中央总成本。
- 后续业务验收需要独立的归属、授权和验收证据记录设计；不得由执行完成、
  经验保存或平台回复 accepted 自动回填历史 accepted/rejected。

隔离测试覆盖精确上下界、空窗口、取消、不支持输入、后处理失败、歧义 intake、
依赖失败、缺失阶段耗时、文本/JSON 脱敏，以及既有数据库/WAL/锁不变检查。
