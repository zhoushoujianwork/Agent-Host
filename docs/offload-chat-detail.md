# Offload Chat implementation details

[User-facing design](offload-chat.md).

## Workspace configuration

The existing workspace `config/web-chat.json` accepts a private owner-only file:

```json
{
  "binary": "/absolute/agent-host",
  "cwd": "/absolute/project",
  "args": [
    "--home", "/absolute/runtime/cli-state",
    "--workspace", "/absolute/project",
    "--agent", "default",
    "--offload-config", "/absolute/config/offload.json"
  ]
}
```

Use the existing private [Offload profile](offload-channel-plugins-detail.md).
Offload owns native credentials; no legacy Agent catalog/native profile is
required. Optional `environment_file` may contain only `TYPESAFE_API_KEY` and/or
`JEV_API_KEY` for memory classification. Disabled and retrieval-only memory need
no environment file. The launcher clears inherited native and Jev provider
variables before adding explicitly selected classifier keys. Unsupported,
duplicate or task/session arguments in saved destinations fail validation.
The command, provider selection and profile digest participate in launch identity.
A changed destination requires a new conversation, without automatic replay.

CLI one-shot and explicit `--resume HOST_SESSION_ID` remain available. Web uses
`--room`, `--request-id` and `--received-at`. It does not add legacy renewal or
notification flags to Offload commands. Agent settings show Offload execution
without claiming legacy tools or datasource mounts.

## Room ownership and native continuity

`RunRoom` owns an OS room lease across admission, execution and closeout, plus
the existing session lease for active-run recovery. Independent rooms retain
concurrency. Busy rooms fail before accepting another request. Source intake is
owner-local; no channel message is fabricated. State records the same immutable
room/request receipt used by the workspace.

Offload provides resume, not native forks. Its explicit state admission selects
only the latest receipt as a possible parent, provided it completed and sealed
before the new input arrived and matches the runtime. Other history remains
bounded attributed source data. An overlapping/unfinished latest receipt prevents
resuming older, subsequently mutated native history. It starts fresh instead.
Legacy ACP room selection is unchanged.

A completed linear child may bind the parent's native ID only within the same
Offload room/runtime and owner partition. Binding verifies the latest parent,
its seal and all existing owners of that ID. The generic
`fork_from_native_session_id` field holds the resume source; the explicit
`offload_serial_continuation` policy identifies linear use, not a fork.
Direct CLI/channel entries cannot adopt room native sessions.

Duplicate IDs with identical input return their original receipt. Only sealed
terminal runs replay public events; raw Offload records and private receipts are
excluded. Replayed closeout projects the latest saved run state, including a
separate memory-only retry, without changing original event evidence.
Failed execution or memory retains its error and does not rerun either
component. Accepted requests with no run, running/unknown outcomes and changed
content/runtime are refused. Offline memory-only retry remains explicit.

## Memory, output and validation

Owner-local room inputs use the existing CLI exact-input memory convention.
Historical text never becomes a new assertion. `execution.ready` exposes the
saved answer before optional closeout. `run.finished` projects execution and
memory outcomes independently. Raw evidence remains in the execution record.

Headless tests include a real Go CLI behind the Python Web bridge with a fake
native executable: new room, continuation, accepted retry, persisted reload and
owned-process cancellation. Host/state fixtures cover concurrent rooms, busy
rooms, overlapping intake, unfinished receipts, memory failure, provenance and
binding checks. Existing ACP/channel behavior and launcher validation retain
regressions. Automated fixtures use no real native provider, desktop or production activation.
The [isolated live canary](offload-canary-acceptance-2026-10-30.md) separately
records bounded real execution, continuation and memory evidence.
