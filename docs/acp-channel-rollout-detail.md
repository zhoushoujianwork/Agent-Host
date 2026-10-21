# Shared ACP channel rollout details

[Main rollout record](acp-channel-rollout.md) owns scope and user-facing results.
Acceptance was performed on 2026-10-21 in Asia/Singapore.

## Deployment and isolation

The inspected runtime is `/Users/mikas/.local/share/agent-host`. At intake it was
served by a source-checkout supervisor rather than the older independent bundle.
The existing viewer assets came from `validation/chat-actions-20261021/viewer-source`.
They were preserved. Ordinary CLI jobs and the separate preview viewer were not
stopped or redirected.

Installed `@agentclientprotocol/claude-agent-acp@0.81.2` under the persistent
`~/.local/share/agent-host-acp` prefix with install scripts disabled. Both
`host.json` execution profiles select `config/execution.acp.json`, retaining
their existing Claude executable/model/provider settings. Each channel's native
`skills` link points to the operator-installed catalog used by the formal CLI
destination. Agent/audience profile isolation and task-scoped memory plugins
remain in force.

Private evidence and pre-change configuration/database snapshots are under
`validation/acp-channels-20261021T022529Z`. The pointer
`validation/acp-channels-current` locates that directory. Reports retain actual
source-send task/message IDs, Host/native sessions, correlated tool results and
delivery acknowledgements. They are local private artifacts, not Git contents.

The initial service check rejected the new configuration before stopping the
old receiver. After fixing discovery, automatic restart safely refused the old
executable path. Its exact idle supervisor PID/command and both idle channel
states were verified before graceful shutdown. The replacement uses the same
state roots, accounts and viewer source. There was never a deliberate second
receiver for either bot identity.

Concurrent checkout work was changing CLI lock/state behavior. The rollout
binary was therefore built from committed base `eb10564` with only the files
listed in the private `source-revision.json` overlaid. `deployment.json` records
the deployed executable and SHA-256. This separates the tested deployment from
unrelated uncommitted changes; it does not silently deploy those changes.

## Fixes

- `discoverUnifiedChannels` recognizes private `execution.<identifier>.json`
  profiles, verifies path ownership/mode and invokes the strict ACP loader.
  Regression coverage checks loading, retained selection, invalid versions and
  non-private profiles. Unknown configuration names are still rejected.
- `harness.MCPServer.SessionKey` is private Host metadata. History derives it
  from the state home and receipt-derived authorization fingerprint; context
  derives it from the source policy, home/config, Agent, audience, project,
  scope, runtime and selected sources. Current receipt/run IDs remain in actual
  launch arguments. ACP compares stable authority only for these explicit
  bindings, and still compares server name/executable and the full arguments
  of ordinary MCP servers. The metadata is excluded from native wire setup.
- ACP's execution fingerprint includes the new receipt-binding policy revision,
  so pre-fix native bindings renew before dispatch. Tests check successful
  continuation with a new receipt, current arguments forwarded on load, and
  rejection when authority/executable changes. Provider tests check receipt/run
  rotation and scope/source-policy changes. Existing tool authorization remains
  checked on every call.
- Shared per-turn memory guidance explains that Host screens eligible user
  declarations and execution evidence after the answer. Native tools and native
  auto-memory settings cannot establish whether Host persisted the current turn.
  Both positive and negative unsupported publication claims are prohibited.
  Initial and resumed Host-turn tests verify delivery of that guidance.

## Retained live observations

DingTalk intake used the already configured DWS profile. `bot find` resolved
the existing bot name to a unique Open ID; `conversation-info` matched the
previously admitted private conversation. Source-send status was `SUCCESS`.
The real Stream receiver created run `c91e5c70-130a-4a38-ab5b-53fed43ed005` through
`harness.acp`. It recalled five records including the existing JMS declaration,
successfully invoked `jms-ssh` and Confluence search, then recorded completed
execution and accepted reply delivery. A separate DWS platform history read
returned both the actual source message and matching bot reply, with complete
coverage for the bounded range.

Feishu's existing CLI user token lacked IM user-send scopes. No OAuth grants
were expanded. Authorized source messages were sent through the logged-in
desktop conversation; the retained Host receipt verifies the configured sender,
account and conversation. Run `cba0de1e-aac1-439d-9a78-a5f5d19d3b07` automatically
saved a synthetic declaration, then `/new` was accepted. Run
`cd379368-6fac-4952-b85d-ead9473ad2ae` used a different Host and native session,
received that declaration in persisted context and returned its exact random
codename without it appearing in the new request. Real `Skill`, Confluence
search and page read succeeded; the bot reply was accepted and independently
visible in the desktop chat. No history tool supplied that answer.

The initial Feishu acknowledgement incorrectly denied persistence even though
the Host later saved the fact. This drove the shared guidance fix. Follow-up
runs `1f0c5011-5800-4253-af7d-87393fbe462d` (Feishu) and
`194b9643-0ece-4a45-8a59-45a8fbe63ddc` (DingTalk) then exposed the receipt/session
identity bug. Both failed before native tool work; failure replies were accepted
and evidence retained. They were not treated as successful continuation or
silently replayed. New explicitly marked read-only probes validate the fix.

An isolated CLI/Web probe also passed all three turns in 30.44 seconds before
the receipt-binding fix; the final deployed revision passed in 55.61 seconds. This uses
explicit ACP selection and synthetic local state/sources, not the running
channel databases. It does not change the ordinary CLI handoff's defaults.

### Final post-fix channel checks

| Channel | Tool/Skill/memory run | History continuation run |
| --- | --- | --- |
| DingTalk | `039b7630-a7e6-43bb-9c12-e279e1d1f47e` | `2958b348-a09c-449d-94ab-41a9702b3adb` |
| Feishu | `afbe45d8-d1ce-479d-b2f7-48f828b4c383` | `c1a63533-1c4a-4931-85ba-ce9782569007` |

All four completed through ACP and have accepted reply delivery. Each first
turn successfully invoked `Skill`, Confluence search and page read. DingTalk
received five existing memory records; Feishu received its exact saved test
fact. Each follow-up retained the same Host/native session, with the adapter
reporting `resumed=true`. Real history search/read results contained the exact
previous receipt with `delivery_status=accepted`, excluded the current receipt
and reported `current_request_excluded=true`. Coverage remained explicitly
partial for the bounded admitted-history window.

Feishu's final answer correctly explains that the absence of a native
memory-write call does not imply Host failed to save: Host screens after the
reply, and its receipt determines publication. After these checks, the private
memory-control socket disabled only the synthetic fact at expected revision 1;
the response confirms `invalidated`, revision 2. Its original evidence remains.
`acceptance-summary.json` and `test-memory-cleanup.json` retain those assertions.
A final bounded DWS platform read independently returned all four source/reply
messages for the post-fix DingTalk pair and matched both saved bot replies.
Feishu's successful continuation reply was independently visible in the desktop
chat, including the current-request exclusion and corrected memory explanation.

## Validation and limits

The initial broad checkout check failed in concurrently changing state/session
code. The isolated rollout check then hit a three-second oversized-stream
fixture deadline under package contention. Repeating with `GOFLAGS=-p=2` passed
the full `make check` gate. The final isolated source also passed full
`GOFLAGS=-p=2 make check` and race tests for ACP, history and owner context.
Final checks use the isolated source after all fixes;
the private reports distinguish earlier failures from final successful probes.
The final shared-checkout `GOFLAGS=-p=2 make check` was also attempted. It failed
in the separately changing `TestIndependentCLIProcessesExecuteConcurrently`
with `SQLITE_BUSY` (`update run: database is locked`). That work was neither
included in the deployment snapshot nor changed by this rollout. The isolated
full-check pass is not presented as a passing shared-checkout gate.

Owner-private intake/replies and the tested operations are the acceptance
boundary. Connected services do not establish mail delivery, every Dokki/local
source operation or Confluence writes. Group conversations, meetings and audio,
interactive approvals, channel Stop controls, sustained throughput and general
retrieval quality remain separate. Missing ACP billing/usage stays unavailable;
auxiliary model cost is not substituted for business execution cost.
