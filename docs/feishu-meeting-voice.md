# Feishu meeting voice

Optional voice for a bot that is already in a Feishu meeting. Meeting audio is
transcribed by the existing speech ASR, wake-prefixed requests enter the same
durable Host channel queue as text, and the text reply is also spoken through
TTS. There is no realtime speech-to-speech model. Technical detail:
[feishu-meeting-voice-detail.md](feishu-meeting-voice-detail.md).

## Enable

Add a `voice` block to the meeting configuration (see
[the example](../config/channel-feishu-meeting-voice.example.json)) and supply
the credentials it names, either by exporting environment variables or through
a private credentials file (below). The channel configuration holds references;
secret values stay in the selected credential source.

- `enabled` and `accept_unattributed_audio` must both be `true`.
- `wake_prefix` defaults to `AI`. Only an utterance that starts with it and has
  a nonempty request is admitted; everything else is discarded.
- `asr`: `endpoint` (`ws://` or `wss://`), `resource_id` or `cluster`, and
  `api_key_env` or both `app_id_env` and `access_token_env`.
- `tts`: `endpoint` (path `/api/v1/tts/ws_binary`), `cluster`, `voice`,
  `app_id_env` and `access_token_env`.

- `credentials_file` (optional): see [Credentials file](#credentials-file).

`agent-host feishu-meeting check` validates the block and reads the named
credentials without any network access, and reports
`voice_supported` as true only when voice is enabled. `probe` stays identity-only
and reports `voice_supported` false. `serve` works with `auto_join` or a fixed
`meeting_id`; it does not work in IM-only mode.
The [channel startup guide](feishu-meeting-channel.md#enable-and-start) gives
the commands and a separate state home for trials.

## Credentials file

The managed service environment file only accepts provider keys, so a managed
service keeps speech credentials in a separate private file. Set
`voice.credentials_file` to its path. A relative path is resolved against the
directory of the Feishu configuration file, not the working directory, so
standalone and managed runs agree.

The file is a JSON object that maps the same environment names referenced by
`asr.api_key_env`, `asr.app_id_env`, `asr.access_token_env`, `tts.app_id_env`
and `tts.access_token_env` to secret strings. The values below are placeholders:

```json
{
  "AGENT_HOST_VOICE_ASR_API_KEY": "PLACEHOLDER_ASR_API_KEY",
  "AGENT_HOST_VOICE_TTS_APP_ID": "PLACEHOLDER_TTS_APP_ID",
  "AGENT_HOST_VOICE_TTS_ACCESS_TOKEN": "PLACEHOLDER_TTS_ACCESS_TOKEN"
}
```

- Create it with mode `0600`, owned by the service user, as a regular file (no
  symlink), at most 32 KiB. Group or other access is rejected.
- When `credentials_file` is set, the file is the only credential source. The
  inherited environment is never used as a fallback, and a missing, unreadable
  or invalid file is an error.
- Every referenced name must be present. Keys the voice block does not
  reference, duplicate keys, `null`, non-string values, empty values and
  values with a newline or surrounding whitespace are rejected. Errors name
  missing environment names but never print values or unknown keys.
- Values are held in memory only. They are not exported to the process
  environment, the model or workbench environment, or logs.
- With voice disabled or no `voice` block, the file is not read and need not
  exist. Without `credentials_file`, the named environment variables are used
  as before.

## Scope and identity

Mixed meeting audio does not identify a speaker, so `allowed_sender_open_ids`
cannot apply to it. `accept_unattributed_audio` is the explicit consent that
anyone audible in the meeting can address the bot. Voice requests use the
sender `meeting-audio` ("Unattributed meeting audio"), the shared meeting
audience, and task text that states the source. They never impersonate the
owner or a human Open ID, and never load a private profile. Text requests keep
the sender allowlist.

The supplied starter requires published `vc:meeting.bot.join:write` and
`vc:meeting.bot.realtime:write` (the realtime scope is mandatory for audio), in
addition to the text channel's meeting scopes. Local checks do not verify platform permissions.

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
