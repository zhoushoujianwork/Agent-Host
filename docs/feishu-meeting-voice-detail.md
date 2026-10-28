# Feishu meeting voice detail

Overview and setup: [feishu-meeting-voice.md](feishu-meeting-voice.md).

## Pipeline

1. `FeishuAudioOpener` calls `MeetingClient.OpenAudio` (one attempt, no
   reconnect) and pumps frames per track.
2. Each track is resampled from 24 kHz to 16 kHz PCM (state is kept across
   chunk and odd-byte boundaries) and segmented by the energy VAD. Limits: 8
   tracks, bounded frame size, 4 queued utterances (extras are dropped and
   reported).
3. Only a completed utterance is sent to ASR. Writing, the explicit end and
   draining run concurrently. Final segments are kept and later revisions of
   the same segment replace earlier ones.
4. A transcript that starts with the wake prefix becomes a `channel.Message`
   through `Service.ReceiveMessage`: sender `meeting-audio`, name `Unattributed
   meeting audio`, conversation type `2`, reply target kind `feishu-meeting`,
   text prefixed with a source marker. IDs are `voice-<random session>-<sequence>`.
5. The reply UUID Host assigns to that message is registered (at most 64
   pending, 30 minute expiry). `voiceReplier` wraps the final text replier and,
   after an accepted text reply of a registered UUID, queues speech (8 queue
   entries, 2000 bytes of text). TTS output is paced near realtime and capped.

ASR has a 30-second deadline, at most 32 final segments and 16 KiB of final
text per utterance. Synthesis and playback have a three-minute deadline and
two-minute audio cap per reply. Playback stays at most 200 ms ahead of the
clock, including after provider stalls, with a 500 ms input holdoff after its
scheduled end. Cancellation clears queued playback within two seconds before
closing the audio session. Partial or empty completed PCM is an error.

## Lifetime

- Automatic mode: the entry manager hooks start polling, wait for its history
  baseline, then start voice with a context derived from the poll lifetime. A
  voice startup failure stops polling for that meeting. Stop removes both.
- Fixed mode: voice starts when the receiver first reports its baseline, and
  readiness waits for it. Voice is closed when the receiver, worker or service
  exits. No event subscriber is added.
- A bridge whose audio ended stays registered as evidence until its meeting
  stops. It is never restarted and nothing is replayed after a restart.

## Configuration schema

`voice.enabled`, `voice.accept_unattributed_audio`, `voice.wake_prefix`,
`voice.credentials_file`,
`voice.asr.{endpoint,cluster,resource_id,api_key_env,app_id_env,access_token_env}`,
`voice.tts.{endpoint,cluster,voice,app_id_env,access_token_env}`. Unknown fields
and null values are rejected. Environment names must be identifiers; a value that
looks like a secret is rejected as a name.

## Credentials file handling

`loadFeishuMeetingConfig` makes a relative `credentials_file` absolute against
the directory of the original configuration file, for enabled voice only.
`feishuVoiceProviders` then validates the voice block and, if the field is set,
reads the file with `readServiceJSON` (owner, mode, regular file, no symlink,
32 KiB, duplicate-key and trailing-data checks). Each value must be a JSON
string that is nonempty, single-line and untrimmed, and each key must be one of
`VoiceConfig.ReferencedEnvNames()`. The resulting in-memory map is the only
`lookup` passed to `ResolveCredentials`; the process environment is neither
read nor written. Absent field: `os.LookupEnv` as before. Disabled voice returns
before any read.

## Tests

Fake audio, ASR and TTS cover wake gating, source identity, final revisions,
reply correlation, failure separation, fixed and automatic lifetimes, startup
failure and poll failure. Credentials-file tests cover relative resolution across working directories,
exclusive use without environment fallback, malformed content, unsafe
permissions and symlinks, error redaction and disabled voice. They need no network.

## Runtime activation probe (2026-10-28)

The bot's membership in the invited meeting was confirmed. A real request for
`/open-apis/vc/v1/realtime/endpoint` was rejected with CLI exit 3,
`authorization.app_scope_not_applied`, code `99991672`, naming the missing
`vc:meeting.bot.realtime:write` scope. The app must apply that permission and
publish the version before audio setup can proceed. No audio session was
created and no PCM frames were received.

The supplied starter contains placeholder credentials, and the selected service
has no configured ASR/TTS credentials. A private empty credential template is
prepared locally; it is deliberately not treated as a usable credential source.
Real speech-provider calls and spoken-input/output acceptance remain pending.
