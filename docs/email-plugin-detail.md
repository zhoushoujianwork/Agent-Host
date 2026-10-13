# Email capability implementation notes

Status: proposed implementation boundaries, not delivered behavior. Product scope
and implementation order are defined in the [main proposal](email-plugin.md).

## Fit with existing code

`internal/harness/harness.go` defines `MCPServer` and `Request.MCPServers`.
`internal/cli/knowledge.go` assembles pinned stdio servers;
`internal/harness/claude/adapter.go` translates them into native configuration.
Reuse this registration path without putting Gmail or IMAP behavior inside the
harness adapter, or turning email operations into knowledge-source writes.

Propose `internal/email` for account selection, normalized tools and provider
adapters, plus CLI setup/check/serve wiring. Command names and exact configuration
schema should be finalized during implementation; none are runnable today.
Compose email registrations with existing knowledge/history/context servers:
the current knowledge resolver resets its MCP list, so simply appending before
that resolver would lose the email servers.

Start with one pinned stdio instance per selected account, named from a stable
account ID. Each exposes the same normalized tools, with no arbitrary account or
credential override in tool arguments. Multiple accounts of the same provider
remain independent. Host supplies an account/tool catalog; the native AI can
query multiple instances and combine results while retaining individual coverage.

Account records contain a stable ID, display name, provider, expected mailbox
identity, credential reference, selected Agent instances and audience policy.
Owner-private mail is independent of project routing unless explicitly limited.
Pin account identity, policy and credential reference/version into runtime
compatibility; ordinary OAuth access-token refresh should not rotate sessions.
Revalidate revocation and binding at calls. Tool-free extraction gets no email MCP.

MCP selection limits exposed capabilities; the current shared service OS user and
native bypass policy are not credential isolation. Do not claim that an omitted
MCP mount prevents arbitrary filesystem access by that same process identity.

## Provider boundaries

- Gmail: use OAuth and Gmail API message/attachment endpoints. Initial scope is
  `gmail.readonly`; metadata-only scope cannot read bodies. Search and read are
  separate calls. A future provider-draft feature needs broader permission;
  `gmail.compose` also authorizes sending, so draft-only exposure must be enforced
  in the tool implementation, not inferred from OAuth.
- 163: use TLS IMAP with a client authorization password. Validate the actual
  account's advertised capabilities and settings during onboarding. Personal
  and enterprise services must not share assumed endpoints. Use read-only mailbox
  selection and body peek operations to avoid setting the Seen flag. SMTP is
  unnecessary for the first increment.
- Normalize common sender, recipient, date, subject and unread filters. Do not
  pretend Gmail query syntax or thread semantics are portable to IMAP. Unsupported
  filters fail explicitly or are reported as bounded local filtering with coverage.

References checked on 2026-10-13:
[Google scope documentation](https://developers.google.com/workspace/gmail/api/auth/scopes),
[Gmail API reference](https://developers.google.com/workspace/gmail/api/reference/rest),
and [Huawei's mail-client authorization instructions for 163](https://consumer.huawei.com/cn/support/content/zh-cn15872099/).
Exact 163 account endpoints and capabilities remain live-validation work.

## Tool surface and evidence

Initial proposed operations: account information, folder/label listing, message
search, message read and attachment read/download. Account information reports
supported operations, not secret configuration. Tool listing performs no mailbox
reads. Reply preparation uses native text generation and requires no write tool.

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

## Later writes and validation

Future sending binds an authorized request to the exact account, recipients,
subject, body and attachments. Record intent and provider receipt durably. A
timeout after submission is an unknown outcome, not proof of failure; do not
automatically replay a send. SMTP acceptance is not proof of recipient delivery.

Implementation checks should cover per-account registration and revocation,
owner/group assembly, preserved existing MCP servers, Gmail token refresh,
IMAP UID invalidation, unread-state preservation, MIME and attachment bounds,
partial multi-account failure and secret redaction. Run `make check` before the
implementation commit. Real acceptance uses bounded search/read on every selected
account and a native AI request; no outgoing test mail is implied by read access.
