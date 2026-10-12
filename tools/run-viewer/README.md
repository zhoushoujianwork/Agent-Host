# Local execution viewer

Status: implemented local, read-only prototype. This is a standalone viewer;
it does not add a Web channel or change the Go host's execution path.

Open saved Agent-Host runs as an HTML dashboard. Select a run to see the core
route, component durations, actual agent/model observations, tool evidence,
Jev choices and probabilities, and the candidate/review/memory lifecycle.
Execution completion, business acceptance, memory persistence, and experiment
results remain separate.

## Start

Requires Python 3.10+ and a modern browser, without additional packages.
From the project root:

```sh
make viewer
```

Run `make` or `make help` to list project commands, viewer options, and startup
examples.

Open the printed loopback URL (default `http://127.0.0.1:8765`). The viewer
automatically loads `~/.agent-host/state.db` and the saved experience reports
in `experiments/experience/results`. Click a record in the sidebar, then click
a phase or switch between events, tools, judgments, memory, and raw data.
The page refreshes every five seconds while visible; the checkbox pauses it.

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
Use `--agent ID --audience ID` to inspect a group or another instance. The default
is `default` and `owner`; both database and archived records follow this partition,
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
State schemas 1–12 are readable without migration. Historical records without
instance and audience fields belong to `default` / `owner`. Older runs display their saved
directory scope when a separate working directory was not recorded. DingTalk run provenance is
available in raw run data, including saved attachment metadata and callback
quote provenance when present. Private reply and input-download capabilities
are never queried; the viewer does not serve saved attachment bytes. Inspect
channel receipt and delivery separately with `agent-host dingtalk status`.

This prototype reads local owner data, binds only to loopback, and has no
execution or review actions. Read errors are shown explicitly; displayed old
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
This is a local inspection prototype with five-second polling. It does not
review, change or delete memories, run agents, or independently verify claims.
