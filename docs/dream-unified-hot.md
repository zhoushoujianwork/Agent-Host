# Dream 统一 HOT 与身份分区

状态：**第一、二阶段已在源码实现，尚未部署。** 当前源码可为一个现有精确目标分区发布统一本地 `current`，并在本地工作台只读查看；第二阶段还可依据显式 `users.json` 绑定和独立共享策略，把同一 canonical person 的已审核来源投影到该目标。它不迁移旧分区、不提供生产启用，也没有第三阶段的持久撤销账本、全局协调或统一版本淘汰。现有 Dream 的其他已交付范围仍以[梦境记忆轮转](dream-memory-rotation.md)和[增量整理](dream-incremental-learning.md)为准；实现边界和后续风险见[详细设计](dream-unified-hot-detail.md)。

## 目标

让同一个规范 person 在不同渠道中，在明确授权下使用同一份个人 HOT，同时继续以 `person + Agent + audience + scope` 隔离记忆。渠道身份是来源和认证依据，不是唯一的数据边界；身份绑定本身不授予共享，群聊、项目和其他 Agent 的内容也不会因此串联。

## 使用方式

1. 操作者在可信 Agent 配置同目录的 `users.json` 中，把完整 `channel/account_id/tenant_id/sender_id` 显式绑定到 canonical person；姓名、昵称、自动账户摘要和请求参数都不能建立共享身份。
2. Dream 配置另行指定只读 `sharing_policy`。策略把一个既有精确目标分区绑定到显式来源分区；身份绑定本身不共享，群聊不共享，事实和任务不能跨 scope，偏好也只有策略显式列出来源时才能跨 scope。共享正文逐行标注来源 ID；每个来源最多读取 200 条当前记忆后再按 kinds 过滤，该上限独立于提炼 `batch`。
3. 配置 `hot_root` 后，Host 把已验证的现有 HOT bundle 投影到固定入口：

   ```text
   <app-data-root>/dream/hot/<opaque-partition-id>/current/{MEMORY,USER,TASKS}.md
   ```

   模型注入仍直接使用本次请求验证并生成的 bundle，不从 `current` 反向加载。
4. 本地工作台的 `/#dream` 页面选择分区后，只读查看当前 HOT、版本来源、cycle/work/missing/cleanup 记录和 Skill 草稿标识。查看页面不等于允许把内容注入 Agent。

## 方案

- 最终统一分区按规范 person、Agent、audience、scope 定位；当前兼容投影保留旧目标四维键，另用 canonical person 校验共享授权。`opaque-partition-id` 由 Host 生成，只用于磁盘定位，不暴露原始渠道身份，也不替代分区元数据校验。
- 身份映射和共享挂载是两项独立决定。映射只回答“这个渠道主体是谁”；挂载另行回答“这个运行分区可以读取哪些已授权个人内容”。拒绝隐式跨 Agent、跨 audience 或跨 scope 共享。
- 每条源记录保留原 channel、account、tenant、native user、run/session 和 provenance。旧数据库不物理合并；通过可回退的只读投影适配到规范 person 分区。
- 每个分区发布不可变快照，再原子切换 `current`。切换前失败会保留上一个已验证版本；若链接替换已完成但父目录同步失败，结果明确报告为“不确定、必须重新核验”，不声称已经回滚。不能就地修改快照，也不能继续加载陈旧或已撤销权限产生的投影。
- 第三阶段将由一个逻辑协调者按规范分区串行共享写入，跨渠道以来源版本和策略摘要幂等，避免重复整理或双重发布。候选、冲突、审核、发布和启用继续使用现有门槛，不因统一视图而放宽。
- 映射或挂载撤销后，新加载立即失败或排除相关来源；权限代次进入运行时指纹，已有 native 会话不能继续携带撤销前上下文，必须新建兼容会话。

## 范围与边界

本设计包含身份解析、授权挂载、旧数据投影、统一 HOT 发布、撤销回收和只读 Dream 页面。它不物理迁移或合并旧 DB，不自动合并冲突，不新增通用插件 API，不增加审批写接口，不允许页面提交任意文件路径，也不改变工具、Skill、渠道或项目权限。

页面沿用现有 loopback、本地 owner 与同源动作边界。服务端只接收预配置的应用数据根和不透明分区选择；UI 能查看某分区不代表该分区已获运行时注入授权。

## 实施顺序

1. **单分区入口与只读页面（源码已实现，未部署）：** 在 Dream 配置中显式设置既存、owner-private 的绝对 `hot_root`（推荐 `<ROOT>/dream/hot`），为一个现有精确分区生成不可变快照和原子 `current`。工作台只从固定 root 和已挂载 state home 读取，不接受任意路径。统一版本清理本阶段不启用。
2. **显式身份与共享挂载（源码已实现，未部署）：** `users.json` 提供 canonical person 的显式账户绑定，独立 `sharing_policy` 精确列出目标、来源与 kinds。每次请求和后台发布都重新验证策略、绑定及来源；授权摘要进入 HOT digest，因此正文不变而代次或绑定改变时也会轮换 native 会话。
3. **持久撤销、全局协调与回收（未实现）：** 增加持久单调撤销账本、跨进程协调和 unified versions 淘汰；在此之前，重新授权必须由操作者递增 `generation`，且不能宣称清除了已在途模型上下文。

每阶段都应可单独关闭并回到现有按渠道数据读取方式；没有完成隔离、回退和撤销验证前，不进入下一阶段或生产启用。
