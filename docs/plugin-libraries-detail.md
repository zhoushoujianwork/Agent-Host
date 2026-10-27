# Plugin library implementation

The [main decision](plugin-libraries.md) governs scope and ownership.

## Module and dependency direction

`plugins/feishu` is the module
`github.com/zhoushoujianwork/Agent-Host/plugins/feishu`. Meeting control uses the
Go standard library; the audio transport also requires `coder/websocket` v1.8.15
and `google.golang.org/protobuf` v1.36.11. The root Host module requires it with a checkout-relative
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

## Identity probe

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

## Feishu audio transport

The earlier blocker was a missing wire contract. The user-supplied
`doubao-voice-meeting-starter` archive (Python reference; not run, not copied
beyond its two `.proto` schemas) supplied one. The library implements it, but
**protocol confidence is source-only**: nothing here has talked to Feishu.

- Endpoint: `lark-cli --profile P api GET /open-apis/vc/v1/realtime/endpoint --params {"meeting_id":…} --as bot`
  through `Transport.Run`. Stdout must be one JSON document: the CLI envelope
  (`ok`, `data.websocket_url`) or the raw platform response (`code` 0, `data`),
  also when the platform response is nested in the envelope. A process error
  fails the call even if stdout looks valid. The URL must be `wss` (plaintext
  `ws` only for loopback fixtures), without user info or fragment, at most 4096
  bytes. The signed URL is never returned, stored, logged or placed in errors;
  dial errors are replaced by a fixed message and redirects are not followed.
- Framing: each binary WebSocket message is a Frontier `Frame` (proto2,
  service 33555721, method 1, `binary`, `application/x-protobuf`, frame type 0)
  holding a `meeting_realtime.v1` `ClientEvent` or `ServerEvent`. Inbound frame
  types 1, 2, 16 and 32 (ack, need-ack, cursors) and empty payloads are skipped;
  the client sends no acks. Schemas are in `internal/audioproto`; regenerate with
  `go generate` there (needs `protoc`, `protoc-gen-go`).
- Session: `session.create` (session 0, fresh UUID event ID) offers
  `audio/pcm`, `s16le`, 24000 Hz for both directions. `session.created` must
  carry a positive session ID, the same media, and, when present, the request's
  event ID. Later events must name that session.
- Audio: `ReadAudio` returns `AudioFrame` with track, source, format, `PTSMS`,
  `DurationMS`, event ID and creation time. Chunks must be non-empty, sample
  aligned and, if they state a format, 24 kHz mono s16le; labels are at most
  256 bytes. `WriteAudio` rejects odd or over-1 MiB input and never trims, then
  sends ordered `audio.upstream.append` events of at most 4800 bytes (100 ms).
  `ClearAudio` sends `audio.upstream.clear` (barge-in flush). `Close` sends
  `session.close` (`USER_LEFT`); it does not leave the meeting.
- Limits and lifecycle: messages are limited to 1 MiB; at most 1024
  consecutive skipped frames or ignored events; setup (endpoint, handshake,
  negotiation) defaults to 15 s; the close notice to 5 s. Writes, clears and
  close are serialized; reads are serialized separately. An `error` or
  `session.closed` event, malformed or oversize data, a text message, a socket
  failure, or cancellation during socket I/O ends the session permanently with a
  sticky error (`AudioServerError` keeps only the numeric code and retryable
  flag; `AudioClosedError` the close reason). Cancelling a context before any I/O
  is a plain rejection. Cancelling a blocked read or write must close the socket,
  so it also ends the session. There is no retry, reconnect, rejoin or replay.
- Unknown server event types are ignored (bounded). Echo of the bot's own audio,
  resampling, VAD and turn policy belong to the caller.

Tests (`audio_test.go`, `audio_helpers_test.go`) use literal golden frame bytes,
a real HTTP/WebSocket handshake over `net.Pipe`, malformed and oversize input,
cancellation, chunking, serialized writes, clear, bounded close and
signed-URL secrecy. The independent consumer now also carries the module's
`go.sum` and compiles the audio API; it opens no live session.

Pending live acceptance: the service/method constants, 24 kHz in both
directions (the starter's README says 16 kHz upstream, its code uses 24 kHz),
frame-type handling, ack and expiry behavior, error codes, behavior when
the bot is not in the meeting, and whether the installed CLI's `api`
command output matches the parsed shapes. The root Host module needs matching
indirect requirements (`coder/websocket` v1.8.15, `google.golang.org/protobuf`
v1.36.11) in `go.mod`/`go.sum` before it builds with this module; that change is
outside this increment.

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

The speech library itself provides no live speech acceptance, Feishu transport,
automatic barge-in or VAD. The later opt-in [Host meeting adapter](feishu-meeting-channel.md)
owns resampling, energy-based turn detection and half-duplex playback. GTM runtime
integration remains separate.

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

## Meeting voice implementation receipts (2026-10-27)

These worker reports were reviewed against the diff. All runs used profile
`claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`.
Costs below are estimated worker USD, not verified billing or supervising-agent
cost. The timed-out run's full cost is unavailable; its reported $0.19137
estimate covers only the salvaged report. Do not treat that as its execution
cost or infer an overall total.

| Run | Status / exit | Measured elapsed | Estimated worker USD | Session |
| --- | --- | ---: | ---: | --- |
| Protocol feasibility | ok / 0 | 103.293 s | 0.501384 | `4efc6b5d-3147-432b-b072-e55c7cffb1c8` |
| Reusable audio transport | ok / 0 | 1643.678 s | 5.468648 | `4efc6b5d-3147-432b-b072-e55c7cffb1c8` |
| Initial Host bridge | timeout / 124, salvaged partial report | 1833.270 s | unavailable; report only 0.19137 | `3e3fe2fa-1068-40db-bb57-7df69be6ab6d` |
| Resumed CLI integration | ok / 0 | 1036.652 s | 3.990628 | `3e3fe2fa-1068-40db-bb57-7df69be6ab6d` |
| Bridge hardening and regressions | ok / 0 | 1227.231 s | 2.069152 | `2e8e1922-b5a3-42c3-938a-f27ecbcccff8` |

The timeout's partial files were preserved. The resumed work completed CLI
wiring and documentation, while a separate bounded review corrected playback
pacing, generation resets, cancellation, PCM completion and recognition limits.
Supervising review additionally rejected ambiguous endpoint errors, checked
cancellation after gate acquisition, enforced reply expiry on delivery, and
included the current chunk in the 200 ms playback bound. Source attribution
states that the speaker is unverified without inventing a human identity.

No live meeting or speech-provider calls, production configuration changes,
service restarts or desktop interaction were performed.

Final supervising checks passed: `GOFLAGS=-p=1 make check` (Go formatting,
vet/tests, both plugin modules and all headless Python/JavaScript targets),
the Feishu transport race suite, 64 targeted voice/CLI race cases, local links
in seven affected documents and staged whitespace review. Live meeting and
speech-provider acceptance remain pending.
