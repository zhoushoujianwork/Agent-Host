# DWS private-chat retrieval validation

The 2026-10-11 report (private validation evidence) records
manual, read-only acceptance through DWS CLI `1.0.61`, using one fixed personal
profile. It validates the source capability described in the
[integration notes](../../docs/knowledge-sources-detail.md#dws-private-chat-retrieval-acceptance-2026-10-11).
It is not an Agent-Host collector or an automated acceptance runner.

The installed CLI leaf schemas were checked before querying. A real colleague's
stable identity located a two-member private conversation through
`chat conversation-info`. `chat +chat-messages` read both participants' history.
`chat +search-msg` used the returned conversation ID, the owner's resolved sender
identity, an observed keyword and an explicit historical day. Its returned
message ID matched history; `chat +messages-mget` reread it. A unique nonmatching
keyword tested an empty result in the same conversation and day. Two history
pages tested genuine continuation and explicit truncation at configured limits.
All account operations used the same explicit `--profile` and `--format json`.

Raw history, paged history, message reread and empty-search results were retained
in owner-private temporary files outside the repository. Local assertions checked
stable IDs/content, both authors, scoped empty results, pagination and private
permissions. The positive keyword-search result and conversation metadata were
inspected interactively; their full responses were not archived. The repository
report contains only structural results, without account identities, conversation
or message IDs, message text, query keywords, credentials or private paths.

An empty recent history window did not establish absence across history.
Keyword search defaults to seven days unless an explicit range is supplied.
The historical sample and pagination-limited results do not prove all private
chats are discoverable or all messages are retrievable. Semantic search,
edits/retractions, event coverage, sustained collection and experience extraction
remain unverified. No messages were sent, no listener was started, and no
Agent-Host intake or memory persistence was enabled.
