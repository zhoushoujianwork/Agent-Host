# Datasource plugin implementation

The [main design](datasource-plugins-design.md) defines the current global
availability policy. This supersedes the scope filtering used in the historical
rollout recorded below.

## Configuration and identity

The registry accepts `local-documents`, `dokki`, `confluence` and `email`.
Unknown IDs fail explicitly. Document sources have names unique inside their
plugin; their full identity is `<plugin-id>:<name>`. Native mounts use
`knowledge_<plugin-id>_<source-name>`.

```json
{
  "version": 1,
  "enabled": true,
  "settings": {
    "sources": [{
      "name": "project_docs",
      "root": "/absolute/project",
      "paths": ["docs"],
      "principal": "local-owner",
      "description": "Project documentation"
    }]
  }
}
```

The filename selects the adapter, so sources do not repeat `provider`.
Dokki/Confluence use `endpoint`, `credential_file`, `principal`, `description`
and optionally Dokki's `source_url_base`. Email uses its existing account
settings and `send_enabled`. Relative roots and credentials resolve against
the configuration file; local `paths` resolve against the source root.
Private configuration-file permissions, path validation, bounded Markdown
reading and source/version receipts remain enforced.

## Assembly and compatibility

Startup discovers and checks all enabled datasource plugins before connecting
channels. Every task with tools enabled receives these plugins, regardless of
channel, Agent, project or audience. Email is also mounted for group tasks.
Agent `plugins` and `knowledge_sources` selectors do not filter unified plugins.
Tool-free extraction mounts no plugins.

Old `access` fields are accepted but ignored. Unified document loading discards
source `projects` and `audiences`; these fields are optional and absent from
current examples. Legacy standalone knowledge configurations retain their prior
selectors, and legacy standalone email retains its owner-only mounting behavior.
The service requires explicit migration of old `plugins.*.json` files.

Source configuration, credentials and the connector policy version participate
in binding fingerprints. Effective configuration changes replace incompatible
native sessions; changing only obsolete scope fields does not alter a binding.
Plugin failures remain explicit, including invalid credentials for an enabled
plugin; Agent selectors cannot conceal an enabled provider failure.

Host memory, history, USER profiles and native conversations remain partitioned
independently. DingTalk bot/DWS are channel features, outside this datasource
policy. Read-only knowledge lookups do not automatically publish learned memory.

## State and migration

State resides under `ROOT/datasource/plugins/<plugin-id>` when needed. Local
queries do not copy documents. Email keeps one shared operation ledger; an
unknown send outcome must be inspected and never automatically retried.

`tools/service/migrate_datasource.py --root /absolute/.agent-host` requires a
stopped runtime, locks channels and backs up configuration and Email state.
It splits legacy providers into their datasource files, removes obsolete scope
fields and Agent selectors, updates native tool names and preserves database
checksums and source references. On failure it restores the backup.

## Current validation

Regression coverage checks global mounts across DingTalk, Feishu, default and
unrelated projects, owner/group audiences and empty Agent selectors; namespaced
identities; older scope fields with unchanged bindings; configuration changes;
explicit provider failures; tool-free execution; global email mounting; and
migration rollback/ledger preservation. Run `make check` before committing.
Native and production verification are recorded separately from offline tests.

## 2026-10-15 global-plugin validation

`make check GOFLAGS=-p=1` passed. The first parallel run hit an unrelated Feishu
receiver readiness timeout; the focused test and complete serial check passed.
An isolated native task using the affected group audience and the Feishu channel
label called Confluence search for `登记` (limit 1), returned one result, and read
the original page successfully. It sent no channel messages. The first default
CLI permission check connected the tool but correctly required a native read-tool
allowlist; the successful run explicitly allowed only search and read.

Production configuration was backed up, obsolete datasource/Agent selectors
were removed, and the local supervisor restarted. Fresh DingTalk and Feishu
connection events and workbench HTTP 200 passed. This verifies restart readiness
and isolated native search/read; the original group message was not replayed.
Private evidence is retained under
`.agent-host/acceptance/global-plugins-20261015T024128Z/`.

## 验证记录

2026-10-13 完成现有四个来源实例的配置迁移：`dokki:dokki`、
`confluence:confluence`、`local-documents:patsnap_docs` 和
`local-documents:owner_notes`。原始 Markdown 正文未移动或写入 memories。
Email 共用迁移后的单一账本，渠道默认 memory_scope 未变化。

迁移报告、备份和原生验证事件保存在 ignored
`.agent-host/backups/datasource-config-20261013T105814Z/`。两边渠道数据库及
Email ledger 的 SHA-256 在停止状态下前后一致。重新启动后钉钉 Stream、
飞书文本和会议入口均产生新的连接事件；工作台返回 HTTP 200。
隔离原生运行成功完成 `mcp__email__list_accounts`、`mcp__email__check_account`
及 `mcp__knowledge_local-documents_owner_notes__search_local_markdown`。
IMAP/SMTP 认证通过；没有读取邮件正文、发信或发送平台测试消息。
Dokki/Confluence 新配置通过离线解析与凭证预检，未新增远程检索验收。

自动化覆盖来源重名、短名称歧义、namespace MCP 绑定、owner/群权限、
Agent 排除不可用 credential、权限 fingerprint 变化、旧格式拒绝，以及迁移
锁定、状态保留和失败回滚。`make check GOFLAGS=-p=1` 通过。
