# Feishu meeting library

Importable Go meeting operations shared by Agent-Host and external applications.
This module has standard-library dependencies only and uses a named `lark-cli`
profile for credentials. It does not require the Agent-Host server, database,
native harness or memory system.

Supported: profile identity check, join, membership, meeting event polling,
text send and explicit leave. **Realtime meeting audio is not implemented.**

## Import from another checkout

Until an authorized source push makes a remote revision available, add the local
module to the consumer (replace `/absolute/path/Agent-Host` with your checkout):

```sh
rtk go mod edit -require=github.com/zhoushoujianwork/Agent-Host/plugins/feishu@v0.0.0
rtk go mod edit -replace=github.com/zhoushoujianwork/Agent-Host/plugins/feishu=/absolute/path/Agent-Host/plugins/feishu
```

```go
import feishu "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"

client, err := feishu.NewMeetingClient(feishu.Config{
    Profile:   "your-profile",
    AppID:     "cli_your_app",
    BotOpenID: "ou_your_bot",
}, nil)
if err != nil {
    return err
}
if err := client.CheckIdentity(ctx); err != nil {
    return err
}
membership, err := client.Membership(ctx, meetingID)
```

`CheckIdentity` verifies the CLI-selected application and local bot credential
availability. It does not prove the configured bot Open ID, remote permissions,
meeting membership or audio access. Use the correct profile for each app.

## Application responsibilities

Authorize the participant and meeting before acting. Persist mutation intent
before `Join`, `SendText` or `Leave`; use a stable persisted UUID for text sends.
Record `MutationResult.Outcome` separately from model execution. An `unknown`
outcome must be reconciled, never silently retried. Confirm membership after
joining before starting a meeting conversation.

`PollEvents` returns events and the proposed cursor, but writes no files. Poll
at intervals of at least five seconds. Decide whether to baseline old events,
filter self-echoes and permitted senders, and durably accept events before
advancing the cursor. `Event.Payload` retains unrecognized platform content;
`ChatItems` decodes the supported chat shape without treating text as commands.

`SendText` sends your text exactly, with no Host branding. `Leave` departs only
the selected meeting. There is no implicit join, leave, replay or background
subscription. Share the owning application's subscription if you later add
pushed events; do not start competing listeners for the same app.

The default transport requires Unix and installed `lark-cli` (the extracted
path was verified against 1.0.96). A custom `Transport` supports tests or another
execution environment and must honor cancellation and the `Output.Started`
contract. Raw stdout/stderr are private evidence and must not be logged publicly.

## Checks and integration status

```sh
rtk go test -race ./...
rtk go vet ./...
```

Tests include an independent consumer module with network module resolution
disabled. Agent-Host consumes this module today. GTMGoPlay integration is a
future consumer change; its Rooms, scenarios, ASR/TTS and PG/COS ownership remain
in that application.

[Scope and next steps](../../docs/plugin-libraries.md) ·
[Protocol and implementation details](../../docs/plugin-libraries-detail.md)
