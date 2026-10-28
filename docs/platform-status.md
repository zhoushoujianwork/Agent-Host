# 平台当前状态

源码现已提供只读 `agent-host status`，汇总当前状态与待关注事项；
历史窗口统计继续使用 `agent-host stats`。源码实现不代表生产运行时已升级。
[技术说明](platform-status-detail.md) 列出检查口径与限制。

```sh
agent-host status --root /absolute/runtime --format json
agent-host status --home /absolute/runtime/channels/dingtalk/state --no-http
```

必须显式指定绝对运行根目录，或重复指定状态目录。默认窗口为 24 小时，
运行超时观察阈值为 2 小时，工作台端口为 8765；支持 `--window`、
`--stale-after`、`--viewer-port`、`--no-http` 和 `--format text|json`。

报告区分执行、业务验收、回复交付与记忆状态。未验证的业务验收不是告警；
缺失证据为未知，不解释为零或未配置。最新执行的年龄只表示活动时间，
不证明服务存活。进程归属与部署模式仍待 #11 实现。

不启动 Host、不获取 Host 锁、不迁移或修复数据库，不输出消息、身份、
操作标识或凭证。只读取状态与文件元数据，并可向回环工作台发送一次 GET。
有可评估数据源时，关注事项不影响退出码；参数错误或全部数据源不可评估才失败。
