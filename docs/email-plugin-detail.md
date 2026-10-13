# Email capability implementation notes

Status: proposed implementation boundaries, not delivered behavior. Product scope
and implementation order are defined in the [main proposal](email-plugin.md).

## Fit with existing code

`internal/harness/harness.go` defines `MCPServer` and `Request.MCPServers`.
`internal/cli/knowledge.go` assembles pinned stdio servers;
`internal/harness/claude/adapter.go` translates them into native configuration.
Reuse this registration path without putting Gmail or IMAP behavior inside the
harness adapter, or turning email operations into knowledge-source writes.

Propose `internal/email` for the account registry, normalized tools and provider
adapters, plus CLI setup/check/serve wiring. Command names and exact configuration
schema should be finalized during implementation; none are runnable today.
Compose email registrations with existing knowledge/history/context servers:
the current knowledge resolver resets its MCP list, so simply appending before
that resolver would lose the email servers.

Register one stdio server named `email` for the enabled Agent/audience. Its
`list_accounts` tool returns the managed account catalog visible to that context.
AI chooses an account using the user's request and this catalog, then supplies
`account_id` to every mailbox operation. Host performs no task-based account
selection or provider-specific tool mounting. Two 163 accounts use different IDs
in the same catalog; they do not need separate MCP instances.

Resolve `account_id` against the registry on every call. Reject unknown, disabled
or unavailable accounts explicitly; never fall back to another sender. Tool
arguments cannot override credentials or provider endpoints. A mail reference
must belong to the supplied account. An explicit user account choice takes
precedence; ask only when the sending account remains ambiguous.

Account records contain a stable ID, display name, provider, expected mailbox
identity, credential reference, selected Agent instances and audience policy.
Owner-private mail is independent of project routing unless explicitly limited.
Bind the catalog policy and configuration version into runtime
compatibility; ordinary OAuth access-token refresh should not rotate sessions.
Revalidate revocation and binding at calls. Tool-free extraction gets no email MCP.

MCP selection limits exposed capabilities; the current shared service OS user and
native bypass policy are not credential isolation. Do not claim that an omitted
MCP mount prevents arbitrary filesystem access by that same process identity.

## Provider boundaries

- Gmail: use OAuth and Gmail API message/attachment and send endpoints. Initial
  scopes are `gmail.readonly` and `gmail.send`; metadata-only scope cannot read
  bodies. Search and read are separate calls. A future provider-draft feature
  needs broader permission;
  `gmail.compose` also authorizes sending, so draft-only exposure must be enforced
  in the tool implementation, not inferred from OAuth.
- 163: use TLS IMAP with a client authorization password. Validate the actual
  account's advertised capabilities and settings during onboarding. Personal
  and enterprise services must not share assumed endpoints. Use read-only mailbox
  selection and body peek operations to avoid setting the Seen flag. Use TLS SMTP
  for sending and preserve reply headers for replies.
- Normalize common sender, recipient, date, subject and unread filters. Do not
  pretend Gmail query syntax or thread semantics are portable to IMAP. Unsupported
  filters fail explicitly or are reported as bounded local filtering with coverage.

References checked on 2026-10-13:
[Google scope documentation](https://developers.google.com/workspace/gmail/api/auth/scopes),
[Gmail API reference](https://developers.google.com/workspace/gmail/api/reference/rest),
and [Huawei's mail-client authorization instructions for 163](https://consumer.huawei.com/cn/support/content/zh-cn15872099/).
Exact 163 account endpoints and capabilities remain live-validation work.

## Tool surface and evidence

Proposed tools (names and schemas are not implemented yet):

| Tool | Purpose |
| --- | --- |
| `list_accounts()` | Return account IDs, addresses, providers, enabled operations and last checked connection status/time; no secrets or mailbox reads. |
| `list_folders(account_id)` | List that account's folders or labels. |
| `search_messages(account_id, filters, cursor)` | Search one account with explicit pagination and coverage. |
| `read_message(account_id, message_id)` | Read the selected message without marking it read. |
| `read_attachment(account_id, message_id, attachment_id)` | Retrieve a bounded, source-linked attachment. |
| `send_message(account_id, to, subject, body, attachments)` | Send from the selected account and retain the operation receipt. |
| `reply_message(account_id, message_id, to, body, attachments)` | Reply using the original message's reply headers and explicit recipients. |

Tool discovery performs no mailbox reads. Cached connection status must be marked
as cached or unchecked; it is not proof a later call will succeed. For a combined
search, AI calls each relevant account and reports per-account results/failures.
Send/reply arguments will also carry an operation ID for duplicate handling.
Reply-all must use the intended recipient set, not silently expand a reply.

Return account ID, provider, stable message reference, original headers, observed
time, pagination and coverage with each result. Gmail IDs remain account-bound.
IMAP references include mailbox, UIDVALIDITY and UID; reject stale references
rather than resolving them to a different message. Preserve source dates and
original timezone information when formatting results.

Bound message counts, body size, attachment size, network duration and tool output.
Decode MIME and supported charsets; expose truncation and parsing failures.
Do not fetch remote HTML resources. Treat bodies and attachments as untrusted
source material. Attachment filenames must not select arbitrary filesystem paths.
Retain attachment provenance and a content digest in the run's private artifacts.

Do not conflate execution success, mailbox mutation, recipient delivery or memory
publication. Retain necessary source receipts in the owner's partition; inspect
existing tool-observation and extraction paths so mail does not become shared
experience merely because it appeared in a successful tool result.

Use private credential storage and redact secrets from arguments, prompts and
diagnostics. Distinguish authentication failure, throttling, unsupported operation,
stale reference, empty result and incomplete search. One failed account must not
be presented as a successful search over all accounts.

## Sending and validation

Sending binds an authorized request to the exact account, recipients,
subject, body and attachments. Record intent and provider receipt durably. A
timeout after submission is an unknown outcome, not proof of failure; do not
automatically replay a send. SMTP acceptance is not proof of recipient delivery.

Implementation checks should cover account discovery, explicit account routing,
unknown IDs, cross-account message references, registration and revocation,
owner/group assembly, preserved existing MCP servers, Gmail token refresh,
IMAP UID invalidation, unread-state preservation, MIME and attachment bounds,
partial multi-account failure, reply headers, send receipts, duplicate-operation
handling, unknown send outcomes and secret redaction. Run `make check` before the
implementation commit. Real acceptance uses bounded search/read on every selected
account and a native AI request. Live sending/reply acceptance needs an explicitly
authorized test recipient and content; read access alone does not authorize it.
