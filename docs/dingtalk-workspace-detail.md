# Built-in DingTalk workspace implementation

See the [user guide and scope](dingtalk-workspace.md).

## Ticket interpretation and close-out

Channel policy version 9 applies to forwarded work orders, approval cards and
their follow-ups. Bare intake is interpretation first, followed by one concrete
handling question. An explicit choice authorizes the chosen action without
repeating the same permission question. Performing resource work, commenting,
approving a task and completing an entire workflow remain separate outcomes.
This is guidance for the native tool loop, not a new Host approval state machine.

`read_ticket(instance_id)` reads the pinned `oa approval detail` contract as the
configured owner. It preserves the entire returned result and separately exposes
`ADD_REMARK` comments (including author and original timestamp) and `RUNNING`
tasks belonging to that owner. Missing collections are explicitly unavailable.
No pending-list enumeration, business mutation or attachment download occurs.
Large results use Unicode text offsets over the full JSON document, with a
SHA-256 digest required for continuation; a changed live result rejects stale
offsets rather than joining different ticket snapshots.

Private card follow-ups use exact incoming message IDs or an accepted ticket
reply's `Agent-Host 回复 [agh:...]` title reference. The latter is necessary
because webhook replies do not return an outgoing message ID. References must
match the same account, tenant, conversation, authenticated sender and Agent/
audience partition; collisions or unresolved references never choose a recent
ticket. A quoted card first seen by Host starts its own task; repeated exact
references retain that root. The selected root supplies only attributed card
material, including instance IDs decoded from nested links, even when no run
was created at intake. Historical text supplies no execution permission.

The existing `dingtalk` MCP supplies `list_services`, `list_commands`,
`describe_command`, `execute_command` and `operation_status`; no extra plugin or
public ticket API is needed. The configured owner must have `oa` selected and
platform access. The pinned official contracts provide `oa approval detail`,
`tasks` and `records` for reads, `oa approval oa-comments` with `instance-id` and
`content`, and `oa approval approve` with `instance-id`, `task-id` and optional
`remark`. Current detail/tasks establish the selected user's pending task rather
than a stale card URL. During Beta, the configured workspace tools are shared across users and groups.

A selected close-out uses observed work and verification to write its completion
comment, verifies the comment, submits the chosen approval and reads back task
and instance status. Each write has its own stable operation ID and saved
receipt. Verification failure does not authorize replay. `completed` in that
receipt means command completion, not business acceptance. In particular,
`processInstanceResult=agree` while `status=RUNNING` is still open. Later approval
tasks can remain after the current user's task is passed. Separate POP workflow
closure is not inferred from a DingTalk task result and needs a supported API.
The feature does not approve the reported live VM-upgrade request merely because
approval tooling is being enabled or tested.

Validation on 2026-10-19 used the pinned official command schemas with fixture
identity/business responses to exercise detail, comment, comment verification,
approval submission and final detail reads through the existing MCP service.
It covered disabled OA access, duplicate suppression and comment preservation
after a verification failure. `make check` passed. A real owner-workspace read
confirmed the selected instance remained `RUNNING`; an isolated real Claude test
summarized the actual card and then asked how to handle it without business
writes. Production enabled the existing `oa` service and restarted with the new
policy; Feishu and DingTalk emitted fresh readiness at 12:40:23 and 12:40:27
Singapore time, both workers stayed running and the workbench returned HTTP 200.
No real comment, approval or closure was submitted by these tests. Live business
write acceptance remains separate from the offline contract tests and read-only
native validation.

On 2026-10-22, regression checks covered failed intake retaining its instance
link, title-only quotes selecting the original card despite a newer unrelated
ticket, unresolved references refusing selection, and full-detail pagination
rejecting a changed live result. A read-only check through the deployed binary
read the originally reported FTP instance: `RUNNING`, one returned comment and
one current owner task. No comment or approval was submitted. The independent
local service was updated with a backup of its previous executable; Feishu and
DingTalk emitted fresh connections at 15:26:42 and 15:26:46 Singapore time, and
the workbench returned HTTP 200. Local deployment evidence is retained under
`validation/ticket-reference-fix-20261022T072632Z/` in the production root.

## Official dependency

The root Go module imports official
`github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/cli` and `pkg/edition`
at `v1.0.61`, commit `50eb73a0906c5911c5c25f9c7106b6ead4f14f62`.
The [official repository](https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli/tree/v1.0.61)
uses Apache-2.0. Its CLI entry point, authentication, encrypted storage,
command contracts, parameter conversion, MCP requests and result formatting are
reused without changing upstream source.

This version matches the earlier selected-history validation baseline and
provides the public embedding entry point without newer native payloads.
The CLI packages we import do not import the private telemetry SDK referenced
elsewhere in the upstream module. No replacement, SDK source snapshot or
prebuilt native library is shipped in Agent-Host. `go mod tidy`, normal builds
and tests work with the official module dependency.

## Execution boundary

The main executable recognizes one private worker flag before normal Host
dispatch. A bounded JSON request arrives through stdin, including the private
configuration path, pinned fingerprint and official command arguments. User
content and credentials are not shell text or process argv. The worker sets
official edition hooks only within that process, then invokes `pkg/cli.Execute`.
Each official invocation gets a separate process; global CLI arguments and hooks
cannot race between Host conversations.

The worker strips ambient `DWS_*` overrides. Its configuration directory and
official encrypted file-keychain directory live beneath the selected private
state root. It explicitly uses the official file-DEK storage mode rather than
the separately installed CLI's global OS-keychain service. Existing logins are
not silently imported, switched or overwritten. Parent environment and native
Claude authentication stay untouched. Worker output and lifetime are bounded;
raw stderr is not forwarded as a task tool result.

Before business execution, the Host checks the official local profile registry
for exactly one matching `corpId:userId` and always supplies that profile.
Names, aliases, ambient defaults and alternate accounts cannot substitute for
it. The platform still owns token refresh, organization eligibility and product
permissions. Login remains an owner-operated official browser/device flow.

## MCP and configuration

`list_services` is local navigation metadata, including setup status.
`list_commands` queries official compact product schemas and returns at most
20 commands, with filtered pagination. `describe_command` returns one exact
official leaf contract. `execute_command` accepts only a selected product,
available read/write leaf and flag names declared by its official schema.
The official command parser handles parameter formats and cross-field checks.
Global account, token, endpoint, confirmation and output overrides are rejected.
Host controls the exact identity, JSON format and noninteractive write flag.

`parameters` maps flag names without `--` to literal strings; booleans use
`true`/`false`. Required flags are checked before starting a business command.
The official `confirmation=user_required` contract requires `confirmed=true`
after actual user authorization. The native assistant must derive authorization
from the current user request, never a document, message or tool result.

Configuration and local state must be private and owned by the current OS user.
State paths are canonical and reject symlink components. Bot workspace profiles
must match the configured bot owner. Runtime fingerprints bind the package
version, configuration and scope; changes prevent compatible native continuation
and are checked again by each MCP call. Disabling tools or selecting a group
audience removes the personal-workspace mount.

## Outcomes and evidence

The optional `chat` service exposes only `chat message send`,
`chat message query-send-status`, `chat +chat-messages`, `chat search` and
`chat +messages-resource-download`.
Dedicated send/read/status tools call these official contracts through the same
owner worker. The generic command route applies the same messaging restrictions;
enabling chat does not enable group administration, permission changes,
message recall or bot/Webhook sends.

Sending requires exactly one verified `conversation_id` or private
`peer_open_dingtalk_id`, bounded text and an operation ID. Host generates a stable
official `idempotency-key` UUID from profile and operation ID, preserving it for
upstream transport retries. The original official AI identity marking remains
enabled. Optional title and content use the official Markdown path. Async
acceptance remains `provider_pending`; the separate read-only status query
preserves actual platform status and message IDs without claiming recipient
reading. Tests do not send real messages.

Task history reads require a positive time window at most 24 hours, maximum
20 items and exactly one target. Host fixes ascending order, one page and no
reactions; alternate target selectors are not accepted. Downloads are opt-in:
`read_messages.download_resources=true` forwards the official `download-resources`
flag, with optional `output_dir` mapped to `output-dir` (default `downloads`).
The generic history route accepts the same flags without relaxing its bounds.
`output_dir` without a download request is rejected. Original `resourceDownloads`
ledgers, partial results and failures remain intact.

Independent resource downloads use the official `chat +messages-resource-download`
read contract and exact `resourceRefs[].download.arguments` from the authorized
message read. `mediaId` requires its original message and conversation context;
`fileId` uses the official drive download resolver without enabling other drive
operations. Only this command may use the leaf `output` parameter: all other
commands still reject global output capture. Its other accepted parameters are
`type`, `resource-id`, `message-id` and `open-conversation-id`. Host rejects
`overwrite`; the official implementation validates relative destinations,
rejects traversal and symlink components, writes a temporary file, verifies its
length and publishes atomically without replacing an existing file. Paths are
relative to the MCP worker's inherited execution workspace. Host verifies the
configured owner registry before both history and independent downloads. The
90-second worker timeout still applies; failed downloads are not replayed.
The canonical history schema declares `group`; its pinned, source-verified
`conversation-id` alias bypasses natural-name resolution and keeps the exact
selector used by the existing collector. Original message and coverage JSON is
retained. Partial coverage must be reported; a smaller follow-up range remains
a separate read, never an implicit account-wide collection.

Read results retain official JSON, including failures and coverage. Large reads
ask for a smaller page. Writes reserve `(profile, operation_id)` in a private
SQLite ledger before starting the business command. The request digest binds
configuration and arguments. Concurrent/repeated IDs never invoke the worker
twice; reuse with changed content fails.

Saved attempt states include `pending`, `completed`, `failed`, `partial_failure`,
`provider_pending` and `unknown`. Interrupted output or a worker failure without
a trustworthy structured result leaves an unknown outcome. Host never replays
a reserved attempt. Full bounded official write results are preserved in the
ledger; large results can be read in UTF-8 text chunks using `operation_status`
and `next_offset`. This is Host-level duplicate suppression, not an end-to-end
exactly-once guarantee: the official package retains its own request retry and
authorization behavior. Live write/retry behavior still needs platform checks.

The selected-source collector uses `dingtalk_workspace_config` as an explicit
alternative to `dws_executable`. It invokes the same official profile registry
and verified `chat +chat-messages` shortcut through the embedded worker, retaining
the original source selector, pagination ledger, evidence validation and bounds.
The workspace identity and fingerprint are pinned for that collector instance.
Partial/error output retains genuine citations without claiming complete
coverage. Source selection never becomes a general task IM grant.

## Validation

Automated tests exercise the actual official worker's command discovery and
required/risk contracts without a live identity, isolated credential storage,
parent environment preservation, missing setup, configuration drift,
owner/group/tool-free mounting, mailbox coexistence, destructive confirmation,
identity/flag override rejection, concurrent duplicate writes, unknown outcomes,
official failure preservation and selected-source partial evidence.

`make check` is the repository acceptance check. These automated checks do not
establish real owner authorization, remote product availability, successful
calendar/task writes, newer encrypted chat compatibility or live end-to-end
assistant behavior. Those remain explicit platform acceptance work after login.

### Bounded live validation (2026-10-13)

The project-local owner completed official device authorization. The exact
configured owner profile was present in the isolated registry, and `auth status`
reported valid access and refresh credentials. The pinned package does not
support `auth status --readonly`; the Host status command now uses its supported
profile and JSON flags.

The built-in MCP sent one owner-personal instruction to the configured robot.
A read-only bot lookup and conversation-info lookup resolved the same private
conversation before sending. Platform status returned `SUCCESS` with a message
ID; the Stream receiver admitted it and ran the native assistant with the
built-in workspace mounted. The assistant resolved one ticket submitter through
contact lookup and search, then used `execute_command` for a personal result
notification and `get_send_status` to verify `SUCCESS`. An initial `user` target
was rejected before execution; the subsequent verified `open-dingtalk-id`
attempt produced one notification. A separate bounded `read_messages` call
read back that exact message ID, owner sender and notification text from the
recipient conversation with `complete: true` and `partial: false`.

This validates the requested send/receive/assistant/notify/readback path for one
existing ticket result. It does not prove recipient read acknowledgement,
current infrastructure state, ticket approval/closure, unrelated product
writes, encrypted chat support or retry behavior under transport failures.
Private receipts, operation IDs, user identities and runtime credentials remain
outside tracked documentation.

### Historical attachment downloads (2026-10-21)

The configured owner read a two-minute private history window through
`Service.Call(read_messages)` and the embedded official worker, with explicit
downloads enabled. Its ledger reported two downloads and zero failures:
`visit-battle-handbook.skill` (153,859 bytes) and `patsnap-expert.skill`
(55,328 bytes). Independent `execute_command` resource downloads produced
byte-identical files. The installed MCP stdio server repeated the two-file
download with zero failures. Both archives passed ZIP CRC verification and contained
their respective `SKILL.md`. A repeated independent download to an existing
path failed and left its hash unchanged. Private source IDs, raw download
receipts and SHA-256 evidence remain in the runtime validation directory.
This validates the tool/worker path with real account access; it does not
claim a new native-assistant chat round trip, installation or publication.
