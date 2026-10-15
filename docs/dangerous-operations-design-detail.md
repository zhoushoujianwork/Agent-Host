# Owner-only dangerous operations: implementation details

Status: implemented in source; automated checks and partial native startup evidence
are separate from pending live channel acceptance. See the [main design](dangerous-operations-design.md)
for behavior and first-scope tradeoffs.

## Identity and native-session policy

DingTalk matches the configured enterprise and `owner_user_id` against the
transport sender staff ID. Feishu matches the configured application/tenant and
`owner_open_id` against the sender Open ID. `internal/cli/agents.go` resolves the
current sender before assembling a runtime. Missing owner configuration grants no
owner authority; meeting participation is not an owner binding.

The Host-injected `CURRENT_AUTHORIZATION` contains the policy version, configured
owner ID and current owner boolean. The policy and owner binding participate in
the existing runtime fingerprint, separating owner and member native sessions.
Group Host history and audience privacy remain shared as before. A member saying
“I am the owner” cannot change the profile. Ordinary local CLI behavior is unchanged.

The actual [dangerous-operation system prompt](../internal/host/dangerous_operations.go)
requires specific owner intent, scope clarification and confirmation before
irreversible loss. It denies member execution, offers a bounded approval request,
and treats source content as evidence rather than authority. Owner confirmation
and confinement within arbitrary Bash remain prompt-guided. Host separately
enforces who can authorize a member-originated privileged run.

## Concrete startup examples

These illustrate actual adapter arguments. Host uses Go argv elements, never a
shell assembled from message content. User input arrives through stdin. Host owns
the policy, configured MCP paths, cwd and fresh UUIDs represented by the variables.

Non-owner baseline with no MCP capabilities:

```bash
claude --print --verbose --output-format stream-json \
  --include-partial-messages \
  --bare --setting-sources "" \
  --settings '{"disableAllHooks":true,"autoMemoryEnabled":false}' \
  --permission-mode dontAsk \
  --tools "Read,Glob,Grep" \
  --allowedTools "Read,Glob,Grep" \
  --disallowedTools "Bash,Edit,Write,NotebookEdit,Agent,Task" \
  --strict-mcp-config --mcp-config '{"mcpServers":{}}' \
  --disable-slash-commands --no-chrome \
  --append-system-prompt "$AGH_POLICY_AND_ACTOR" \
  --session-id "$AGH_MEMBER_SESSION_ID"
```

In a supported group with a configured owner, Host additionally mounts only its
`approval` MCP server and pre-approves exactly `mcp__approval__request_approval`.
There are no ambient, knowledge, history, business-command or mixed write MCP
servers in the restricted profile. Native plugins, additional directories and
execution-capable tools conflict with this profile and are rejected.

`--tools` limits built-in inventory; `--allowedTools` pre-approves calls. Restricting
only `Bash(rm *)` would leave script, Git, redirect and API deletion paths. Disabling
file mutation tools also prevents truncation through Edit/Write. Every native init
must advertise a nonempty subset of Read/Glob/Grep plus the exact optional approval
tool. Unexpected or missing inventory fails the run without fallback. The installed
binary may expose fewer read tools; it currently advertises Read alone.

Owner startup retains existing execution configuration:

```bash
claude --print --verbose --output-format stream-json \
  --include-partial-messages \
  --permission-mode bypassPermissions --tools default \
  --strict-mcp-config --mcp-config "$AGH_OWNER_MCP_CONFIG" \
  --append-system-prompt "$AGH_POLICY_AND_ACTOR" \
  --session-id "$AGH_OWNER_SESSION_ID"
```

An empty member permission field is resolved to dontAsk. An explicit bypass or
incompatible restricted setting is rejected. Resume replaces only the session
argument with `--resume "$AGH_COMPATIBLE_SESSION_ID"`; all restrictions are applied
again. Owner/member sessions never share native compatibility. Approved work gets
a fresh separate session rather than elevating the member's process.

## Authentication and settings

Claude Code 2.1.223 exposes the required flags. Bare mode skips OAuth/keychain
credentials and native customization discovery. The adapter preserves only a
reviewed provider environment whitelist from a user-owned, non-writable-by-others
regular `settings.json` in the configured native profile: API key/auth token,
base URL and model keys. These values reach the child environment, never argv or
Host evidence. Other settings and hooks are not inherited; helper-only auth is
rejected rather than running a command. Already protected provider environment
credentials remain supported. No credentials were copied or configured by this task.

Both current channel native profiles lack provider auth environment settings.
The user explicitly chose to retain bare mode and configure API/provider credentials
separately. The real smoke observed only Read, then failed with “Not logged in”.
This establishes partial startup inventory evidence, not a successful model/tool
turn. No weaker permission mode or provider fallback was used.

Official references: [CLI flags](https://code.claude.com/docs/en/cli-reference) and
[permissions](https://code.claude.com/docs/en/permissions). Online documentation
can describe newer versions; this implementation does not depend on `--restricted`,
which is newer than the installed binary.

## Proposal, mention and approval lifecycle

1. The narrow `request_approval` MCP accepts only operation, target, impact and
   an explicit group-shareable flag. Private, oversized, malformed or extra fields
   are rejected. It cannot choose an owner/destination, send a message or execute.
2. A private per-run directory holds at most one bounded proposal. Exact repeats
   are idempotent; a different proposal is rejected. The response explicitly says
   the owner has not yet been notified. Host consumes it only after successful
   native completion and removes the temporary directory. Failed turns send nothing.
3. Host persists an approval record before sending. It binds the original receipt
   and proposal run, configured owner, bot/account/tenant/conversation, operation,
   target, impact, project IDs/workspace and a 15-minute expiry. Duplicate proposals
   for the same source and action do not send another owner mention.
4. Feishu sends a genuine `at` post node and requires a validated outgoing message
   ID/chat acknowledgement. The owner's direct `parent_id` reply selects that record;
   ancestor `root_id` and quoted message bodies cannot select it. Exact owner replies
   OK/Approve/No/Reject can enter without a bot mention; other group intake remains
   bot-mention based.
5. DingTalk sends through the original reply capability with real `atUserIds`.
   Its webhook acknowledgement has no outgoing message ID. The prompt therefore
   requires `@bot Approve <24-hex-request-code>` or `@bot Reject <code>`. The code
   identifies a saved operation, not a provider message. Same-owner/account/tenant/
   conversation checks still apply. An uncorrelated OK approves nothing.
6. Host checks the actual persisted reply receipt and current configured owner.
   Atomic transitions reject non-owners, incorrect decisions, duplicates, stale
   bindings, expired requests and unconfirmed deliveries. Worker startup and minute
   sweeps cancel old owner bindings and expire pending records. Expiry is persisted;
   subsequent approval attempts explain it without launching work.
7. Approval is consumed before project resolution/dispatch. Host validates the
   saved workspace and existing Agent project allowlist, creates a fresh session,
   and atomically binds one execution run. The owner's original reply remains the
   run input; the recorded operation arrives separately through Host instructions.
   Original member request, approval receipt and actual run remain distinct evidence.
8. Failures or uncertain launches are reported without replay. Failed/unknown
   mention delivery is recorded and is never accepted as approval. No automatic
   retry of a consumed destructive action occurs. Changed targets/impacts require
   a fresh proposal and approval. Meetings have no approval-send capability.

Approval grants permission to launch a scoped privileged Claude run; unrestricted
owner Bash is not a hard per-operation executor. Strong target confinement needs a
typed Host action executor. This capability boundary is not OS isolation against a
compromised native executable. Read/data disclosure scope is governed by existing
channel policy; this change specifically removes member mutation/execution tools.

## Validation and rollout

Automated suites cover restricted argv/resume, inherited customization exclusion,
auth settings safety, native inventory rejection, MCP proposal validation, provider
mention/reply metadata, owner matching on both channels and meetings, durable
approval correlation, expiry/rebinding, duplicate/concurrent consumption, one-use
run dispatch, workspace changes and notification/native failures.

The opt-in `TestRestrictedNativeSmoke` uses disposable files and a temporary native
profile; it requires protected API/provider auth to verify a complete read turn and
rejected mutation attempts. It remains unaccepted locally because authentication
is absent. No live group approval messages were sent, no services were restarted,
and no live DingTalk/Feishu approval round trip is claimed. Configure credentials,
then run the native smoke and bounded disposable-resource platform acceptance
before treating this path as production-validated.
