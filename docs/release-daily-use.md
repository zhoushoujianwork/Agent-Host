# Release record: everyday reliability

Delivered and deployed on 2026-10-12. This is a historical release record.
Its group tool policy below was superseded by [owner/member authorization](dangerous-operations-design.md)
on 2026-10-15; use [current progress](release-follow-up.md) for present behavior.

- Default-workspace tasks retain history, memory and conversation continuity,
  including after `/new` and failed-session recovery.
- Owner private requests execute without added Host restrictions. Groups use
  the same tools, with system-prompt judgment even when the sender is the owner;
  this adds no member permission system or enforced tool isolation.
- Failures explain the outcome, possible partial effects and next step. Recovery
  preserves evidence and never silently replays a task; optional progress
  failures no longer cancel it.
- Private and group replies show the actual model and recorded execution time
  in the public footer when available.

Validation: `make check`, seven isolated real-model cases and 45 focused local
cases passed. Real owner DingTalk checks covered memory/history, group replies,
public footers and automatic recovery after controlled native interruption.

See the [validation record](release-daily-use-detail.md) for evidence and its
limits. Remaining checks and deferred ideas are in [release follow-up](release-follow-up.md).
