# Production service installation

Use `make install` to build, install and start the DingTalk private/group-mention service on
macOS. It checks the production configuration before replacing a running
Agent-Host instance, and reports success only after a fresh Stream connection
and a running process are observed. See the [details](service-install-detail.md)
for configuration, lifecycle and validation.

## Separate production from development

The production runtime lives in `~/.agent-host/production`: its installed binary,
configuration, credentials, SQLite state, logs, working directory and native
Claude profile are separate. Development chat keeps its existing default
`~/.agent-host` state; automated tests use temporary directories or an explicit
test `--home`. Installation does not import old development memories, sessions,
or test fixtures.

Agent-Host can also keep its response instructions separate from the owner's
personal Claude preferences. Set the agent's `instructions_file` to a private
`config/CLAUDE.md` and exclude the personal Claude directory in production's
native settings. The personal English-learning instructions stay available for
terminal use; the service follows its own role and the current user's language,
adding language coaching only when requested. See the
[response-instruction configuration](service-install-detail.md#response-instruction-configuration)
for the two settings and their boundaries.

The default robot workspace is `~/.agent-host/production/workspace`. Its
existing memory scope remains available. Locally registered projects can be
identified from DingTalk requests and execute in their own real directories; their
knowledge scopes remain stable when those directories change. Native Claude sessions and settings
live in `~/.agent-host/production/native`. This directory separation is not an
operating-system sandbox; native tool permissions still apply.

Directory access is a shared native policy, separate from project routing. The
production profile can add explicitly selected directories to Claude's native
allowlist through `permissions.additionalDirectories` in `native/settings.json`,
with paths resolved to absolute directories.
Project routing still selects the cwd, project context and memory scopes;
accessible folders are not automatically registered projects. Native command/edit
checks and OS access restrictions still apply. See the
[configuration details](service-install-detail.md#directory-access-policy).

## Install and use

Prepare the private configuration once using the
[service](../config/service.example.json),
[environment](../config/environment.example.json), and
[DingTalk](../config/dingtalk.example.json) examples. Put them in
`~/.agent-host/production/config`, with the bot secret in `dingtalk.secret`.
For group-specific roles, add an absolute `agents_config` reference to
`service.json` as described in the [instance guide](agent-instances.md).
Unbound groups mentioning the bot use the default instance. Changes require
restart; `service check` validates references without creating profiles.
Optional `max_concurrent_conversations` in `service.json` sets the active-turn
limit from 1–32 (default four). Conversations and shared agent/audience memory
partitions stay ordered; different audiences may run concurrently. Shared
checkouts can still have conflicting edits. See the
[conversation guide](channel-conversations.md) for input and concurrency scope.

Optional `knowledge_config` supplies selected remote or local Markdown tools.
Configure a source for both its registered project and `default` when ordinary
default-workspace questions need to discover it. Private retained notes should
declare `audiences: ["owner"]`; the Agent source list can further narrow access.
Source roots do not become execution directories. See
[local knowledge](local-knowledge.md) for bounded migration and source evidence.

DingTalk history search/read is enabled automatically for eligible tasks.
Optional `discovery_config` supplies a private file with selected DWS conversations,
a fixed profile and registered project mappings; omission disables collection.
It runs independently and supports local topic review/export. See the
[history/discovery guide](conversation-history.md) before enabling it.
The current binary uses schema 13; keep a private pre-upgrade SQLite backup.
An older binary rollback also needs its matching database backup.

Directories must be owner-only (`0700`); credential/configuration files must be
owner-only (`0600`). Never commit populated credentials.

Then run from the checkout:

```sh
make install
```

The installed program runs independently of the checkout under the current
user's macOS LaunchAgent. It starts at login. Rerun `make install` to explicitly
upgrade or restart it. Temporary DingTalk connection loss reconnects while
running tasks continue. Unexpected process failures restart with a 30-second
launchd throttle; errors remain in the local log. Interrupted native work is
never automatically replayed.

Each service start also launches the read-only workbench and opens its execution
page in your system's default browser. Inspect the execution chain, tool evidence
and memory decisions there; **记忆工作台** opens the saved-memory view. The workbench
reads this production runtime, refreshes every five seconds and stops with the
service. Its local URL is printed in `logs/service.stderr.log`. For manual
headless startup, add `--no-browser` to `agent-host service run --root ...`.

Choose the native model and authentication available to your account in private
service/environment configuration. A gateway is optional; use only your own
credentials. Claude uses its native output budget and same-provider/model
recovery. See the
[native recovery settings](service-install-detail.md#layout-and-configuration).

Send the robot a private message or mention it in an enterprise group. Supported
inputs include text, pictures, image-with-text messages, forwarded Markdown,
files and callback-provided quotes. Attachments
are stored privately under `~/.agent-host/production/state/attachments`; replies
remain text/Markdown. No original quoted history or media is fetched automatically.
Reliable project associations route automatically; all other valid requests
execute in the configured agent's default workspace. An empty catalog skips
project classification and needs no setup. The native assistant asks for
business details when needed; the Host creates no project waiting task.
Older pending requests remain recorded without replay and only their original
requester can explicitly confirm or cancel them. See the
[routing guide](smart-project-routing.md) for compatibility and access boundaries.
`/new` resets the current compatible native conversation and keeps memory;
`/status` shows a safe conversation summary; `/help` lists these three Host
controls. Project and agent settings remain local, and native interactive
commands are not forwarded. These standalone controls use the same conversation
queue; commands in attached or quoted material do not control the Host.
Failed or interrupted sessions recover automatically on the next message;
the robot briefly reports the fresh session and retains previous evidence.
Read progress in `~/.agent-host/production/logs/service.ndjson`. DingTalk gets
the final answer and progress reactions on the original question; execution,
memory, reply delivery and marker delivery retain separate outcomes.

For read-only inspection:

```sh
~/.agent-host/production/bin/agent-host dingtalk status --home ~/.agent-host/production/state
~/.agent-host/production/bin/agent-host run show --home ~/.agent-host/production/state --workspace ~/.agent-host/production/workspace RUN_ID
```

The existing memgov service is stopped, including its group handling. The new
service has completed its first owner DM, including native execution, memory
processing and a platform-accepted reply. The measured first run took about
42 seconds. See the [deployment record](service-install-detail.md#deployment-evidence)
for evidence and remaining checks.
That historical text-only deployment record does not validate the new concurrent
conversation, attachment or quoted-input paths; those checks are tracked separately.

## Register production projects

Stop the installed service before changing the project registry, then use the
installed binary with `--home ~/.agent-host/production/state`:

```sh
launchctl bootout gui/$(id -u)/com.agent-host.production
~/.agent-host/production/bin/agent-host project add --home ~/.agent-host/production/state --id api --workspace /absolute/path/api
make install
```

Use `project update --id api --workspace /new/path` with the same state home to
move its execution directory. Listing and inspection are read-only and remain
available while the service runs. Automatic routing during chat requires no
restart. Claude auto memory is disabled per invocation without editing personal
settings. See [runtime assembly](runtime-assembly.md).
