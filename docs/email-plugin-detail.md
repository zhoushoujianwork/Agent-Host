# Email capability implementation notes

Current datasource policy: enabled `datasource.plugins.*.json` plugins are global
across channels, Agents, projects and owner/group conversations. Old plugin
`access`, source project/audience fields and Agent datasource selectors are
ignored. Standalone legacy configurations and historical scope-validation
records below retain their original meaning. See the
[datasource design](datasource-plugins-design.md) for current behavior.

Status: implementation and validation notes. Product scope
and implementation order are defined in the [main guide](email-plugin.md).

## Fit with existing code

`internal/harness/harness.go` defines `MCPServer` and `Request.MCPServers`.
`internal/cli/knowledge.go` assembles pinned stdio servers;
`internal/harness/claude/adapter.go` translates them into native configuration.
Reuse this registration path without putting Gmail or IMAP behavior inside the
harness adapter, or turning email operations into knowledge-source writes.

`internal/email` implements the capability with an account registry, normalized tools and provider
adapters, plus CLI check/serve wiring. OAuth browser onboarding remains external.
Compose email registrations with existing knowledge/history/context servers:
the current knowledge resolver resets its MCP list, so simply appending before
that resolver would lose the email servers.

The runtime registers one stdio server named `email` for the enabled Agent/audience. Its
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

Account records contain a stable ID, address, provider, credential-file reference,
`send_enabled`, and optional `disabled`. All enabled accounts are listed in owner
runs when the runtime has an email configuration; no per-Agent account subsets
are implemented. Personal mail is independent of project routing. Group tasks
and tool-free runs do not mount this capability.
Bind the catalog policy and configuration version into runtime
compatibility; ordinary OAuth access-token refresh should not rotate sessions.
Revalidate registry configuration and account enablement at every call. Tool-free extraction gets no email MCP.

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

Implemented tools (optional arguments omitted here for readability):

| Tool | Purpose |
| --- | --- |
| `list_accounts()` | Return account IDs, addresses, providers, enabled operations and unchecked connection status; no secrets or mailbox reads. |
| `check_account(account_id)` | Check live provider authentication; 163 checks IMAP and enabled SMTP without sending. |
| `list_folders(account_id)` | List that account's folders or labels. |
| `search_messages(account_id, filters, cursor)` | Search one account with explicit pagination and coverage. |
| `read_message(account_id, message_id)` | Read the selected message without marking it read. |
| `read_attachment(account_id, message_id, attachment_id)` | Retrieve a bounded, source-linked attachment. |
| `send_message(account_id, operation_id, to, subject, body, attachments)` | Send from the selected account and retain the operation receipt. |
| `reply_message(account_id, operation_id, message_id, to, body, attachments)` | Reply using the original message's reply headers and explicit recipients. |

Tool discovery performs no mailbox reads. `list_accounts` reports unchecked status;
`check_account` provides a timestamped live result, not a future guarantee. For a combined
search, AI calls each relevant account and reports per-account results/failures.
Send/reply arguments require an operation ID for duplicate handling;
`operation_status(account_id, operation_id)` retrieves the retained receipt.
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
Attachment chunks return provenance and a SHA-256 digest. No automatic download
path or arbitrary filesystem write is exposed.

Do not conflate execution success, mailbox mutation, recipient delivery or memory
publication. Native tool receipts remain in the owner's run partition. Email MCP observations
are excluded from reusable execution-experience extraction, including sends.
Sending receipts are also retained independently in the private email SQLite store.

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


## Configuration and commands

Create an owner-private JSON file (0600), for example:

```json
{
  "accounts": [
    {
      "id": "personal-163",
      "address": "owner@163.com",
      "provider": "163",
      "credential_file": "/absolute/private/163.secret",
      "send_enabled": true
    }
  ],
  "state_directory": "/absolute/private/email-state"
}
```

A 163 credential file contains only its client authorization password. This
adapter targets personal `@163.com` accounts at `imap.163.com:993` and
`smtp.163.com:465` with TLS certificate verification; enterprise endpoints are not
supported. Gmail uses `provider: "gmail"` and a private JSON credential file
containing `client_id`, `client_secret` and `refresh_token`, previously authorized
for `gmail.readonly` and, for sending, `gmail.send`. It verifies the Google
profile address before accessing messages or sending; it refreshes access tokens
on demand without rotating the native session. No credentials enter MCP arguments.

```sh
agent-host email check --config /absolute/private/data-email-accounts.json
agent-host email check --config /absolute/private/data-email-accounts.json --account personal-163 --live
agent-host chat --email-config /absolute/private/data-email-accounts.json -m 'List my managed mailboxes.'
```

The local check validates the account registry only; missing credentials or
provider access failures are reported by live checks/calls for the selected
account. In the project-local runtime, add `email_config` with the absolute registry
path to the private `config/component-service.json`, then restart `make run`.

One account operation is bounded to 30 seconds. Read MIME is capped at 8 MiB,
with at most 200 parts and body/attachment chunks of up to 12,000 bytes.
Optional `length` requests a smaller chunk if JSON encoding exceeds the tool
output limit. Body offsets are
UTF-8 boundaries; attachment chunks are base64. Outgoing attachments total at
most 512 KiB, with at most ten attachments and 50 explicit recipients. Body text
is plain text. HTML-only incoming content is returned as source without fetching
remote resources or executing it. Existing MIME/charset decoding errors fail
explicitly. Large attachments require a later streaming/download increment.

Search returns at most 20 messages per call (default ten), latest UID first for
IMAP. The 163 server tested omits UIDNEXT even in STATUS and uses sparse UIDs;
search does not depend on either contiguous UIDs or UIDNEXT. Continuation pins
account/folder/UIDVALIDITY and the upper UID bound. Use identical filters across
pages. A connection-wide 16 MiB response budget bounds IMAP parsing. This is a
live mailbox view, not a snapshot; concurrent arrivals/deletions can alter coverage.
Gmail uses its own page token; folder is a label ID and defaults to INBOX.

The operations store commits `(account_id, operation_id, content digest)` before
submission. Parallel calls with the same operation ID submit at most once.
Changed content under the same ID fails. A crash leaves `pending`; submission
errors conservatively become `unknown`, even if the error occurred before DATA.
Both require inspection, not an automatic new-ID retry. There is no automatic
reconciliation or retry command yet. The ledger stores message identity and
provider receipt, not body/attachment content. SMTP acceptance does not ensure
a Sent-folder copy; this increment does not append one automatically.

## Validation record

- Offline: account discovery/routing, invalid account and cross-account references,
  configuration revocation, owner/group/tool-free assembly, preservation of
  knowledge MCP mounts, session compatibility, strict arguments and private files.
- Protocol fixtures: 163-style missing UIDNEXT, read-only EXAMINE and BODY.PEEK,
  UIDVALIDITY rejection, MIME reply/attachment round trip, UTF-8 continuation,
  actual TLS SMTP exchange, Gmail refresh/identity checking and send encoding.
- Send ledger: successful duplicate suppression, concurrent duplicate calls,
  content mismatch, disabled sending and unknown-outcome replay prevention.
- Live 2026-10-13: one supplied 163 account passed IMAP and SMTP authentication; folders, search and
  one body read passed with flags unchanged. No outgoing message was sent.
- Native AI 2026-10-13: account discovery, specified-account search and message
  read passed through the actual Claude adapter and email MCP. The private report
  records successful tool names and no message content; sends remained zero.
- Production installation on 2026-10-13 restored the managed channel connection.
  This verifies service startup, not a channel-message round trip.
- Fresh project-local acceptance on 2026-10-13 passed live account authentication,
  folder listing, message search and body reading with flags unchanged. A full
  Host CLI request listed managed accounts, returned exactly three recent emails,
  read the newest message's 4,240-byte body and produced a summary with a suggested
  next action. All three native MCP calls had successful paired results; no
  send/reply call occurred. The private report is under the ignored runtime's
  `evidence/email-readme-*/report.json`; mailbox content is not committed.
- Gmail real-account onboarding, live attachment reads, real send/reply and a
  second account remain unverified. Offline tests do not establish these outcomes.

Opt-in read probe: set `AGENT_HOST_EMAIL_LIVE_CONFIG` and
`AGENT_HOST_EMAIL_LIVE_ACCOUNT`, then run
`go test ./internal/email -run TestMailboxReadLive -v -count=1`.
The native probe is opt-in through `AGENT_HOST_EMAIL_NATIVE_LIVE=1`, with explicit
Host binary, private service root and mailbox configuration; see the test source.
It records successful tool names only and never sends mail.

The active local registry lives in `.agent-host/email/data-email-accounts.json`; both optional
runtime configurations reference it. Feishu startup previously omitted the
registry and had tools disabled. The corrected project-local configuration uses
`no_tools: false`; Gmail remains unconfigured pending separate authorization.
