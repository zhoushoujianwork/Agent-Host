# Project-local runtime

Run the channel service from the project with `make run`. Configuration,
credentials, state, logs and native profiles live under the ignored
`.agent-host/` directory. The default local runtime currently serves Feishu.
There is no system startup registration or login-time restart.
See the [details](service-install-detail.md#project-local-runtime) for layout,
configuration, migration evidence and the retained legacy deployment history.

## Start and observe

```sh
make run
```

The command builds `bin/agent-host`, checks local configuration, prepares the
workbench files and starts the service in the foreground. Logs appear in the
terminal and append to `.agent-host/runtime/logs/service.ndjson`. The printed
workbench URL shows saved tasks, tools and memory. Ctrl-C stops the service and
workbench. Running `make run` again picks up configuration and code changes.

To avoid opening a browser:

```sh
make run RUN_ARGS=--no-browser
```

The migrated DingTalk runtime is available separately:

```sh
make run RUNTIME_ROOT="$PWD/.agent-host/dingtalk"
```

Stop the current runtime before switching. These commands do not install a
system service. They do not replay old tasks.

## Configure

Edit `.agent-host/runtime/config/service.json` for the channel, model, provider,
tool policy and optional source configurations. Provider values belong in
`config/environment.json`; channel identity belongs in `config/feishu.json` or
`config/dingtalk.json`. Keep credentials and populated configuration private
and uncommitted. New setups can use the tracked `config/*.example.json` files.
A missing configuration fails explicitly rather than creating a fake setup.

The configured email registry is `.agent-host/email/accounts.json`. AI discovers
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
