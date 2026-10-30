# Offload execution and channel plugins

Give Agent-Host a lighter execution path: Offload runs the local Agent, while
enabled channel plugins receive messages and deliver results. Reuse Offload's
profiles, native sessions and execution monitoring. Agent-Host keeps channel
identity, conversation bindings and attributed evidence.

The first slice supports DingTalk and Feishu through their existing channel
components. Select an Offload execution configuration for a channel profile;
enable only the channels needed. Existing profiles keep their current behavior.
Development and acceptance use isolated runtime directories, without switching
the installed services.

Start with a private Offload profile file and pass `--offload-config PATH` to
either channel's `check` and `serve` command. Managed channel profiles use
`offload_config`; `service run --no-workbench` starts channels without the Web
workbench. See the [configuration examples](offload-channel-plugins-detail.md#configuration-and-compatibility).

The same host also supports [Web/Mac rooms and one-shot local Chat](offload-chat.md).
Web rooms preserve request receipts and serialize native continuation per room.

Memory is optional and disabled by default. The [memory plugin](offload-memory-plugin.md)
adds local retrieval and optional source-grounded user-statement processing.
Only processing requires the explicitly configured Jev classifier; no hidden
Claude extractor or extra Offload worker is started. Missing execution controls
still fail explicitly.

Offload owns model execution and credentials. The local 0.3.0 baseline also
provides native conversation history, statistics and additional directories;
Agent-Host does not rebuild those features. This slice uses execution,
continuation and final JSON results. Richer per-run tool controls, Host MCP
injection and token streaming are outside this first slice. Existing ACP
sessions are retained, never relabelled as Offload sessions or replayed.

Use one explicitly selected workspace per channel execution profile. Agent
catalogs, semantic project routing, attachments and meeting execution remain on
the existing path until their Offload contracts are implemented. Selecting an
unsupported combination gives an explicit configuration or input error.

Implementation order: build the shared Offload client, connect both channel
components to a slim host, verify isolated end-to-end delivery and continuation,
then open a PR. Each invocation now supplies `agh:<run-id>` through Offload's
`--caller-run-id` for per-turn correlation with private execution evidence.
Caller-category attribution is tracked in
[Offload #31](https://github.com/zhoushoujianwork/offload/issues/31); caller-based
statistics and monitor views follow in
[Offload #32](https://github.com/zhoushoujianwork/offload/issues/32) after their
implementation baseline is published.

[Implementation and acceptance details](offload-channel-plugins-detail.md).
