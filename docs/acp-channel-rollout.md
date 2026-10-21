# Shared ACP channel rollout

DingTalk and Feishu now select the same pinned Claude ACP backend in the local
runtime. Existing bot identities, channel state, memory scopes and datasource
configuration are retained. Both native profiles mount the same installed Skill
catalog already used by CLI/Web.

[Acceptance evidence and implementation details](acp-channel-rollout-detail.md)
record the real messages, failures, fixes and validation limits. The
[ACP guide](acp-execution.md) explains configuration and supported controls.

## What was verified

| Entry | Acceptance |
| --- | --- |
| DingTalk owner DM | Real user send and bot reply; existing JMS memory; native `jms-ssh` Skill; Confluence search/read. |
| Feishu owner DM | Real desktop user send and bot reply; automatic fact retention; `/new`; fresh-session recall; native Skill; Confluence search/read. |
| CLI and Web | Isolated real ACP check for fact retention, fresh Web recall, MCP search/read, Skill use and native continuation. |

Both real channel follow-ups retained their Host and native ACP sessions and
successfully searched/read the previous turn through current-receipt history
tools. The current request was excluded in both cases.

Native initialization observed 71 Skills in each channel. DingTalk connected
seven MCP services; Feishu connected six. The additional DingTalk service is
its configured DWS capability. A connected inventory proves loading; the table
lists the operations actually exercised.

The rollout found and fixes three issues: the managed scanner rejected a named
ACP profile, per-message MCP receipt arguments broke native continuation, and
answers could incorrectly deny Host-managed memory retention. Host keeps
automatic memory selection separate from the native response; the later
receipt determines whether a particular statement was saved.

Channel and user memory remain scoped. Switching transport starts a compatible
native conversation rather than migrating print history. No provider fallback
or automatic replay was added. The synthetic Feishu acceptance fact is disabled
after verification, with its evidence retained.

This acceptance covers owner-private text conversations. Group mentions,
meeting text/audio, interactive channel approval/Stop controls, every datasource
operation and general memory quality are not established by it.
