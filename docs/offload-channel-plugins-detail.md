# Offload channel implementation details

[User-facing design](offload-channel-plugins.md). Tracking:
[Agent-Host #43](https://github.com/zhoushoujianwork/Agent-Host/issues/43).

## Boundaries

- `internal/offloadexec`: subprocess-only client for the verified basic Offload
  CLI, with prompt on stdin, explicit command/profile/workspace, bounded JSON
  results and cancellation of only the process group it created.
- A slim channel host implements the existing `channel.Host` interface. Both
  existing transports call it through one factory. It does not construct the
  legacy Host or require Jev when memory is disabled.
- Existing component envelopes provide explicit channel enablement and lifecycle.
  New transports register their adapter without changing the execution client.
  No dynamic loader or plugin marketplace is introduced.

## Configuration and compatibility

An explicit Offload selector is separate from `execution_config` (ACP); selecting
both fails validation. Offload owns provider credentials. Unknown capabilities
are not emulated using a prompt and are never silently dropped. Profile aliases
may resolve on a fresh invocation; continuation retains the returned canonical
profile, native session, workspace and runtime fingerprint.

The channel CLI accepts `--offload-config /absolute/offload.json`; managed
execution profiles use `offload_config`. The private file uses this shape:

```json
{
  "version": 1,
  "command": "/absolute/path/to/offload",
  "profile": "your-offload-profile",
  "timeout": "30m",
  "memory": {"enabled": false}
}
```

Optional `model` overrides the Offload profile model. The configuration must be
a regular owner-private file, normally mode `0600`; the command must be an
absolute executable path. No credential is copied from Offload into this file.
Use isolated channel state and a new configuration during evaluation. For a
channel-only background service, `service run --no-workbench` avoids starting
the Python workbench or opening a browser. Existing channel component
`enabled` settings still determine which transports start.

In `host.json`, select a new execution profile without copying legacy execution
or environment settings:

```json
{
  "version": 1,
  "default_profile": "light",
  "profiles": {
    "light": {"execution": {"offload_config": "/absolute/offload.json"}}
  }
}
```

Existing `channels.dingtalk.json` and `channels.feishu.json` envelopes can select
that profile using `execution_profile`. DingTalk enables its `bot` feature and
disables `dws`; Feishu enables IM without meeting entry. Enabled Host datasource
plugins, Agent catalogs and tool restrictions are rejected for this profile.
Other execution profiles retain their existing capabilities. Keep the Offload
profile outside the component registry, or under its private `credentials/`
directory; arbitrary extra JSON files are not component declarations.

Owner-private messages and authenticated group mentions resolve separate
audience hosts through the existing channel resolver. Groups keep their own
conversation and native-session bindings. This slice does not enable additional
private users or change channel admission policy. Ordinary channel task execution
passes Offload's write intent, matching the current owner-equivalent Beta policy;
that intent is an instruction, not a sandbox or permission grant.

The installed basic CLI has no caller tag yet. AGH source/channel attribution
remains in its channel receipts; the Offload record must not be relabelled as
though upstream caller support already existed. Wire the optional caller flag
after its supported contract in Offload #31 is available. Caller aggregation and
monitor views are separately tracked in Offload #32. Do not pass unsupported
flags to older binaries or confuse the caller label with request correlation.

## State and outcomes

Keep execution, delivery, acceptance and memory outcomes independent. A successful
Offload process is not invented business acceptance. Memory-disabled runs carry
an explicit policy marker and an audited skipped outcome, so a completed turn
can continue without pending memory work.

A fresh AGH session uses a clearly pending native binding. Once a valid Offload
result supplies a native session, bind it transactionally to the owned session
and run; never reinterpret an existing ACP identifier. Preserve the actual
Offload result and canonical profile as execution evidence. Configuration changes
must not continue an incompatible session or claim old queued messages.

Cancellation reaps only the owned invocation and records the observed failure or
cancellation. No automatic retry, salvage or replay is introduced. The current
Host-private run-control socket and richer live controls are not claimed by this
first slice.

## Acceptance

Use fake Offload executables and local transport fixtures: success, failure,
malformed/additive JSON, unavailable command/profile, bounded output, continuation,
canonical profile binding, conversation isolation, cancellation, both channel
delivery paths and disabled channel lifecycle. Memory-disabled execution must
work without Jev; unsupported memory, MCP, restricted tools or media must fail
before native execution. Preserve the legacy path in regression tests.

Run repository checks and create local commits before the feature-branch PR.
Real account messages, production switching and desktop changes are separate
operator actions, not part of these headless checks.
