# Built-in DingTalk workspace implementation

See the [user guide and scope](dingtalk-workspace.md).

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
