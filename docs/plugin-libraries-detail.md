# Plugin library implementation

The [main decision](plugin-libraries.md) governs scope and ownership.

## Module and dependency direction

`plugins/feishu` is the module
`github.com/zhoushoujianwork/Agent-Host/plugins/feishu`. It imports only the Go
standard library. The root Host module requires it with a checkout-relative
`replace`, so building a source checkout does not require a published version.
External consumers require this submodule, not the `agent-host` root module.

`internal/channel/feishumeeting` retains Host configuration, allowlists,
`AI:` question selection, `[AI]` reply presentation, baseline/cursor files,
entry intent/recovery and session routing. Its CLI types alias the library
transport; its join, membership, poll and reply paths call the library.
The existing event subscriber and its owner-death supervisor remain internal.
The library does not start a competing subscription for the same application.

The library's Unix subprocess helper owns bounded request processes. The Host
subscriber retains its own corresponding process-group helper because its
supervised lifetime differs from one request. Existing descendant-cleanup
tests exercise the public request transport through the Host's type alias.

## Public contract

| API | Contract |
| --- | --- |
| `NewMeetingClient(Config, Transport)` | Local identity syntax validation; no network or background work; nil transport uses `lark-cli` |
| `CheckIdentity(ctx)` | Calls named-profile `whoami --as bot`; verifies profile, app ID, bot identity mode and local credential availability |
| `Join(ctx, JoinRequest)` | One mutation using a nine-digit number; optional verified invitation call ID and expected long ID; acknowledgement does not prove presence |
| `Membership(ctx, id, expectedNumber...)` | `present`, `absent`, `ended` or `unknown`; incomplete/error responses never establish absence |
| `PollEvents(ctx, id, cursor)` | Validates page and event identities before returning; retains raw payloads, cursor and `HasMore`; caller owns pacing and durable advancement |
| `Event.ChatItems()` | Decodes meeting text items, preserving actor and source timestamps; no sender authorization or command interpretation |
| `SendText(ctx, id, uuid, text)` | Sends exact caller text with a stable caller-persisted UUID; no Host prefix or silent retry |
| `Leave(ctx, id)` | One explicit bot-departure attempt; never ends the meeting for other participants |

Mutation results are `accepted`, `failed` or `unknown`. Only explicit local/auth
rejection or failure before process start establishes a failed write. Network
errors, malformed/mismatched acknowledgements and process failure after starting
remain unknown. Callers retain intent and reconcile uncertain outcomes; the
library never automatically joins, sends or leaves again.

CLI requests use argv without a shell, a 30-second timeout, a 4 MiB stdout limit,
a 64 KiB stderr limit and process-group cleanup on Unix. Returned errors omit
raw CLI diagnostics and credentials. `Output` is private evidence, not log text.
The default CLI transport explicitly rejects non-Unix platforms; a consumer can
supply a transport with its own supported lifecycle.

The extraction's subprocess regression exposed an inherited output-limit bug:
embedding `bytes.Buffer` promoted `ReadFrom`, so `io.Copy` could bypass the
bounded `Write`. The module now holds a named private buffer. Real subprocess
tests verify both stream limits, cancellation and generic error reporting.

Poll at intervals of at least five seconds. Validate all event identities before
handling items; commit the cursor only after accepting the page. Host retains
its existing startup baseline and scope hash, so extraction does not replay
history or invalidate existing cursor files. Unknown event payloads remain
available to external consumers without being promoted to Host tasks.

## Identity probe and audio blocker

`agent-host feishu probe --config PATH` (also `feishu-meeting probe`) runs only
the library identity check. It does not require native model configuration or
open/create Host state. Existing `check` remains fully local and does not launch
a CLI process. Neither command proves remote scopes or realtime audio access.

The read-only environment check on 2026-10-26 found that the configured Host and
installed CLI profile identify a different app from the requested voice-test
app. No credentials, service settings or subscriptions were changed. The user
must identify the target profile/configuration before its live test can proceed.
The built Host's `probe` passed with the installed configuration (exit 0). A
private temporary copy using the requested app ID with the installed profile
was rejected (exit 1, no success output). That copy was removed after the check.

The supplied [meeting manual](https://bytedance.larkoffice.com/docx/CZUOdaFtQoNbTAxVilbcPcqonGh)
describes an optional voice extension and references `GetBotRealtimeEndpoint`,
but its retrieved content omits Step 4. The
[official meeting SDK guide](https://github.com/larksuite/channel-sdk-python/blob/main/docs/meeting-channel.md)
documents meeting events and text. A focused inspection of official CLI,
Go SDK and Node channel SDK source did not find the referenced endpoint symbol.
The endpoint path, credentials, audio encoding/framing, send/receive controls,
expiry/reconnect and interruption acknowledgement must be verified before the
audio adapter is implemented. No public voice API claims working Feishu audio.

## Verification

`make check` includes the nested module's vet and tests, and formatting includes
plugin sources. Its independent-consumer test creates another Go module and
executes join, membership, poll, text and leave with a synthetic transport while
`GOPROXY=off`; it imports no Host internals and requires no local credentials.
Library tests cover identity mismatch, cancellation, malformed responses,
uncertain writes, no automatic retries, destination validation and payload
preservation. Existing Host tests cover admission, startup baselines, durable
cursor handling, lifecycle and process cleanup through the shared transport.

The source exporter includes `plugins/` and still excludes credentials/runtime
files within it. A source-only checkout includes the nested module; no release
asset or published module version is created by this increment.

These are offline implementation checks. They do not establish a successful
live meeting audio session or GTMGoPlay business acceptance.

### 2026-10-26 acceptance receipt

- `go -C plugins/feishu test -race ./...`: passed under supervisor execution,
  including the independent consumer and real subprocess output-limit tests.
- `GOFLAGS=-p=1 make check`: passed all Go, Python and JavaScript checks, including
  the nested module. An earlier default-concurrency run timed out in the existing
  `TestChatCallerLifecycle/parent-killed` fixture. Its isolated rerun and the full
  serialized-package run passed; that unrelated test was not changed.
- `make build`: passed. `git diff --check` and local documentation links passed.
- Live read-only profile probe: installed configuration passed; requested app
  on that profile was rejected, as described above. No meeting was joined or
  audio sent by these checks.

## Supporting worker receipts

The supervisor inspected the reported source and test changes and independently
reproduced the output-limit failure. Execution status is distinct from task
acceptance: the first research run and first test run returned useful blockers,
then continued within the same respective session. Costs below are worker
estimates only; they exclude supervising work and are not verified billing.

| Work | Execution / result | Profile / model | Turns | Elapsed | Estimated USD | Session |
| --- | --- | --- | --- | --- | --- | --- |
| Initial localization | ok / exit 0; report BLOCKED on unavailable enumeration tools | cc / claude-sonnet-5[1m] | 23 | 38.232 s | 0.202026 | `2df861b2-636c-49ab-b4d3-5ee1ed4521f6` |
| Localization with exact filenames | ok / exit 0; compatibility findings inspected | cc / claude-sonnet-5[1m] | 8 | 40.418 s | 0.228504 | `2df861b2-636c-49ab-b4d3-5ee1ed4521f6` |
| Subprocess regression tests | ok / exit 0; report BLOCKED on reproduced output-limit bug; test exit 1 | codex / gpt-6.1-sol | 11 | 136.739 s | unavailable | `01a17609-86df-7940-b857-e1117b4661e5` |
| Bounded-buffer fix | ok / exit 0; worker race tests exit 0 | codex / gpt-6.1-sol | 6 | 45.483 s | unavailable | `01a17609-86df-7940-b857-e1117b4661e5` |

## Phase-one speech library

`plugins/speech` is the independent module
`github.com/zhoushoujianwork/Agent-Host/plugins/speech`. The root package contains
provider-neutral ASR/TTS contracts only. Its `doubao` subpackage pins
`github.com/coder/websocket v1.8.15`, matching the read-only GTM protocol sources.
It imports no Host/GTM internals and owns no model or conversation loop.

ASR validates PCM16/16kHz mono before connecting, frames sample-aligned input in
at most 12800-byte chunks, and separates explicit input completion from immediate
cancellation. Definite utterance events do not finish a stream: only the
provider-final frame does. TTS accepts once-submitted text (at most 10000 UTF-8
bytes) on legacy `/api/v1/tts/ws_binary`, with explicit app/token/cluster and
voice. Output streams as PCM16/24kHz mono; newer bidirectional TTS and collected
MP3 are outside this slice.

Legacy TTS ACK and sequence framing follows the
[official interface](https://docs.volcengine.com/docs/DoubaoVoice/Websocketinterface?lang=zh)
and [provider-hosted sample](https://developer.volcengine.com/articles/7500048059624554515).
Header-only flags0 ACKs are ignored; the earlier eight-byte zero-length ACK is
also accepted. Audio flags1 requires a positive signed sequence, while flags2/3
requires a negative sequence before the length. ASR input-completion flags2
remains size-only. Literal-byte regressions exercise ACK and flags2 final audio
through streaming TTS. Completion with no audio is an explicit error.

The shared parser bounds message/decompressed payloads to 1 MiB, with an 8 MiB
total TTS output limit. Channels are bounded; cancellation releases socket I/O,
queued writes and output backpressure. Zero connect timeout means 10 seconds,
including the initial write. Failures are terminal, with remote message text
and credential-bearing dial diagnostics omitted; there is no retry or replay.

Local `httptest.Server` WebSocket fixtures use `net.Pipe` to avoid loopback bind
permissions. They exercise framing, completion, streaming, cancellation,
validation, truncation, malformed frames and decompression/output limits. A
separate temporary Go module imports the public API with a local replacement
and the offline module cache. `Makefile` includes speech vet/tests; the
[compile-checked turn example](../plugins/speech/example/turn.go) keeps the
Agent/model callback between ASR and TTS. The example aggregates nonempty
definite segments in order, replaces identified revisions, deduplicates exact
text without identity and rejects partial-only input. Its tests cover
aggregation, errors, cancellation and playback. It is educational caller-owned
turn policy, not a core orchestration API. The independent-consumer subprocess
uses a quoted replacement path and a context timeout. See the
[README](../plugins/speech/README.md) for formats, limits and caller responsibilities.

This increment provides no live speech acceptance, Feishu audio transport,
automatic barge-in, VAD or complete meeting integration. Host and GTM runtime
configuration, credentials and existing speech implementations are unchanged.

### Speech implementation receipts

Both worker runs used profile `codex-gpt-6.1-sol-medium-fast`, model
`gpt-6.1-sol`, session `01a17624-2f3d-77e0-8402-fb896c4c8cb2`.
Worker cost estimates are unavailable; elapsed time is measured and excludes
supervising work. Reports were checked against the actual code and tests.

| Run | Status / exit | Tool calls | Elapsed | Estimated worker USD |
| --- | --- | ---: | ---: | --- |
| Initial implementation | ok / 0 | 23 | 471.248 s | unavailable |
| Review corrections and regressions | ok / 0 | 8 | 186.325 s | unavailable |

Independent review corrected legacy TTS ACK/sequence handling, rejected empty
synthesis, and made the example aggregate definite transcript segments before
calling the Agent. Module race tests passed after those corrections, including
the independent consumer and example behavior checks. No live provider or
meeting calls, production restart, or GTM application changes were performed.

Final supervising validation passed: `go -C plugins/speech test -race ./...`,
`GOFLAGS=-p=1 make check` (Go vet/tests, both plugin modules and the repository's
headless Python/JavaScript suites), staged whitespace review and local document
link checks. Package compilation was serialized to avoid contention; no test
was skipped or replaced. Desktop and live-provider acceptance remain pending.
