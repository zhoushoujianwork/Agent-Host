# Datasource plugins

Enabled datasource plugins are available to every channel, Agent, project and
conversation, including groups. Users configure the sources or mailboxes and
set `enabled: true`; no effective-scope configuration is required. Restart
`make run` after changing configuration.

The supported plugins are `local-documents`, `dokki`, `confluence` and `email`.
Each has one `.agent-host/config/datasource.plugins.<name>.json` file containing
`version`, `enabled` and `settings`. A plugin can contain multiple sources or
mailboxes. Omitted or disabled plugins supply no tools or connections.

AI receives the source descriptions and search/read tools, then consults relevant
original material on demand. It does not load all documents at startup. Answers
retain source links, dates and uncertainty. Configured local file paths still
define which documents the local adapter can read.

Existing `access`, source `projects`/`audiences`, and Agent plugin/source lists
are obsolete for datasource plugins and no longer restrict availability. Current
examples omit them; older files remain readable during migration. Account
credentials, supported operations, sending switches and duplicate-send handling
continue to be owned by the adapters. Enabling a plugin does not authorize a
particular outgoing message.

This policy changes plugin availability. Host conversation history, learned
memory and native conversation state retain their existing identities. DingTalk
bot/DWS remain independent channel features.

Implementation and validation are in the [technical details](datasource-plugins-design-detail.md).
The [configuration guide](configuration-layout.md) explains startup and directories.
