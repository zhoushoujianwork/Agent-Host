# 数据源插件配置实现约定

状态：待实现。[主设计](datasource-plugins-design.md)定义用户行为与实施范围。
本稿只描述后续改动，不修改现行运行配置。当前加载器使用 `plugins.email.json`
与 `plugins.knowledge.json`；知识 provider 为 `local_markdown`、`dokki`、`confluence`。

## 类型与实例

配置名遵循 `datasource.plugins.<plugin-id>.json`。首期只注册
`local-documents`、`dokki`、`confluence`、`email`，未知 ID 或拼写错误明确失败。
一个插件对应一个配置文件，来源实例放在文件内部，不通过增加文件后缀表示实例。

| plugin ID | 复用实现 | settings |
| --- | --- | --- |
| `local-documents` | `internal/knowledge` 的 local_markdown adapter | sources |
| `dokki` | 现有 Dokki adapter | sources |
| `confluence` | 现有 Confluence adapter | sources |
| `email` | `internal/email` 的账号路由和 provider adapters | accounts |

对外按数据源插件组织，内部保留公共知识检索代码。加载器由注册表选择 factory，
不再用一个 knowledge 插件配置来聚合三种 provider。一个文件中不重复填写
provider：适配器由 plugin ID 确定，加载时补充现有内部类型。

来源 `name` 在插件内唯一，完整身份为 `<plugin-id>:<name>`。已有的
`owner_notes`、`patsnap_docs` 等名称继续保留；它们是来源实例，不是插件类型。
对外配置、Agent 引用和审计记录使用完整身份，避免不同插件同名实例混淆。
现有 MCP 工具接口可以保留，挂载名称必须能区分实例，工具名变化需明确迁移。

## 本地文档配置示例

以下是新结构的示例，路径及授权需要按部署填写，当前二进制尚不能加载。

```json
{
  "version": 1,
  "enabled": true,
  "access": {
    "channels": ["dingtalk", "feishu"],
    "audiences": ["owner"]
  },
  "settings": {
    "sources": [
      {
        "name": "project_docs",
        "root": "/absolute/project",
        "paths": ["docs", "knowledge"],
        "principal": "local-owner",
        "description": "Project documentation",
        "projects": ["default", "project-id"],
        "audiences": ["owner"]
      },
      {
        "name": "owner_notes",
        "root": "../knowledge/owner/legacy-snapshot",
        "paths": ["notes", "SOURCES.md"],
        "principal": "local-owner",
        "description": "Historical private notes",
        "projects": ["default"],
        "audiences": ["owner"]
      }
    ]
  }
}
```

相对 root 和 credential references 相对配置文件解析；`paths` 相对 source root。
root 与 paths 继续使用现有规范路径、显式选择、禁止目录逃逸和不受支持符号链接
的规则。只有配置允许的 Markdown 才可检索；不递归读取整个运行根目录。
历史快照中的原始消息 ID 属于来源追溯，不视作当前 Host SQLite 的外键。

Dokki/Confluence 的 sources 沿用现有 endpoint、credential_file、principal、
description、projects 和 audiences；Dokki 继续支持 source_url_base。
Email accounts 沿用现有账号字段和 send_enabled。状态目录由运行根与插件 ID
推导，不要求用户再配置 state_directory。

## 发现、授权和生命周期

只扫描 config 顶层的规定文件。公共 envelope 必须包含 version 1 和显式 enabled。
关闭项只检查外壳，不要求业务参数或凭证存在。缺省文件视为未启用，严格读取仍
拒绝重复 JSON 字段、未知字段与不支持版本。

统一数据源注册表提供类型解码、离线预检、能力声明、任务绑定和必要的状态管理。
启动检查所有启用项后才允许外部连接；资料搜索仍在任务执行中按需发生。
只读预检不创建数据库，不进行远程认证。启用插件不要求一定存在常驻进程。

任务装配取 plugin access、Agent plugin/source subset、项目、audience 与 native
tool policy 的交集。来源名单省略时继承符合条件的来源，显式空列表表示不挂载。
外层授权只能缩小来源授权；群 audience 不回退到 owner。Email 仍只支持 owner。
tool-free extraction 不挂载数据源工具。

Agent 插件引用由原 `knowledge` 改为对应数据源 plugin IDs；来源引用迁移为
完整身份。兼容期间旧知识源名称必须能唯一解析，否则要求明确指定，不随意选中。
来源授权、参数和适配器版本参与 binding fingerprint；变更后不继续使用不兼容
native session。资料正文变化沿用读取摘要与续读校验，不等同于权限变化。

状态位于 `ROOT/datasource/plugins/<plugin-id>`，只为需要状态的启用插件创建。
本地文档查询不生成默认副本或自动缓存目录。缓存如果以后加入，需要保留来源
和版本，不作为学习记忆。只读参考工具继续排除在自动执行经验来源之外。

共享 Email 账本沿用现有唯一操作键和提交前落盘机制。多渠道访问同一账本，
不按渠道复制；pending/unknown 操作不得因重启或改名而重试发送。

## 迁移

本次侧聊只产出设计。后续实施需先完成离线检查，停止写入、备份配置及必要状态，
再进行显式迁移，不能仅把现行文件重命名后尝试启动。

| 现行项 | 目标 |
| --- | --- |
| plugins.knowledge.json 中 local_markdown sources | datasource.plugins.local-documents.json |
| 同文件中的 dokki sources | datasource.plugins.dokki.json |
| 同文件中的 confluence sources | datasource.plugins.confluence.json |
| plugins.email.json | datasource.plugins.email.json |
| plugins/email 的运行状态 | datasource/plugins/email |
| 本地文档、私人笔记和已有快照 | 原位引用，不自动搬迁 |

拆分时保持原 enabled 与 access，再保留各 source 的项目、audience 和来源属性。
只含禁用配置的类型保持禁用，不因拆分自动启用。Agent source references 更新
为完整身份，运行证据保留原记录。Email 凭证引用和状态指向同步更新，操作 ID、
状态、收据与记录数必须保持，不能重建空账本代替旧账本。

旧格式与新格式同时存在时报冲突，不合并或覆盖。过渡兼容入口可以读取旧格式，
但默认新入口需要显式迁移。SQLite 记忆与历史 scope 不因数据源改名而转换。
迁移失败恢复停止状态下的备份，重新检查后启动；启动不会隐式修复或重放任务。

## 验证重点

- 已知/未知插件、独立启用、缺省和禁用项不初始化或连接。
- 一个本地插件多个来源，实例名称冲突、相对路径和 Markdown 选择范围。
- 渠道、Agent、项目与 owner/群权限交集，旧引用有歧义时明确失败。
- 同名实例跨插件可区分，检索与读取保留来源、日期、摘要和可用性。
- 现有本地笔记可检索且不导入 memories，SQLite 记忆召回行为保持。
- 邮件账本迁移与并发去重，未知发送结果不重试；停机清理和回滚。
- make check 和授权范围内的数据源实际调用，分别记录离线与实际验证。
