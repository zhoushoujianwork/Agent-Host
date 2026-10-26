# Streaming speech library

Independent module: `github.com/zhoushoujianwork/Agent-Host/plugins/speech`.
The root `speech` package defines provider-neutral ASR/TTS interfaces. Import
`/doubao` for the observed binary Doubao ASR and legacy TTS implementations.
There are no Host or GTM internal imports, model loops, credentials loaded from
disk, automatic retries, replay or provider fallback.

## Use from another application

For a local checkout, add a module requirement and replacement:

```go
require github.com/zhoushoujianwork/Agent-Host/plugins/speech v0.0.0
replace github.com/zhoushoujianwork/Agent-Host/plugins/speech => "/absolute/path/Agent-Host/plugins/speech"
```

A remotely resolvable version needs a separately authorized source publication.
No binary plugin loader is needed.

Supply configuration explicitly from your application's authorized settings:

```go
cfg := doubao.Config{
    ASREndpoint: asrEndpoint,
    TTSEndpoint: legacyTTSEndpoint, // Path must be /api/v1/tts/ws_binary.
    APIKeyV2: asrAPIKey,           // ASR only; alternatively AppID/AccessToken.
    AppID: appID,
    AccessToken: accessToken,     // Required for legacy TTS even with APIKeyV2.
    ResourceID: asrResourceID,    // Explicit ASR resource; ASRCluster is fallback.
    TTSCluster: ttsCluster,
    ASRModel: "bigmodel",
    ConnectTimeout: 10 * time.Second,
}
```

No arbitrary header overrides or resource-ID substitutions are performed. The
caller owns endpoint selection and authentication. TTS v3 bidirectional
endpoints and API-key-only TTS authentication are unsupported in this phase.

The [compile-checked example](example/turn.go) demonstrates the entire turn:

```go
ctx, cancel := context.WithCancel(parent)
defer cancel()
err := example.Turn(ctx,
    doubao.ASRProvider{Config: cfg},
    doubao.TTSProvider{Config: cfg},
    pcm16kMono, sessionID, turnID, voice,
    agentReply,   // Your Agent/model produces a reply from the transcript.
    playPCM24k,  // Your audio transport consumes each chunk immediately.
)
```

`Turn` writes ASR input concurrently while draining transcripts, explicitly calls
`EndUtterance`, waits for provider completion, invokes the caller's Agent/model,
then streams TTS chunks to caller-owned playback. This educational turn policy
collects only nonempty definite segments in first-seen order, replacing repeated
segment-ID or start-time revisions and deduplicating exact text when identity is
absent. Partial-only input is rejected before invoking the Agent. Cancellation
is checked before the Agent and before successful return. Applications choose
their own aggregation and turn policy; this example is not a core orchestration
API. Callback implementations and all conversation state remain in the
application. Both callbacks must honor their context.

## Formats and lifecycle

- ASR accepts only `Encoding: "pcm_s16le"`, `SampleRate: 16000`, mono. Session ID
  is required. `WriteAudio` rejects odd byte counts and splits input into at most
  12800-byte sample-aligned wire chunks. Locale and hotwords are request-scoped.
- `TranscriptEvent.Final` means a definite utterance. It does **not** mean the
  provider has finished: drain `Events()` and `Errors()` until both close. Only
  the provider's final binary frame completes recognition cleanly. A socket
  close, including a normal WebSocket close, before that frame is an error.
- `EndUtterance(ctx)` explicitly sends end of input once. `Close()` never sends
  it. ASR input cannot resume after completion; open a new stream for new input.
- TTS requires session ID, turn ID, voice, valid UTF-8 nonblank text (at most
  10000 bytes), `Encoding: "pcm_s16le"` and `SampleRate: 24000`. It submits the
  whole text once and exposes raw PCM16 mono chunks before the final frame.
  It does not collect MP3, accept incremental text or infer a voice.
- ASR and TTS use different sample rates. The caller owns channel conversion,
  decoding and resampling at input/output boundaries. The library cannot infer
  the true sample rate or channel count of raw PCM bytes.
- Consume data and error channels concurrently. Channels are bounded (16 ASR
  events, 8 audio chunks, 1 terminal error); slow consumers exert backpressure.
  Streams have no implicit duration timeout. Supply a deadline as needed.
- Cancel the opening context or call idempotent `Close()` to stop immediately.
  Cancellation unblocks socket I/O, queued writes and output backpressure;
  channels close and the socket is closed. Locally canceled streams do not emit
  remote-failure errors. `Close` waits for the receive loop to exit, without a
  WebSocket close handshake. Caller write methods still return their error.
- Setup is bounded by `ConnectTimeout` (zero means 10 seconds), covering both
  connection and the initial request write. Remote errors and dial diagnostics
  omit remote text, credential-bearing URLs and response bodies. Provider error
  codes remain available in error text. Authenticated redirects are rejected.

The parser bounds each message and decompressed payload to 1 MiB and total TTS
output to 8 MiB. Invalid framing/compression, unexpected response types, provider
errors and premature closure terminate the stream explicitly. A TTS final frame
without any audio is an error.

Legacy TTS framing follows the [official WebSocket interface](https://docs.volcengine.com/docs/DoubaoVoice/Websocketinterface?lang=zh)
and [provider-hosted parsing sample](https://developer.volcengine.com/articles/7500048059624554515):
flags0 is an empty header-only ACK (the earlier zero-length-payload ACK spelling
is also accepted); audio flags1 carries a positive signed sequence, while final
flags2 and flags3 carry a negative sequence before the payload length. ASR input
completion flags2 retains its separate size-only framing. Local regressions use
literal ACK/final bytes independent of the encoder.

## Delivered scope and verification

Local tests use `httptest.Server` and WebSocket handshakes over in-memory
`net.Pipe` connections, requiring no loopback port privileges or provider calls.
They cover binary requests and input completion, partial/definite/provider-final
ASR, early TTS streaming, cancellation and backpressure, format/auth validation,
malformed/oversized/compressed payloads and premature EOF. An independent-module
test imports the public packages with a local replacement and `GOPROXY=off`.
The example builds with `go test ./...`; its tests cover definite-segment
aggregation/revisions, partial-only rejection, errors, cancellation and playback.
The independent consumer quotes its replacement path and bounds its subprocess
with a context timeout.

```sh
go test -race ./...
go vet ./...
```

These are local protocol/lifecycle checks, **not live provider acceptance**.
There is no Feishu realtime audio transport, VAD, automatic barge-in, meeting
integration or GTM application migration in this module. Applications must
explicitly wire audio capture, turn policy, Agent execution and playback before
conducting separately authorized live acceptance.
