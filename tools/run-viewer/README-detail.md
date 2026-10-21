# Local execution viewer details

See the [main guide](README.md) for scope and usage.

## Boundary and data flow

`server.py` uses only Python's standard library. It serves `index.html`,
`style.css`, and `app.js`, plus these JSON endpoints:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/records` | Recent database runs and saved report runs, with source identity |
| `GET /api/record?key=...` | One run, complete event journal, related candidates/memories and source metadata |
| `GET /api/memories` | Memory library, experience drafts, annotations, source/run links, recall decisions and local action token |
| `POST /api/memory-action` | Publish/reject an existing live owner draft or disable a current memory through its running Host |

Database keys are `db:RUN_ID`. Report keys combine a hash of the selected report
path and its Run ID, so identical IDs in distinct sources are not conflated.
Execution and memory requests cannot select arbitrary files or change their
configured source paths. The separate settings API intentionally edits only
enumerated owner-local configuration files; see the
[settings implementation](../../docs/settings-console-detail.md). Saving source
configuration does not replace the viewer's current catalog or restart services.

SQLite is opened with `mode=ro`, `query_only=ON`, and a read transaction per
request. Legacy reads invoke no Host process. A file-memory backend uses the
explicit local Host verifier to validate publication and rebuild derived cache;
it starts no business task, native harness, or model call. Memory actions delegate to the existing running Host and may
invoke Jev relationship comparison before publication. Schema versions 1–15
are supported; v1 has no candidate table.
The additive v3 retrieval indexes, v4 channel tables and v5/v6 reaction outbox do not change the
queried run/candidate payloads. Run provenance is retained in raw data; the
viewer never reads channel reply capabilities or the channel inbox.
An unsupported version or malformed existing source returns an explicit error.
A missing database is an empty source, and its directory is not created.
Concurrent Go-host updates appear on a subsequent refresh.

## CLI sources and empty views

Managed `--config-dir` startup mounts the fixed `ROOT/cli-state` even before it
exists. `service workbench` provides the same projection without enabled
channels. Each source keeps its database, identity filters and record links;
qualified CLI keys are `cli:db:RUN_ID`. No external JSON reports or native
desktop conversations are imported. A CLI source must stay private, owned by
the current user and free of symlinks, including when created after startup.

The records index adds `sources`, with database source ID/kind/name, `home`,
`record_count` and `status`. Status is `missing_database`, `no_runs`,
`scope_filtered` or `ready`; a multi-source read failure is `read_failed` with
an explicit `error` and unknown (`null`) count. Healthy records remain available;
failure is not interpreted as a successfully read empty source. The ordinary
single-source endpoint retains its HTTP read error. Scope filtering happens
before limits and applies to direct record URLs too.

The trace page renders source diagnostics as text, identifies an unmounted CLI
source in legacy multi-source mode, and distinguishes local search/source
filters from an empty catalog. Refresh repeats discovery at the registered path;
it neither creates state nor guesses another home. Compare this path with the
explicit Chat home to diagnose a runtime mismatch. Errors before Host creates
a run remain terminal diagnostics rather than fabricated run records.

## File-memory projections

`state/file-memory.json` selects the published file backend. The configured
`--host-binary` runs `memory verify --home STATE_HOME` with a bounded timeout;
managed startup supplies its running executable. A failed or unavailable verifier
is an explicit read error. Missing index files are rebuilt by Host, with no Python
memory publication or fallback to historical operational memory tables.

After verification the viewer holds a shared `publish.lock`, attaches the derived
index read-only as `published_memory`, and checks its checkpoint generation,
SQLite integrity and complete memory payload set against current file records.
Record/document SHA256 and source, state-home, domain, Agent, audience, scope,
ID and revision must agree with the publication manifest. Memory library reads,
run-detail current memories and owner-action prechecks use the attached memory
tables. Run, event, project and candidate reads use the preserved operational
database. The lock is released when the projection connection closes.

The same partition and project filters apply to both backends. The UI labels
verified file memory separately from operational state and archived reports.
Source damage, cache mismatch or a publication race cannot silently show stale
old rows. Existing archived injection contexts retain their original revisions.

Runtime feature phases carry their Host-defined label and parent phase in the
journal. The viewer includes observed child stages, repeated calls and their
events under execution; parallel calls remain running until all observed call
IDs have terminal phase events. Schema v8 changes reaction occurrences without
changing the run payloads read here.

Schema v7 adds a project registry. `--project ID` resolves its stored `scope`;
`--workspace PATH` resolves the canonical path against `projects.workspace`,
falling back to the path as a legacy scope when it is unregistered. The same
helper filters execution rows, memory rows, and memory source-run observations
within each read transaction. A project keeps its namespace when its checkout
moves, so the new workspace filter still includes old runs. Unknown project IDs
fail, including with a legacy or missing database. CLI filters are mutually exclusive.
Archived sources remain historical snapshots outside project filters, but follow
the configured Agent and audience partition.

Schema v10 separates Agent and audience identity from project scope. The viewer
defaults to `--agent default --audience owner`; alternate selections apply before
run and memory limits, and direct record URLs reject another partition. Run detail
loads only memories from the run's partition and participating scopes. Historical
payloads missing identity fields are interpreted as `default` / `owner` without
rewriting them. The UI displays instance, audience and stored member attribution;
these diagnostics remain local and are not added to channel replies.

Run summaries expose recorded `project_id`, `project_name`, `workspace`, Host
and native session IDs, and `runtime_fingerprint`. A missing/empty `workspace`
falls back to the historical `scope`; the viewer does not backfill a run's
project name or working directory from today's registry. Raw database payloads
remain unchanged. Report reconstruction prefers recorded run fields, then
`run.started` fields, with legacy `workspace` as the scope fallback.

The database list is limited to the latest 200 runs; its total is exposed in
the API and the count tooltip. Details include the full stored event journal.
Reports are loaded from the configured directory's `*.json` files, with a
32 MiB limit per report. This is a bounded local inspection tool, not a capacity
or production-performance claim.

## Report reconstruction

The supported report formats include the existing experience experiment: `runs[]`,
`commands[].events`, and explicit JSON output from memory inspection commands.
Native retrieval experiments with inspected `commands[].run` payloads are also
adapted into the same trace representation without changing the saved report.
The loader merges journals by `(run_id, seq)`, including later acceptance events.
Conflicting duplicate events fail rather than being silently overwritten.

The run's original receipt is retained separately. Explicit memory snapshots
and later inspection output retain the last observed memory state. Recalled
memory snapshots remain unchanged; a reuse run may therefore show active
memory at injection and invalidated memory at the report's final inspection.
This is historical observation, not a query of the isolated experiment's now
deleted database.

Where a report lacks a candidate inspection row, the browser correlates
`candidates.saved` IDs with eligible `experience.assessed` proposals and labels
the result as reconstructed observation. Later recorded acceptance/rejection
events update that archived observation. Database candidate rows remain the
authority for current status; the viewer never infers acceptance from Jev
eligibility alone.

## Presentation semantics

- Route nodes use recorded `phase.started`/`phase.finished` events. Unobserved
  phases are not treated as successful. Retry and review events remain in the
  same ordered journal, even when their `elapsed_ms` origin resets.
- Duration bars show the latest completed attempt per phase. Total automatic
  time comes from the original `run.finished.elapsed_ms`; manual review is
  separate. Unknown durations remain unknown.
- Actual models are read from native initialization and decision/extraction
  result events. Requested aliases remain distinct from returned model names.
- Tools are paired by normalized `data.tool.id`; missing call/result contents
  remain explicitly absent. A tool's error does not overwrite the final Run
  execution status.
- Jev question choices, probabilities, `confidence`, policy and threshold are
  shown from audit data. The UI does not re-classify or re-evaluate a candidate.
  A `no_existing_memory` comparison is labeled as a local check with no Jev call.
- Pending candidates, conflicted/invalidated memory, and elapsed `expires_at`
  are displayed separately. The viewer does not change eligibility or memory.
- Event views display up to the latest 200 matching events and say so when
  capped. The raw-record view retains the full fetched data. Large native
  envelopes and text deltas are optional in the event view.
- A visible tab polls every five seconds, updating changed details and the run
  list. Polling errors retain old content with an explicit stale-data warning;
  they do not silently switch to reports or another implementation.

## Local HTTP boundary

### Session groups

The execution index exposes only room ID, request ID, received time and sender
identity from the run's room snapshot; it does not copy room history. The list
groups by source, Agent, audience and room ID, including forked runs. Old rows
fall back to their recorded Host session, then native session; rows without an
identity stay separate. Reset room generations and archived sources never merge.

Conversations sort by latest loaded message time. Messages within a conversation
sort by recorded received time, falling back to run creation time, with stable
timestamp/key tie breakers. Numbers describe loaded messages only: existing
per-source recent-run limits remain in force. Search matches whole loaded
conversations, preserving context and numbering; each run keeps its own selected
state and execution-detail link. Collapsed groups retain their state on refresh.

The entry selector uses the explicit source `channel` metadata rather than the
database/report storage kind. CLI Chat includes Web Chat when both use the same
configured CLI state source. Known channel labels are CLI Chat, Feishu and
DingTalk; custom mounted channels retain their own names. Standalone state and
unqualified archives keep separate local/archive options. Configured sources
without records remain selectable. Switching entries selects a run from that
entry or clears stale details when it has no records. Refresh preserves the
selection and does not rebuild an unchanged selector.

### Chat outcomes

Chat keeps the process lifecycle (`status`, `exit_code`) separate from
`execution_status` and `memory_status`. Public `run.finished` receipt data supplies
these outcomes and their `execution_error`, `memory_error` and `experience_error`;
raw stderr remains private. Execution phase completion is visible while Host
finishes memory processing. A memory failure does not label the completed model
execution as failed. Errors render as literal text, with their processing stage.

The room receipt persists these fields across viewer restarts. Older terminal
messages without separate outcomes read only the exact `run.finished` event from
the configured destination's read-only state database. Missing evidence stays
unknown; answer text alone never proves successful execution. No task is replayed.

### HTTP routes

The server binds to `127.0.0.1` and accepts only matching loopback Host headers.
Only fixed asset and JSON routes are served. PUT/PATCH/DELETE and unsupported
POST routes return 405. `POST /api/memory-action` requires a matching Origin,
the per-process token returned by the same-origin memory read, JSON content
type and a body of at most 8 KiB. Unknown fields and actions are rejected.
Responses use no-store, nosniff and a same-origin content security policy.
Untrusted requests, model output and tool data are rendered with `textContent`;
no source HTML, Markdown scripts or commands are executed. No external assets
or telemetry are used. This is not a remote or multi-user security design.

The managed viewer installation includes `executions.html` alongside the root
workspace page, so execution links work from installed runtimes as well as the
source checkout. Viewer preparation tests check its installed contents explicitly.

## Validation

```sh
python3 -B tools/run-viewer/server_test.py
node --check tools/run-viewer/app.js
make check
```

The viewer tests cover no state creation/migration, read-only enforcement,
unchanged database bytes, project/workspace filtering after a checkout move,
legacy directory fallbacks, explicit unknown-project errors, subsequent reads of newly added
runs, explicit malformed-source errors, HTTP boundaries, and real archived
review/reuse/invalidation provenance. Browser inspection covers record selection,
phase details, probability-based rejection, evidence navigation, memory status
and source/search controls. These tests do not prove production scale or
memory-model accuracy.

## Owner-local memory controls

Each managed channel Host opens `state/memory-control.sock` with mode `0600`
after acquiring its existing runtime writer lock. Python retains read-only
SQLite connections and forwards only `accept`, `reject`, `invalidate` or `restructure` to the
selected source's socket. The source/channel-qualified record key resolves to
an existing row in the configured project, Agent and owner partition; paths,
model settings and credentials never come from browser requests. Offline
sources, report snapshots and group-audience projections cannot mutate state.

The Go endpoint validates fields and bounds, scopes the Store to the requested
Agent's owner partition and serializes local actions. Draft review requires a
finished source run. Publication reuses `Host.AcceptExperience`, including Jev
comparison, evidence validation, revision checks and conflict/duplicate handling;
it does not run an agent or replay the source business task. Rejection reuses
`Store.RejectCandidate`. Disabling reuses `Store.InvalidateMemoryRevision`, whose
displayed revision check and invalidation are one transaction. Reject/disable
reasons remain persisted with the original record and evidence.

Drafts are separately bounded to the latest 200 per source and never counted as
effective memory. Detail dialogs show applicability, original steps and recorded
assessment evidence. Polling pauses while the dialog is open. Actions disable
their buttons while pending; failures are shown and never automatically retried.
After transport interruption, refresh to inspect state before any further action.
Standalone CLI mutations still require the runtime lock; Web actions use the
already-running writer and do not weaken that ownership rule.

Regression checks cover actual Host publication and persisted rejection/disable,
no source replay, stale revision and reviewed-candidate rejection, private socket
lifecycle, project/source/partition isolation, report exclusion, body bounds and
cross-origin/token rejection. These controls retain the currently implemented
explicit publication workflow; automatic experience publication remains separate.

## Memory projection and visualization

Schema 9 adds project-set payloads. Execution and memory-use filters include a
run when the requested project is a member even if its compatibility scope belongs
to a neutral cwd or a different primary project. The memory table remains filtered
by its own stable scope. Joint run detail loads only participating scopes and
aggregates all per-scope retrieval decisions instead of keeping the first event.
Recorded project roots, extra directories and user supplements are displayed
without replacing historical data with today's project registration.

`memory_view.py` reads memory payloads and, for schema v3–v13, their separate
stored `memory_annotations`. Missing required annotations fail explicitly.
Legacy v1/v2 rows retain absent metadata; the viewer does not migrate or extract
new identities. Memory reads and related runs/events use one read-only database
snapshot. Channel inbox and reply capabilities are never queried.

### Bounded list reads

Memory database projections select only `memory.retrieval.evaluated` and
`decision.completed` events in SQLite before decoding them. Agent inventory
reads select execution initialization evidence in reverse sequence order and stop
at the newest admitted inventory; skill assembly uses its own event filter.
These list paths do not decode native tool transcripts or scan them for every
memory. Execution details still load the complete stored event stream. Existing
source, partition, history limits and file-memory verification remain in force.

JSON uses compact serialization and, for responses of at least 1 KiB, gzip level
1 when the client explicitly accepts gzip with positive quality. Responses vary
by `Accept-Encoding`, retain `no-store`, and keep the same decoded JSON fields.
Clients requesting identity or disabling gzip receive uncompressed JSON.

Report projections retain explicit inspection rows, injected memory snapshots
and the existing-memory text/ID pairs recorded in Jev comparison inputs. Later
inspection lifecycle state takes precedence over older injection snapshots.
Missing provenance fields can be filled only from another observation of that
same source-qualified memory ID. Sources and identical IDs in different report
files remain separate. Missing source-run IDs remain missing.

When a report contains exactly one input identical to a memory's text and a
`memory.query.analyzed` event, its raw annotation can be displayed as
`recorded_input`. This is labeled as a historical input annotation, rather than
a fresh read of the deleted experiment database's derived index. It never
establishes an inferred source-run ID or fabricated admission decision.

Cards expose all published lifecycle states; pending candidates remain in the
execution viewer. The graph is a bounded projection of at most five filtered
records, showing each record's first object and primary topic. An object/topic
edge is drawn only when the recorded topic binds that object ID. Remaining
annotations are available in the detail view. Graph nodes support click and
keyboard activation, and reduced-motion settings disable animated edges.

Jev eligibility probability and its host threshold are distinct from the
memory-kind probability, which currently has no independent gate. A source
decision is shown only for a recorded `source_run_id` linked to a loaded run.
The detail lifecycle leaves unknown stages explicitly unobserved. Source-run
user-statement judgments are shown only for user-statement memories; they must
never be attributed to a reviewed procedure from the same run. Procedure
assessment and review remain accessible through the source execution trace. Recalled
context distinguishes actual injection from an accepted candidate that did not
fit assembly, and from an excluded candidate.

The projection loads at most 200 memory rows and the latest 200 database runs.
The UI reports an incomplete library when the memory cap is exceeded. Use
`--project` or `--workspace` to narrow the stable database memory namespace;
archive sources retain their original
scope and are independently selectable. Polling errors mark retained content
as stale. Unchanged responses preserve an open detail view's interaction state.
No external dependencies, assets, scripts or model calls are introduced.

Validation adds `python3 -B tools/run-viewer/memory_view_test.py` and
`node --check tools/run-viewer/memory.js`. Tests cover exact source preservation,
workspace isolation, missing metadata errors, current/archived distinction,
duplicate-ID source separation, retained invalidation, decision attribution, explicit bounds, and
loopback/read-only HTTP behavior. Browser checks cover filtering, graph links,
memory details, trace navigation and responsive layout.


## Verified memory read cache

Each channel Catalog serializes memory validation across HTTP threads. The first
read runs the Host verifier and validates the published index against current
authoritative records. Subsequent reads reuse that successful audit while the
filesystem signature is unchanged. The signature covers the backend marker,
publication manifest and lock, domain mappings, every referenced revision's
record/document, Host executable, index and index WAL. File identity, mode, size,
mtime and ctime detect replacements and same-size edits with restored mtime;
ancestor identity/mode checks reject symlink substitutions. Directory timestamps
and operational state changes do not invalidate the memory audit.

Reads still open a fresh read-only SQLite snapshot and hold the shared publication
lock. On a signature change they release that lock before Host verification,
then reacquire it and check that the authoritative inputs stayed unchanged.
Only successful validation can authorize reads. Verifier failures are shared for
one second to prevent queued readers launching identical failed jobs; a later
refresh retries, and changed inputs bypass that cooldown. Missing or corrupt data
fails explicitly; there is no fallback to the last verified contents. This is an in-process local
filesystem optimization, not protection against a hostile filesystem or a change
after the read snapshot has been established. Full `memory verify` remains
available independently; the first read after startup/change retains its cost.

Regression tests cover concurrent readers, generation/index changes, missing
index recovery, same-size edits with restored mtime, verifier failure and an edit
during verification. The record response and memory partition filters are unchanged.

## Shared channel workbench

The Go service supervisor starts one viewer with repeated
`--channel-home NAME=STATE_PATH` arguments and `--no-reports`. It owns the viewer
lifetime; channel workers do not start viewers. The fixed default port is 8765.
Execution and memory projections combine sources while qualifying their UI keys
with channel names. Stored run/memory IDs, raw events and tool inputs remain
unchanged. Memory origin, usage and annotation links return to their source's
execution record, including when two databases contain identical IDs.
Each source retains the existing Agent/audience partition. A source read failure
fails the combined request explicitly rather than silently omitting a channel.

The `restructure` action is available only for active reviewed procedures and
requires the displayed revision. It delegates to `Host.RestructureExperience`
and the atomic revision operation documented in
[experience details](../../docs/experience-memory-detail.md#successful-procedures-and-avoidance-notes).
The page renders successful steps separately from avoidance notes and preserves
the prior version and extraction/assessment audit.

### All-chat console view

Managed startup passes `--all-chats`. This removes Agent/audience read filters
for run lists, memory lists, candidates and archives. Project/workspace filters
and existing bounded load limits still apply. Run details query memories using
the selected run’s own Agent and audience. Action lookups retain the configured
owner partition, and group/other-Agent rows do not advertise management controls.
Regression coverage checks group and alternate-Agent listing, provenance links,
partitioned run details, unchanged filtered mode, and denied group mutations.
