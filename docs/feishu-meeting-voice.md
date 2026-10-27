# Feishu meeting voice

Optional voice for a bot that is already in a Feishu meeting. Meeting audio is
transcribed by the existing speech ASR, wake-prefixed requests enter the same
durable Host channel queue as text, and the text reply is also spoken through
TTS. There is no realtime speech-to-speech model. Technical detail:
[feishu-meeting-voice-detail.md](feishu-meeting-voice-detail.md).

## Enable

Add a `voice` block to the meeting configuration (see
[the example](../config/channel-feishu-meeting-voice.example.json)) and export
the environment variables it names. Configuration never holds credential values.

- `enabled` and `accept_unattributed_audio` must both be `true`.
- `wake_prefix` defaults to `AI`. Only an utterance that starts with it and has
  a nonempty request is admitted; everything else is discarded.
- `asr`: `endpoint` (`ws://` or `wss://`), `resource_id` or `cluster`, and
  `api_key_env` or both `app_id_env` and `access_token_env`.
- `tts`: `endpoint` (path `/api/v1/tts/ws_binary`), `cluster`, `voice`,
  `app_id_env` and `access_token_env`.

`agent-host feishu-meeting check` validates the block and reads the named
environment variables without any network access, and reports
`voice_supported` as true only when voice is enabled. `probe` stays identity-only
and reports `voice_supported` false. `serve` works with `auto_join` or a fixed
`meeting_id`; it does not work in IM-only mode.
The [channel startup guide](feishu-meeting-channel.md#enable-and-start) gives
the commands and a separate state home for trials.

## Scope and identity

Mixed meeting audio does not identify a speaker, so `allowed_sender_open_ids`
cannot apply to it. `accept_unattributed_audio` is the explicit consent that
anyone audible in the meeting can address the bot. Voice requests use the
sender `meeting-audio` ("Unattributed meeting audio"), the shared meeting
audience, and task text that states the source. They never impersonate the
owner or a human Open ID, and never load a private profile. Text requests keep
the sender allowlist.

The supplied starter requires published `vc:meeting.bot.join:write` and
`vc:meeting.bot.realtime:write`, in addition to the text channel's meeting
scopes. Local checks do not verify platform permissions.

## Behavior

- First release is half duplex: input is ignored while the bot speaks plus a
  short tail. Barge-in is not supported.
- Segmentation is a simple energy VAD: 20 ms frames, about 200 ms minimum
  speech, 600 ms closing silence, 15 s maximum per utterance. Noise or crosstalk
  can split, merge or miss utterances.
- The text reply is always the durable result. Only a reply to a voice request
  that was accepted as text is spoken. Speech failure is reported separately and
  never changes the text result or replays anything.
- Audio attaches after the meeting baseline is ready and ends with the meeting,
  poll failure or shutdown. It does not reconnect; text continues after a voice
  failure until the service restarts.

## Not yet accepted

No live acceptance has run: Feishu realtime audio, Doubao ASR/TTS, playback
quality, echo behavior, and multi-speaker use are unmeasured. Treat first use
as a supervised trial.
