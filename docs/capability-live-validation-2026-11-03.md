# Capability rollout and DingTalk acceptance — 2026-11-03

[Capability direction](agent-capability-runtime.md) ·
[Isolated acceptance](capability-validation-2026-11-03.md).

## Deployment

The personal macOS runtime adopted source commit `8ea975c` through a clean
source build. Both DingTalk and Feishu reconnected. Private configuration,
previous binary and launch definition were retained for rollback. The obsolete
ACP selector was retired; full Host assembly now executes through Offload pkg
using the configured Claude profile. Agent identities, memory roots and sources
were preserved. This does not implement the proposed per-Agent capability grants
or directory migration.

## Live conversation evidence

DWS sent bounded, read-only acceptance requests to the configured robot,
OpenClaw小钉-周守健, using the owner's configured organization/account. A bounded
sender subscription observed the actual robot replies.

| Case | Host run | Observed result |
| --- | --- | --- |
| A: memory and Confluence | `c57f53ab-a5b8-47c3-9ce9-4fdc22ff2c0a` | Memory search, current body and supporting evidence; Confluence search and page read; reply received |
| B: Dokki and local documents | `5d90a928-de3c-4bd3-bb51-60eb9874eae3` | Both sources searched and one result read from each; reply received |

Case A retained `caller=agh` and its exact Host-run correlation. Its memory
outcome was `unchanged`. Dokki's selected HTML file returned metadata and a
signed download reference; the robot explicitly reported that it had not read
the file body. Local document reading was bounded to the first 3,000 characters.
Search counts and snippets are not proof of complete knowledge coverage.

## Discovered display issue

Native output included a closed internal `<agh-closeout>` block followed by a
service notice. The terminal-only acceptance parser correctly refused that
metadata, but the same early return exposed it in the chat reply. The display
fix removes the closed bookkeeping block while retaining the notice. It does
not accept non-terminal metadata or invent a missing run ID. Regression cases
cover current, stale and missing run IDs and malformed JSON.

The original A/B task acceptance status was `unresolved`; execution completion
and independently observed capability calls do not rewrite that outcome.
Native metadata also omitted the required run ID. Provider notice provenance
and complete Dokki attachment extraction remain outside this acceptance.

Case B's post-answer memory closeout failed: a `phase.progress` append encountered
`SQLITE_BUSY` and a context deadline, then memory processing recorded cancellation.
The channel subsequently reconnected. The reply had already been delivered; this
does not establish successful memory closeout or sustained channel stability.
The cause has not been isolated, and this display fix does not address it.

Private rollout receipts retain binary identity, rollback files, message/run
correlation and the final display-fix verification. No third-party document
writes or business task replay were part of these checks.
