# Project-local runtime

Run all enabled channel services from the project with `make run`. Configuration,
credentials, state, logs and native profiles live under the ignored
`.agent-host/` directory. The Go CLI scans `.agent-host/channels/channel-*.json` and
starts every enabled channel together. Feishu and DingTalk can run concurrently.
There is no system startup registration or login-time restart.
See the [details](service-install-detail.md#project-local-runtime) for layout,
configuration, migration evidence and the retained legacy deployment history.

## Start and observe

```sh
make run
```

The command builds `bin/agent-host` and invokes its Go CLI directly. The CLI
checks all enabled configurations, prepares workbench files, and supervises the
channel workers in the foreground. Each channel keeps its own state, logs and model
environment. One shared workbench displays all enabled channels. Logs appear in the terminal and each runtime's
`logs/service.ndjson`. Ctrl-C stops all workers and the shared workbench.
Running `make run` again picks up configuration and code changes.

The shared workbench stays at `http://127.0.0.1:8765/` across restarts.
It combines enabled channels' execution records and memory with clear source
labels. The [memory workbench](../tools/run-viewer/README.md#memory-workspace)
also lets the owner publish or reject saved experience drafts and disable
memory while its channel Host is running. To choose another fixed port, use
`make run RUN_ARGS='--viewer-port 9000'`. If occupied, the workbench reports
failure instead of switching ports; the channels still start.

To avoid opening a browser:

```sh
make run RUN_ARGS=--no-browser
```

To run only one explicit runtime for diagnosis:

```sh
make run RUNTIME_ROOT="$PWD/.agent-host/dingtalk"
```

Stop its current worker before starting the same runtime again. These commands
do not install a system service or replay old tasks.

## Configure

Create one private registration file per channel, for example
`.agent-host/channels/channel-feishu.json`:

```json
{
  "enabled": true,
  "runtime_root": "../runtime"
}
```

For `.agent-host/channels/channel-dingtalk.json`, use `"runtime_root": "../dingtalk"`.
Set `enabled` to `false` to disable a channel on the next startup. Runtime paths
are relative to the registration directory or absolute. Only present, enabled
registrations start workers; each must reference a complete runtime. Protect
the directory with mode `700` and files with mode `600`. The example is
[channel-registration.example.json](../config/channel-registration.example.json).

An absent channel directory preserves the older single-runtime setup. An
existing directory with no enabled channels fails explicitly. Invalid enabled
runtime settings prevent startup before any channel connects. Duplicate runtime
paths and running instances are rejected. If a worker exits unexpectedly, the
CLI reports failure and stops its peers; it does not silently restart tasks.

Configuration names distinguish their purpose: `channel-*` for channels,
`data-*` for data sources, `component-*` for Host/Agent settings, and
`credential-*` for authentication material. See the [naming guide](../config/README.md).

Edit each runtime's `config/component-service.json` for the channel, model, provider,
tool policy and optional source configurations. Provider values belong in
`config/component-environment.json`; channel identity belongs in `config/channel-feishu.json` or
`config/channel-dingtalk.json`. Agent configuration uses `component-agents.json`;
knowledge and discovery use `data-knowledge.json` and `data-discovery.json`.
Keep credentials and populated configuration private
and uncommitted. New setups can use the tracked `config/*.example.json` files.
A missing configuration fails explicitly rather than creating a fake setup.

The configured email registry is `.agent-host/email/data-email-accounts.json`. AI discovers
its enabled accounts through one MCP and chooses the account for each operation.
Email requires `no_tools: false`; group tasks do not mount personal email.
See the [email guide](email-plugin.md).

Ordinary local CLI chat defaults to `.agent-host/cli-state` under its invocation
directory. To inspect the channel runtime, specify its state explicitly:

```sh
./bin/agent-host feishu status --home "$PWD/.agent-host/runtime/state"
./bin/agent-host run show --home "$PWD/.agent-host/runtime/state" --workspace "$PWD/.agent-host/runtime/workspace" RUN_ID
```

Directory access remains governed by native settings; project routing and source
selection retain their existing boundaries. A runtime directory is not an OS
sandbox. Current configuration changes require restarting `make run`.
