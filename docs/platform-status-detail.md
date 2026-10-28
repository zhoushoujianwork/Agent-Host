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
