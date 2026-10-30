# Offload Chat implementation details

[User-facing design](offload-chat.md).

## Configuration and usage

Use the existing private [Offload profile](offload-channel-plugins-detail.md).
No new execution configuration schema or plugin loader is introduced.

```sh
agent-host chat --offload-config /absolute/offload.json \
  --home /absolute/cli-state --workspace /absolute/project \
  --format json -m 'Check the current task'
agent-host chat --offload-config /absolute/offload.json \
  --home /absolute/cli-state --workspace /absolute/project \
  --resume HOST_SESSION_ID -m 'Continue the task'
```

Use the Host session ID from `run.started`, not the native Offload session ID.
The existing `--memory-scope` can select a durable scope. Memory disabled and
retrieval-only modes require no Jev key; processing uses the profile's selected
provider and existing credential variables. Per-turn caller IDs remain
`agh:<run UUID>`. Native capabilities and credentials remain owned by Offload.
Changing an incompatible runtime requires an explicit fresh session; no native
history conversion, fallback or automatic replay occurs.

## Shared execution and evidence

`offloadchannel.Host.Run` and `RunChannel` share one execution/recall/closeout
implementation. Local Chat uses `Store.CreateRun`; channels continue using
`CreateChannelRun`. Entry provenance cannot change when resuming a native
session. Room sessions remain rejected. Local Chat is owner-partition only.

Source-less owner runs use the existing CLI user-statement convention: exact
input and source run ID, no fabricated channel sender. Classifier eligibility,
duplicate/conflict handling and transactional publication remain unchanged.
Non-owner or ambiguous/quoted channel sources retain their previous exclusions.

The existing chat-state lock permits independent conversations; the existing
per-session lease prevents concurrent execution of one native session. Caller
process cancellation and per-turn deadlines remain in force. Offline memory
retry is the existing command and never invokes native work.

`execution.ready` carries the durably completed answer for text and NDJSON
renderers before optional processing. Raw Offload results remain private event
evidence. Execution, acceptance and memory outcomes remain separate.

## Acceptance and next slice

Headless fixtures cover one-shot/resume, exact argv correlation, original-session
preservation, text/NDJSON visibility, local memory publication and fresh-session
recall, state/session locks and refusal of unsupported flags/provenance. Existing
channel tests exercise the same host. No real native worker or account is used.

Web still sends room/request IDs. Connecting that entry requires its actual room
contract, stable request deduplication, completed-response replay from evidence,
fork compatibility and cancellation tests. This step does not remove those
requirements by pretending Web messages are channel intake.
