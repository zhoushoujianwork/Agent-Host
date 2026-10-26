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

## Voice status and next steps

**Realtime meeting audio is not implemented.** The supplied meeting manual's
retrievable text omits its voice setup section. The official CLI and SDK sources
checked for this increment did not supply the referenced realtime endpoint and
audio wire contract. No guessed endpoint or synthetic success stands in for
real audio acceptance.

The next slice needs the target app's configured credential profile and the
official audio connection/protocol documentation. Then implement the audio
transport inside this library, verify a fixed spoken sentence in a test meeting,
and add speech input/replies and interruption. Keep the first test confined to
an explicitly selected meeting.

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
