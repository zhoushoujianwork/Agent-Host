# Datasource plugins

Enabled datasource plugins are available to every channel, Agent, project and
conversation, including groups. Users configure the sources or mailboxes and
set `enabled: true`; no effective-scope configuration is required. Restart
`make run` after changing configuration.

During Beta, all users and Agents receive every enabled datasource, including
group conversations. Per-user `knowledge_sources` and `group_knowledge_sources`
allowlists are retained in configuration but do not restrict availability.
Configured mailboxes and DingTalk workspace tools are also shared. Disabled
sources stay disabled; upstream account permissions and operation settings
still apply. Host profiles, private conversation history and memory remain
separate between users.

The supported plugins are `local-documents`, `dokki`, `confluence` and `email`.
Each has one `.agent-host/config/datasource.plugins.<name>.json` file containing
`version`, `enabled` and `settings`. A plugin can contain multiple sources or
mailboxes. Omitted or disabled plugins supply no tools or connections.

AI receives the source descriptions and search/read tools, then consults relevant
original material on demand. It does not load all documents at startup. Answers
retain source links, dates and uncertainty. Configured local file paths still
define which documents the local adapter can read.

Confluence supports creating and updating pages and blog posts when authorized
by the user. Automatically managed notes and reports default to the authenticated
connector account's personal blog; regular pages remain available when requested. Blog creation
can resolve the personal space automatically, without per-user configuration.
Use descriptive blog titles without appending a date; Confluence already dates posts.
Updates require the content's current version and replace its title and complete
body. Read back the content to verify the result; a failed or uncertain write must
be inspected before retrying. Existing Confluence configuration is sufficient,
subject to the account's upstream permissions.
This default chooses where authorized records are written. It does not publish
every chat message or replace the Host's local learned memory, preferences,
Skills or operational evidence.

Existing `access`, source `projects`/`audiences`, and Agent plugin/source lists
are obsolete for datasource plugins and no longer restrict availability. Current
examples omit them; older files remain readable during migration. Account
credentials, supported operations, sending switches and duplicate-send handling
continue to be owned by the adapters. Enabling a plugin does not authorize a
particular outgoing message.

This policy changes plugin availability. Host conversation history, learned
memory and native conversation state retain their existing identities. DingTalk
bot/DWS remain independent channel features.

## Proposed usage guidance for enabled plugins

The [managed Skills design](managed-skills-design.md) adds a usage Skill alongside
each enabled plugin's tools. The default Agent receives its capability descriptor
and proactively loads complete guidance before related calls, including identity
resolution, operation semantics and result verification. Tools and guidance must
describe the same enabled configuration; missing guidance or dependencies are
explicit readiness failures. Disabled plugins provide neither active usage Skills
nor tools. This is proposed work, not a claim that current source descriptions
already implement the managed catalog. Existing Beta sharing, upstream permissions
and per-operation authorization remain unchanged.

Implementation and validation are in the [technical details](datasource-plugins-design-detail.md).
The [configuration guide](configuration-layout.md) explains startup and directories.
