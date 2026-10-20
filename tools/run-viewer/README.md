# Local execution viewer

The root page is now the [Web workspace](../../docs/web-workspace.md): a draggable
conversation canvas, local Chat drafts, memory graph and settings navigation.
The full execution inspector remains at `/executions.html`; existing `/?key=...`
bookmarks open the selected execution inside the workspace. Web Chat execution
is not yet connected; drafts remain local to the browser.

Status: local execution inspection and owner memory management are implemented.
Memory actions use the running Host; the page does not submit business tasks.

Open saved Agent-Host runs as an HTML dashboard. Select a run to see the core
route, component durations, actual agent/model observations, tool evidence,
Jev choices and probabilities, and the candidate/review/memory lifecycle.
Execution completion, business acceptance, memory persistence, and experiment
results remain separate.

## Start

`make run` starts one shared workbench at `http://127.0.0.1:8765/` and opens it
in the default browser. It displays private and group chats from every Agent
in all enabled channels, with channel and chat labels and separate execution/memory identities.
With `--config-dir`, it also mounts `ROOT/cli-state` before the first CLI run;
refresh picks up new Chat records automatically. The source status above the
trace explains missing state, no runs, scope filters and read failures, and shows
the exact state home. An ad hoc `chat --home` elsewhere remains independent.
Run details retain their own chat memory partition. Group and other-Agent
memories are viewable; existing owner memory controls keep their original scope.
`RUN_ARGS='--viewer-port 9000'` overrides
the fixed default. The workbench stops with the supervisor. See
[project-local startup](../../docs/service-install.md) for headless startup.
For CLI-only use without channel credentials, use
`agent-host service workbench --config-dir /absolute/ROOT/config --viewer-source /absolute/checkout/tools/run-viewer`.
The config directory may be empty. Run Chat against `/absolute/ROOT/cli-state`;
see the [CLI-only startup steps](../../docs/service-install.md) and
[source diagnostics contract](README-detail.md#cli-sources-and-empty-views).
When a [workspace backup policy](../../docs/workspace-backup-design.md) is configured,
the execution page shows its last success, archive size, verification time and
failure status. It reads backup health without exposing the recovery key or
creating/restoring archives from the browser.
The commands below start a separate viewer for local inspection; use a different
port when the managed workbench is already running.

Requires Python 3.10+ and a modern browser, without additional packages.
From the project root:

```sh
make viewer
```

Run `make` or `make help` to list project commands, viewer options, and startup
examples.

Open the printed loopback URL (default `http://127.0.0.1:8765`). The viewer
automatically loads `.agent-host/runtime/state/state.db` and the saved experience reports
in `experiments/experience/results`. Click a record in the sidebar, then click
a phase or switch between events, tools, judgments, memory, and raw data.
The page refreshes every five seconds while visible; the checkbox pauses it.
Unchanged file-memory reads reuse a verified snapshot. A changed publication,
manual file edit or index change triggers verification again; simultaneous reads
share that verification. The first read after startup or a change can still take
longer. Verification errors remain visible. See the [read-path details](README-detail.md#verified-memory-read-cache).

To inspect an isolated host or choose an available port:

```sh
make viewer VIEWER_ARGS='--home /path/to/isolated-home --port 0'
```

Use `--project PROJECT_ID` to inspect a registered project's runs and memories,
including history from earlier checkout locations and joint runs containing it. Alternatively,
`--workspace /absolute/project/path` resolves an exact registered workspace to
the same project history, or filters the historical directory scope when the
workspace is unregistered. These two options are mutually exclusive; an unknown
project ID is an explicit error. `--reports` selects another saved experience-report
directory; archived records remain a separate source outside the project filters.
Use `--all-chats` to inspect every Agent and chat in a standalone viewer.
Without that option, use `--agent ID --audience ID` to inspect a group or another
instance. The default is `default` and `owner`; both database and archived records follow this partition,
including memory and source-run links. IDs are available in local run records.
Pass these options through `VIEWER_ARGS`. The server runs in the foreground;
stop it with Ctrl-C. You can also run `python3 -B tools/run-viewer/server.py`
directly with the same options.

## Reading a trace

- **Core route:** click a phase for its recorded component, duration and events.
  “Completed” means the phase ran, even if its decision was to save nothing.
- **Judgments:** inspect the Jev choice, per-choice probability, configured
  threshold, and `confidence` separately. The recorded host decision determines
  the displayed route.
- **Evidence:** follow a candidate's tool reference to the correlated input and
  result. A failing pre-fix test does not imply the final run failed.
- **Memory:** distinguish the memory snapshot injected before execution,
  candidates awaiting review, and the latest observed published-memory status.
- **Execution context:** inspect the Agent instance, audience, recorded project, actual working directory
  (`cwd`), participating projects, additional directories, source-linked user
  supplements, Host/native session IDs, and runtime fingerprint. The memory namespace
  (`scope`) remains separate from the directory; moving a project does not rewrite
  old run context. Search also matches project names, directories and session IDs.

Archived reports are explicitly historical snapshots. Their original completion
receipt may say `review_pending` even when later events show acceptance; a
published memory may subsequently be invalidated. The page preserves both
observations. Live database records reflect the current stored state.
State schemas 1–14 are readable without migration. Historical records without
instance and audience fields belong to `default` / `owner`. Older runs display their saved
directory scope when a separate working directory was not recorded. DingTalk run provenance is
available in raw run data, including saved attachment metadata and callback
quote provenance when present. Private reply and input-download capabilities
are never queried; the viewer does not serve saved attachment bytes. Inspect
channel receipt and delivery separately with `agent-host dingtalk status`.

Migrated state homes with `file-memory.json` read current memory from its
published files and verified SQLite search cache. The viewer runs
`agent-host memory verify --home ...` before those reads, rebuilding a missing
cache through Host. It reports damaged files, stale cache content, or an unavailable
verifier explicitly; it never falls back to the old memory rows in `state.db`.
Managed startup supplies the exact Host executable. For a standalone viewer,
`--host-binary /absolute/path/to/agent-host` overrides the default `bin/agent-host`.
Operational runs, candidates and events remain in `state.db`; saved reports remain
historical snapshots.

The workbench reads local owner data, binds only to loopback, and provides
memory controls when the corresponding channel Host is running. Read errors are shown explicitly; displayed old
data is marked unsynchronized. It does not authenticate multiple users or
provide remote deployment. Stop the process with Ctrl-C.

See [implementation details](README-detail.md) for data contracts, limits and
validation. The host workflow is documented in
[reviewed execution experience](../../docs/experience-memory.md).
The [conversation/input guide](../../docs/channel-conversations.md) describes
concurrency, attachment storage and the separate native/platform validation
boundary. Concurrent turns keep their own run evidence; a completed run does
not imply that every conversation in the inbox has completed.

## Memory workspace

Open `/memory.html`, or click **记忆工作台** in the execution viewer. The memory
workspace shows saved records as cards and an interactive object/topic graph.
Select an object or search text to filter the library. Open a card to inspect
recorded Jev choices and probabilities, source text, revisions and subsequent
injection or exclusion observations. Links return to the exact execution trace.

To inspect the existing three-statement retrieval experiment:

```sh
make viewer VIEWER_ARGS='--reports experiments/retrieval/results --port 0'
```

Select `live-final-2026-10-10.json` to see three memories, two service objects,
four topic labels and two actual injection turns. Report sources remain
separate from each other and from current database state. A credential topic
is not displayed as a service or a newly invented credential object.

Archived input annotations and incomplete source links are explicitly labeled.
The **经验草稿** section shows pending proposals, their applicability, original
steps and cited tool evidence. Open a draft and choose **发布原稿** to publish
that exact proposal through the existing Host comparison and admission path,
or **不采用** with a reason. Published and rejected drafts remain inspectable.
Open an active memory and choose **停用记忆** with a reason to exclude it from
future recall while retaining the source and revision history.

These controls work while the channel service is running. An offline database,
archived report or group-audience view is inspection-only. Stale revisions and
already-processed drafts are rejected; failures remain visible. Refresh after
an interrupted action to inspect its outcome before trying again. Automatic
refresh pauses while a detail dialog is open so it does not erase your input.
The page neither runs business tasks nor independently verifies their claims.

An adopted procedural memory also provides **整理步骤与避免事项**. It uses the
original recorded tool results to separate the successful path from observed
pitfalls, checks the revised lesson, and saves a new version. The original
draft, older versions and source evidence remain unchanged. Review the resulting
steps and **避免事项** in the memory detail. This operation does not rerun the task.

## Settings

Open `/settings.html` from **Settings** in the execution workspace. An explicit
`--backup-root /absolute/host/root` enables the local configuration editor;
managed unified startup already supplies it. The compact OpenFilm-inspired page
exposes appearance, essential Agent choices, native models/provider connections,
existing channel setup and shortcut help. Advanced parameters keep their saved
defaults and are managed in configuration files. Editing a visible field retains
unlisted values. Chat is only a right-side panel; its open/closed state survives
module switches. Cmd+W closes that panel in place, or returns a selected
conversation detail to the canvas when the panel is already closed.

Secret fields load as retained markers, never as their saved values. You can
replace or explicitly clear them. Saves reject conflicting revisions, keep a
private prior copy and run Host configuration validation. Failed validation
restores the original file. Changes to channel services require a restart;
saving does not restart services or run a task. See the
[settings design](../../docs/settings-console.md) and
[implementation details](../../docs/settings-console-detail.md).
