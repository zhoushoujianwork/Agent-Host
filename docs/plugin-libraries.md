# Reusable plugin libraries

Integration capabilities should be usable by Agent-Host and other applications
through a normal library import. GTMGoPlay is the first intended external
consumer. Applications keep their own conversation logic, permissions and
storage; they do not need to start Agent-Host to use an integration.

## First component: Feishu meetings

The [Feishu library](../plugins/feishu/README.md) is an independent Go module.
It provides joining, membership inspection, explicit leaving, meeting text
delivery, event polling and a local credential-profile identity check.
Agent-Host's existing meeting adapter now uses those same implementations.

GTMGoPlay can import the library when it adds a Feishu meeting entry. Its Rooms
component will continue to own training context, conversation turns, ASR/TTS and
business records. The library supplies the Feishu connection operations.

## Speech component

The [speech library](../plugins/speech/README.md) is an independent Go module
with provider-neutral streaming interfaces and explicit Doubao adapters. It
supports PCM16/16kHz mono ASR and legacy PCM16/24kHz mono TTS with bounded
parsing and cancellation. The application owns the Agent/model response between
recognition and synthesis, audio conversion and turn policy.

Local protocol tests and an independent-consumer fixture cover this first slice.
It has no live provider acceptance, VAD or automatic barge-in. The Feishu
library supplies the audio transport separately. Host's opt-in meeting adapter
owns turn detection, resampling and Agent execution; GTM application integration
remains separate.

## Voice status and next steps

The Feishu library now has a **source-verified, not live-verified** meeting
audio transport: `MeetingClient.OpenAudio` returns an `AudioSession` with
`ReadAudio`, `WriteAudio`, `ClearAudio` and `Close`, using 24 kHz mono s16le PCM
in both directions. Its protocol comes from the user-supplied reference starter,
not from official documentation, and no live meeting session has confirmed it.
Details and limits are in the [implementation notes](plugin-libraries-detail.md#feishu-audio-transport).

Host's [meeting channel](feishu-meeting-channel.md) bridges this transport to
16 kHz ASR, the existing Host Agent and 24 kHz TTS. Voice is opt-in and uses
wake-prefixed, unattributed meeting audio; energy-based turn detection and
half-duplex playback remain application policy.

Live acceptance, robust echo cancellation, interruption and GTMGoPlay integration
remain open. Next, verify the selected profile's app identity and published
realtime permissions, then run a separately authorized meeting test of spoken
input and a spoken reply. The current half-duplex bridge does not implement
barge-in.

## Library ownership

- Provider-specific calls and media protocols belong in `plugins/<name>`.
- Agent-Host admission, durable recovery, routing and memory stay in Host.
- GTMGoPlay scenarios, room records and speech-provider choices stay in GTMGoPlay.
- New public APIs follow actual consumer requirements. Existing unrelated
  plugins can migrate when they have a concrete reuse need.

The source distribution remains in this repository. Local consumers can use a
Go `replace` directive; a remotely resolvable version requires a separately
authorized source push. There is no binary plugin loader or plugin marketplace.

[Implementation, verification and remaining blockers](plugin-libraries-detail.md)
