# Everyday reliability: validation record

Historical evidence for the 2026-10-12 revision. Its prompt-only group policy
was superseded by [owner/member authorization](dangerous-operations-design.md).
These results do not validate the later approval flow.

Companion to the [delivered release](release-daily-use.md).
[Remaining work](release-follow-up.md) is tracked separately. These records keep
execution, reply delivery, task acceptance and memory persistence distinct.

## Delivered changes

- `de3b565`: default-workspace continuity, owner/group prompts and failure replies.
- `d03252a`: public actual-model and execution-time footers.
- `afbfb7b`: optional progress failures preserve execution and later evidence;
  mandatory evidence failures and parent cancellation still propagate.

Current behavior is described in the [conversation guide](channel-conversations.md),
[instance guide](agent-instances.md) and [history guide](conversation-history.md).

## Local validation on 2026-10-12

`make check` passed. The latest isolated native run passed seven cases in 71.7
seconds using configured `claude-sonnet-5[1m]`; 45 focused local cases and the
preceding 34 progress/failure race cases also passed. Selective SQLite rejection
and display deadlines verified that optional hint failures preserve later tool
and completion evidence. They do not establish the production wait's root cause.

Native checks covered continuation, history search/read after `/new` and
recovery, ordinary group work and owner/claimed-owner disclosure requests.
Senders, routing judgments, memory admission and delivery were fixtures; these
runs do not establish real non-owner platform use or two real members' follow-ups.
Earlier fixture failures remain in reports `01` and `02`; report `03` passed.

## Real DingTalk checks on 2026-10-12

| Window | Result |
| --- | --- |
| Daily use | 12 distinct requests, 12 accepted replies, 24 exact source/reply reads. Of 11 business turns, ten completed and one was canceled; two channel turns failed, including a post-execution saving failure. Real Jev saved an attributed preference; memory/history after `/new` and owner group behavior were observed. |
| Public footers | Two real owner private/group turns completed; all four source/reply messages were reread with actual-model and execution-time footers. |
| Controlled interruption and recovery | Four distinct requests, four accepted replies, eight exact reads. Three business turns completed; the native leader was interrupted with source/ancestry proof and failed as intended. A new request without `/new` used fresh sessions, linked prior context and saved memory without replay. |

The first window's optional progress write exceeded five seconds and canceled
execution; a separate experience extraction failed after business completion.
The latter's upstream cancellation and nearby restart do not establish the
restart initiator. Three early wait attempts sent no signal and are not
interruption proof; the later controlled check supplied that proof.

The first post-`/new` recall also received recent-request context, so it does not
isolate memory causation. Earlier group marker checks do not prove independent
secrecy or tool isolation. The later recovery had exact source-linked memory,
no recent-request handoff or tool calls, and retained the original failed records.
Its wait tool reported rejection/interruption, not proof that sleep had started.
Reply acceptance and message existence do not establish human task acceptance.

Test preferences were invalidated with source evidence retained, bounded
listeners exited, and production was restored. The final deployed binary was
`afbfb7b`, SHA-256
`9d0b655920073d27f3c0af9f46aff44fe3f5b67aa04d047e42375323c364f5be`;
the latest recheck found its managed process alive and its receipt unchanged.
The installer proves connection only; platform evidence is recorded separately.

## Retained evidence

Private reports under `~/.agent-host/validation/` retain raw receipts, failures,
run/events, cleanup and deployment proof; real content and platform IDs stay
outside Git:

- `daily-use-native-2026-10-12-03.json`; earlier `01` and `02` reports remain siblings.
- `daily-use-dws-20261012-151544/report.json` and sibling snapshots/backup;
  comparison preserved all original rows and values in 16 evidence tables.
- `channel-footer-dws-20261012-163505/report.json`.
- `daily-recovery-dws-20261012-170707/report.json` and independent verification.
- `daily-use-recheck-20261012-174647/native-report.json`.
