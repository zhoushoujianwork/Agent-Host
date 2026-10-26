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

`internal/state/rooms.go` filters earlier room turns through
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

## Correction direction

Preserve exact runtime checks for native continuation. Add explicit evidence for
why context was excluded, and resolve quoted outbound replies to their originating
run within the same authenticated account/conversation/Agent/audience. If a
runtime change prevents native continuation, an explicitly selected prior result
can be supplied as attributed text after current access checks. Never resume an
incompatible native session or silently select an unrelated recent task.

This diagnostic made no production changes, replayed no task and sent no message.
It identifies the cause; the context-continuity correction is not implemented here.

## Delegated inspection

Read-only worker session `1568aa01-5cea-4736-b514-b3b847af8dcc`, profile
`claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`, returned
BLOCKED with exit 0 after 28 calls and 124.306 seconds; estimated worker cost
USD 0.706034 (not verified billing). Its production reads were denied by its tool
policy. The supervisor independently retrieved the target records through the
local read-only viewer API and SQLite `mode=ro` and verified the code boundary.
