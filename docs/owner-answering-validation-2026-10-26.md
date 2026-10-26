# Owner answering: implementation and diagnostic evidence

## Independent observation follow-up

The subsequent correction records durable per-message observations separately
from the moving collection watermark. A fixed activation baseline excludes old
imports while late unseen messages remain eligible. Owner outgoing activity is
retained separately from agh execution and delivery; viewing/read receipts never
imply agh completion or manual takeover. Focused tests passed (58 cases), and the
supervisor's complete `make check` passed after this correction. Production
activation remains pending.

The implementation worker resumed session
`01a17717-f55c-7131-a402-aec32cb89759`, profile
`codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`: COMPLETE / exit 0,
22 calls, 180.523 seconds, API-baseline estimated worker USD 2.0275672
(not verified billing). The separate
[session-context diagnosis](session-context-diagnosis-2026-10-26.md) records the
user-reported execution's missing context and its remaining correction.

This report accompanies the [feature guide](owner-answering-beta.md). Source
implementation checks and production behavior are separate. The new automatic
intake path has not been activated in production by this work.

## Existing private-chat task

The owner-requested DingTalk run `21165a30-5906-48ee-8e6e-6c684bf7f930`
started at 15:21:10 on 2026-10-26 (UTC+08). Read-only inspection of its persisted
events and channel receipt established:

- `search_local_markdown("mutiboard")` started at 15:25:03 and returned at
  15:44:15: approximately 19 minutes 12 seconds. The result reported 151 files,
  665,240 bytes and two matches. The trace establishes the slow tool boundary,
  but not the underlying filesystem or process-scheduling cause.
- The requested personal reply was submitted at 15:48:03. The provider's
  `get_send_status` result reported `SUCCESS` at 15:49:52. This establishes
  platform delivery status, not recipient reading or issue resolution.
- Native execution completed. At 15:51:55, subsequent experience extraction
  failed with `experience source exceeds 131072 bytes; no truncation performed`.
  The run retained `execution_status=completed`, `memory_status=unchanged` and
  `experience_status=failed`; the channel's final reply was `accepted`.
- No restart, cancellation, replay or additional message was performed by this
  diagnostic. Production had no personal-discovery configuration enabled.

The initial delegated report's inference that the pause was a long model turn
was not supported. Direct review traced the entire delay to the outstanding
local-document tool call and confirmed the later send receipt.

## Diagnostic execution

The `agent-host-tasks` handoff used session
`3fd0976d-ff8b-46cb-9217-0dede4d74bc2`, run
`334b7a16-f05c-4522-a6ed-673073f154c6`. Its native inspection completed, but the
launcher exited 1 after a separate experience-extraction error (`unknown or
duplicate evidence reference`). Elapsed time was 247.030 seconds; model was
`claude-sonnet-5`. Observed native usage was 806,710 input and 4,771 output tokens,
with a native-reported estimated cost of USD 2.650152. This excludes JEV and any
unreported calls and is not verified billing. Its conclusions were independently
checked against the production records above.

## Source validation

Supervisor review covered verified identity bindings, atomic intake and active
question recovery, terminal ACP outcome parsing, bounded cancellation, async
delivery reconciliation and tool-free owner clarification. No production
acceptance of the new automatic-intake path is claimed.

The supervisor could run the composed Host test outside the worker sandbox. Its
initial run failed because the test's tool execution completed but its deliberately
absent learning adapters produced `experience extraction and judgment are required
for tool evidence`. This exposed the execution-versus-learning result handling
covered by the requested corrections; it was not a localhost-listener failure.
The corrected composed Host test passed independently outside that sandbox.

An initial full check overlapped an unfinished worker edit and stopped on a
temporarily undefined field. After correction, `go vet ./...` passed and the
full Go test run passed the owner-answering, discovery, state and channel packages,
but failed the existing `TestChatCallerLifecycle/parent-exit` fixture's 12-second
process-start wait. The targeted rerun passed all five cases. The final full check
passed all Go packages, Go vet and both plugin modules. It then stopped in the
installer tests because concurrent viewer-control work referenced
`tools/run-viewer/record_control.py` before that file was created. That unrelated
file is now present; separate reruns passed all 20 installer and 23 viewer tests.
The complete `make check` pipeline is therefore not claimed as passing. No
production processes or desktop applications were manipulated by validation.

The viewer's schema allowlist was extended to 17. Its additive-schema test now
also verifies that private owner-question records do not enter the ordinary run
views. All 23 `tools/run-viewer/server_test.py` tests passed headlessly.

## Delegated implementation evidence

All development runs used profile `codex-gpt-6.1-sol-medium-fast`, model
`gpt-6.1-sol`, session `01a17717-f55c-7131-a402-aec32cb89759`.

| Run | Status / exit | Tool calls | Elapsed seconds | Estimated worker USD |
| --- | --- | ---: | ---: | ---: |
| Feasibility follow-up | Error / 1: model at capacity | 30 | 89.492 | Unavailable |
| Initial implementation probe | Blocked report / 0: ambiguous provider identity namespaces | 16 | 138.795 | 0.3777448 |
| First implementation with explicit identity bindings | Source implementation returned / 0; supervisor corrections required | 114 | 1614.223 | 3.9507032 |
| Six review corrections | Implemented / 0 | 70 | 1622.822 | 3.6040896 |

These are API-baseline worker estimates, not verified billing or total parent
cost. The first implementation reported passing focused tests, but its composed
Host test was blocked by the worker sandbox's localhost listener restriction.
The correction worker reported 639 core tests, 20 focused CLI/channel checks and
49 final coordinator tests passing; its Host fixture remained sandbox-blocked.
Supervisor validation remains distinct from those reports.
