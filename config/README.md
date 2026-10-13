# Configuration names

The proposed [unified configuration and plugin startup](../docs/configuration-layout.md)
uses `.agent-host/config/channels.<name>.json` and `plugins.<name>.json` with
explicit enablement. It is not implemented yet; the names and loading rules below
describe the current runtime.

Host configuration filenames identify the component that owns them. Keep
populated files and credentials under the ignored `.agent-host/` directory.
Use the examples in this directory as templates, removing `.example` locally.

| Prefix | Purpose | Examples |
| --- | --- | --- |
| `channel-` | Channel registration or channel protocol settings | `channel-registration.json`, `channel-feishu.json`, `channel-dingtalk.json` |
| `data-` | Data-source access and account configuration | `data-knowledge.json`, `data-discovery.json`, `data-email-accounts.json` |
| `component-` | Host execution, provider environment, or Agent configuration | `component-service.json`, `component-environment.json`, `component-agents.json` |
| `credential-` | Private authentication material referenced by configuration | `credential-dingtalk.secret`, `credential-dokki.json` |

Channel registrations live in `.agent-host/channels/channel-NAME.json` and
contain `enabled` and `runtime_root`. Protocol settings live inside the selected
runtime's `config/channel-NAME.json`; they contain platform-specific fields.
Their directory determines their role. Data and component configurations cannot
be loaded as channel registrations. Unclassified registration filenames fail.

The service loader accepts former `service.json`, `environment.json` and channel
filenames for existing single-runtime installations. If a canonical file and its
former filename both exist, startup fails explicitly. New installations use the
names above. Native tools' conventional filenames such as `CLAUDE.md` stay intact;
they are not Host JSON configuration files.

See the [runtime guide](../docs/service-install.md) for startup and configuration.
## Built-in DingTalk workspace

`data-dingtalk-workspace.example.json` selects the exact owner profile, private
state root and business products. Save a private copy beside the bot JSON as
`data-dingtalk-workspace.json`, or set the bot's absolute `workspace_config` path.
Authorize through `agent-host dingtalk-workspace login --config PATH`.
For selected-source discovery, replace `dws_executable` with
`dingtalk_workspace_config` pointing to that workspace file; keep existing
source selectors and limits. See [the guide](../docs/dingtalk-workspace.md).
