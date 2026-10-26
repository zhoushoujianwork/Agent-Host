# DingTalk context discontinuity diagnosis

Read-only inspection of execution `97d25846-656d-4ff6-b839-d26ce3e86b48`
on 2026-10-26 established a runtime-context discontinuity, rather than a change
of channel conversation, Agent, audience or room.

## Observed behavior

The owner sent “结果发给 马理想” at 18:58:14 UTC+08, quoting a bot message.
The resulting run's context contained only the canceled 18:56 “hi” request.
The assistant consequently reported that it could not identify the requested
result. No result was forwarded by that run.

| Record | Host session | Runtime fingerprint prefix | Context |
| --- | --- | --- | --- |
| 18:37 completed service check, run `18320775-16cc-4b57-ad02-6549a763bcd8` | `223ae7bf-c819-4dad-a02e-c498a035c7a8` | `6992a760` | Prior task inherited |
| 18:56 canceled greeting, run `238255cf-98fa-4465-a88a-8df85b8bb16b` | `d6679f23-9df8-42a6-8e97-bc66ea3fcc87` | `1c5c2a32` | Empty |
| 18:58 result-forwarding request | `02c9c460-b2f2-443e-9fd0-b497b5ec70a0` | `1c5c2a32` | Canceled greeting only; no native parent |

All three share room
`5c7164a35df5852d3152a5fb840f777bc55bce78eed3e75fc9e4cfa08d875cbc:1`,
Agent `default`, audience `owner`, workspace, scope and requested model.
Different Host/native session IDs per room turn are normal for the fork design.

## Cause and limits

At diagnosis time, `internal/state/rooms.go` filtered earlier room turns through
`sameRecoveryRuntime` before adding their text or choosing a native parent.
`internal/state/channel_recovery.go` requires exact fingerprint equality.
The 18:37 task therefore could not contribute context to either later request.
The canceled greeting was compatible, so it appeared as history but could not
become a completed native parent.

The quoted message ID was retained, but its received body contained only
“Agent-Host 回复”. The room code stores `ReplyToID` as provenance; it does not
resolve a bot reply ID to its originating task/result. No matching outbound
message mapping was found in the persisted channel-message payloads. Thus the
quote did not restore the intended result.

The fingerprint includes assembled configuration, native settings, project/access
scope and other capability fingerprints. Stored session records retain the hash,
not the complete historical configuration inputs. The exact setting responsible
for this change was not established and must not be guessed from service restarts.
Recipient identity resolution is a separate issue from the missing task context.

## Source correction (2026-10-26)

The source now separates readable room history from native continuation.
Bounded, attributed text and completed results survive runtime fingerprint and
model changes within the same Agent/audience, room epoch, scope, primary project,
workspace, full project identity/scope/workspace set and additional directories.
Results must have completed and received their final journal seal by the incoming
request's arrival. Current capabilities govern new execution; history grants no
permissions and does not assert equality of historical tool/native settings.

Native parents and explicit takeovers still require exact runtime/model
compatibility. Durable `native_continuation` and entry `text_only_reason` explain
the selection without exposing content excluded by scope. A compatible older
completed parent can still be selected while text from other eligible turns
remains available.

The current DingTalk session-webhook adapter receives an acceptance status
(`errcode:0`), not an outgoing message ID usable for a durable quote-to-run map.
The separate notification `processQueryKey` is not established as a quoted
message ID. Preview-only quotes therefore remain unresolved. The prompt preserves
the quote ID and supplies eligible historical results as evidence; ambiguous
references require clarification rather than guessing a result or replaying work.

Focused fixture tests cover the completed-A / canceled-B / quoted-followup-B
pattern, model changes, compatible older parents, scope isolation and arrival
sealing. This is a source fix, not production deployment or live channel acceptance.
No production changes, task replays or messages were made during this correction.

Supervisor acceptance additionally used a private SQLite backup of the original
production records. Constructing an isolated room snapshot at the original
request's arrival retained the completed service-check result as attributed text
(`history=9`) with no native parent. No harness, transport or original request
was invoked.

Supervisor `make check` passed vet and the changed state/Host/channel packages,
but its full Go run failed existing CLI fixture startup/overlap time bounds under
load. An isolated rerun of `TestChatCallerLifecycle`,
`TestIndependentCLIProcessesExecuteConcurrently` and
`TestCLIWorkbenchObservesOfficialChatAfterStartup` passed (96.404 seconds).
All remaining 31 plugin/Python/Node check commands passed separately. The initial
single `make check` invocation is not claimed as passing. Deployment uses the
committed artifact and retains a runtime-local receipt after connection checks;
historical failed requests are not automatically replayed.

## Delegated inspection

Read-only worker session `1568aa01-5cea-4736-b514-b3b847af8dcc`, profile
`claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`, returned
BLOCKED with exit 0 after 28 calls and 124.306 seconds; estimated worker cost
USD 0.706034 (not verified billing). Its production reads were denied by its tool
policy. The supervisor independently retrieved the target records through the
local read-only viewer API and SQLite `mode=ro` and verified the code boundary.

Implementation used session `01a177ee-8ed4-7252-929d-8c72972e24d6`, profile
`codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`. The feasibility probe exited
0 after 8 calls and 89.216 seconds (API-baseline estimate USD 0.284960). The
implementation exited 0 after 17 calls and 311.601 seconds (estimate USD
0.6307168). These are worker estimates, not verified billing or total parent
cost. State tests passed in the worker; Host/channel execution checks encountered
its local-socket sandbox restriction and required supervisor validation.
