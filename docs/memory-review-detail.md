# 候选审查报告实现说明

[操作说明](memory-review.md)。入口为 `memory review-report`，要求显式 `--home`，
直接以 SQLite `mode=ro`、`query_only` 和 200 ms busy timeout 读取当前 schema 的 state.db；
不初始化 Host、不迁移、不取得 host.lock、不访问模型或发布存储。

- JSON schema_version=1；--limit 为 1–100，默认 20；--min-age 为非负 Go duration，默认 168h。
  --status 支持 pending（默认）、accepted、rejected、all。先筛选再按创建时间和 ID 排序，读取 limit+1 判定截断。
- age_seconds 使用生成时间减 created_at；未知、无效或未来时间为 null，仍纳入报告，避免静默丢失。
- scope_ref 和 partition_ref 为完整 SHA-256 指纹，可比较一致性，不暴露工作目录或私人分区标识。
  指纹不是匿名化保证；来源运行 ID 和候选 ID 仅用于授权环境中定位证据。
- 只选择元数据，不输出标题、正文、条件、模型评估、证据正文/工具参数、发送者、审查备注内容。
  has_review_note 只表示备注存在；source_evidence_count 只表示引用数，不验证其正确性。
- run_experience_state 原样报告运行保存的经验处理状态；它可能综合提炼和审查，不能当作独立提炼完成凭据。
  extraction_completion 与 publication 保持 unverified，accepted 不推断正式存储已发布。
- supersession、intentional_withholding、review_owner 保持 unverified：当前 schema 没有结构化证明。
  合成测试中的带取代说明的 rejected 候选仍为 rejected，并隐藏备注；accepted 和 pending 分别覆盖。
- 数据库不存在、schema 不支持、读取失败均返回错误，无自动恢复。只读测试对比数据库和 WAL 字节并检查无 Host 锁。
