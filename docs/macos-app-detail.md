# macOS workspace app details

[User guide](macos-app.md).

## Platform and lifecycle

`NativeBridge.swift` accepts connection requests only from the app-owned main
frame at the current loopback origin. The file chooser provides the path;
page scripts cannot supply it. `connection.py` reuses Chat's existing validator
without starting a task and returns only the selected config, Agent and state
home. Credentials and raw validator output never cross the bridge.

Connection overrides are private local files bound to the bundle's original
source configuration. Inbox and draft files remain isolated by the effective
source set. The native draft backup is not a task submission or Host memory.

`maintenance.py` serializes viewer mutations with its quiescence check. Connection
switches require all viewer-owned conversations and child processes to be idle.
Updates additionally query every mounted source, independently of UI pagination
and scope, and require terminal execution outcomes plus final `run.finished`
receipts. Missing stores, stale snapshots and unknown activity defer updates.
No source database is modified by this check. An owner-token protected resume
action reopens mutations if a restart cannot proceed.

The app uses AppKit controls, NSPopover, NSStatusItem, WKWebView and
UNUserNotificationCenter. Native controls follow system appearance, provide
keyboard traversal and accessibility labels, and use no custom motion. The
workspace retains its existing keyboard and appearance behavior. Source builds
produce a local ad-hoc signed `.app`; no release asset, installer, notarization or
binary distribution is added. Python remains a local runtime dependency recorded
by absolute executable path at build time; rebuild after moving that interpreter.

`make macos-app` compiles the Host, builds the native shell, copies the same viewer
assets and Host executable into the bundle and records explicit viewer arguments.
It neither launches the app nor registers login items. Rebuilding the bundle does
not update a running app or any production deployment. Distribution remains
source-only. Normal builds initialize their own runtime on launch; existing CLI
handoff configuration is an explicit advanced import. See [app-owned runtime](#app-owned-runtime).

The app owns one Python viewer child with an ephemeral loopback port. It consumes
the viewer's printed URL and terminates only that child on Quit. A viewer crash
leaves explicit unavailable status and relaunch guidance. It never replays tasks.
The original workspace handles new conversations, session attachment, evidence,
memory and settings; no separate Web product or additional server deployment is
required. An app-local viewer is still a technical dependency.

## Projection and controls

Every menu selection, inbox destination and notification payload uses the exact
catalog key (including channel prefix). A notification opens both that record
and the existing Chat session attachment path. Attachment failures stay visible
in Workspace, including archived, busy or unsupported sessions. Old inbox links
whose sources were removed fail explicitly in the record inspector.

The app polls `/api/records` and scoped `/api/record?key=...`. It reads the same
Host run, events and `control` object as the execution inspector. A failed source
is unavailable, never idle. Missing states remain unknown. No Host journal is
modified by inbox projection. The current catalog limit applies (200 runs per
source); an app that was offline beyond that window may not discover older events.

`run.started` maps to started. `execution.ready` maps to execution completion
before memory closeout; `run.finished` supplies terminal results when there was
no earlier ready event. Final `canceled` maps to stopped, never the Stop response.
Historical `native.evidence` records can still map a recorded permission request
to input-needed evidence. This is presentation compatibility for retained evidence,
not an ACP execution path. The shell does not add harness protocol behavior or
pretend to keep an engine waiting.

Stop first reads the record's current mutation capability, then calls the existing
origin-checked `/api/record/stop` with that exact key. Selection changes cannot
retarget the in-flight request. Pending state never rewrites execution status.
Unavailable control includes the Host reason and original-entry guidance.

## Inbox and notifications

The private Application Support journal (mode 0600, atomic writes) stores projected
event IDs, at most 200 recent inbox entries, and per-event `readIDs` and
`acknowledgedIDs`. Its filename identifies the configured source set. Journals
written before these fields existed decode with `decodeIfPresent`: every entry is
unread and none is acknowledged. IDs combine source-qualified key and durable event
sequence; repeated progress, polls and relaunches do not duplicate alerts. The seen
set retains IDs beyond the visible entry limit, so pruned events are not
re-ingested; read and acknowledged state is kept only for retained entries.
Entries are ordered by parsed instant, not by timestamp text.

Inbox rows group retained events by the stable catalog key. Opening a task
(row, popover, notification) marks all its current events read. Acknowledge is a
separate action that covers the task's current attention events only. The status
attention count is the number of tasks with an unacknowledged attention event,
independent of read state and of raw record status. The filter and search field
sit outside the rebuilt list, so polling does not reset focus, filter or query.

Notifications use a task category with an Open task action; default clicks have
the same destination. The app asks macOS for authorization and explains denied
permissions with System Settings guidance. Delivery failures do not remove inbox
entries. All alerts omit sound, and event titles contain the request preview;
system preview preferences apply. Policy: `failed` and `input-needed` notify;
`unknown` is in-app only; `completed` notifies only when enabled in Notification
Preferences; `started` and `stopped` never notify. Delivery also requires an
event time (parsed with its UTC offset) at or after launch, and no alert for the
selected task while the Workspace window is visible and key. The foreground
delegate applies the same suppression to delivered notifications.

## Menus and preferences

`installMenus` builds App, File, Edit and Window menus. Items target the same
methods as the popover buttons. Edit items go through the responder chain so
WebKit handles them. `validateMenuItem` disables New Task until the workspace
page has loaded from the app's own viewer. Notification Preferences is a separate
native window (`openPreferences`) that can host other preferences later.

## Validation

`python3 tools/macos-app/check.py` runs model assertions and compiles the AppKit
entry without launching it. Existing `record_control_test.py` verifies exact-key
stop authorization and Host HTTP behavior. `make check` includes both. On other
platforms, the Swift/AppKit check reports an explicit platform skip.

Pending authorized macOS smoke procedure: build against an isolated Host fixture;
launch the app and verify the workspace and persistent menu; create a task;
select that run in the popover and confirm the same key/conversation; request Stop
and observe Host's final record; open an inbox event and a system notification;
repeat with notification permission denied. Check keyboard traversal, VoiceOver,
light/dark appearance and Reduce Motion. Do not use production channel identities
for development acceptance. None of these desktop steps is implied by a build.

## App-owned runtime

`runtime.py` (bundled, Python stdlib only) prepares the app runtime offline. It
never calls a model, reads another runtime or selects a native harness.

- **init** runs on every normal launch with a 40-second bound. It creates
  `Application Support/Agent-Host/runtime` privately (0700, owner-checked), using
  a staged rename for the initial `config/ cli-state/ workspace/` tree and marker.
  It registers project `local` with the bundled Host so an empty state is distinct
  from a missing source. Symlinks, foreign ownership and an unmarked existing root
  fail closed.
- **seed** creates only `agents.cli.json` and `web-chat.json` (0600). The default
  Agent has `native_skills: []` and `knowledge_sources: []`. The handoff contains
  only `--home`, `--agents-config`, `--agent` and `--workspace`; omitting
  `--offload-config` deliberately inherits Offload's current default. No model,
  ACP, Claude executable, native profile, provider or credential is discovered or
  created.
- Repeat init preserves the Agent catalog and unknown handoff fields, rebasing only
  the bundled Host executable. Unsupported legacy backend flags are reported as
  `needs_attention` and left unchanged rather than weakened or silently migrated.
  Advanced imported configurations may select an explicit Offload profile, but the
  app-owned runtime always inherits the current default.

With no viewer arguments, `NativeBridge.startupArguments` initializes this root
and starts the viewer with its private state, backup root and Chat handoff. Explicit
advanced/imported arguments remain verbatim. The app never adopts the user-level
handoff for its marked root.

Settings exposes the app handoff as evidence and the app Agent catalog through the
existing editor. The settings-field merge retains current Host model, provider,
channel and exact capability fields. The app handoff itself adds no backend fields;
it explains that backend/model selection belongs to Offload. Agent capability
validation remains fail-closed, and an empty native Skill selection means zero
Skills. The maintenance gate still prevents configuration writes while a task or
closeout is active.

`app_prerequisite_notice` checks owner-local files only: the bundled executable,
bounded state/workspace paths, supported option names and readable Agent catalog.
It sends no task and performs no provider/sign-in probe. The app-owned launch
adapter then invokes the current Host Chat contract directly because the older
handoff helper still requires a removed native-profile shape when no explicit
Offload profile is present. This exception is marker- and path-bounded; imported
handoffs retain the established validator. A missing or invalid Offload default
surfaces explicitly instead of falling back to another provider.

## Source and acceptance boundary

The files and tests described here are delivered source behavior in this checkout.
They do not prove adoption by a separately deployed runtime or installed app.
`tools/macos-app/check.py` exercises the Python helpers everywhere and compiles
the Swift/AppKit sources on macOS without launching the app. Visual layout,
VoiceOver, menu/popover interaction, real notifications, app launch and a complete
Offload-backed task remain pending desktop acceptance.
