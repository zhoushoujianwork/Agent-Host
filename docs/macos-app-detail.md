# macOS workspace app details

[User guide](macos-app.md).

## Platform and lifecycle

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
source-only. Start the app after configuring the existing CLI handoff; see
[workspace setup](web-workspace-detail.md#web-chat-execution).

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
`native.evidence` with `acp_update`, a `permission_request` and a cancelled outcome
maps to input-needed evidence. Selected/bypassed permission outcomes do not ask
for input. The current engine cannot resume such a cancelled approval request;
open its conversation to inspect and decide the next request. The shell does not
add harness protocol behavior or pretend to keep the engine waiting.

Stop first reads the record's current mutation capability, then calls the existing
origin-checked `/api/record/stop` with that exact key. Selection changes cannot
retarget the in-flight request. Pending state never rewrites execution status.
Unavailable control includes the Host reason and original-entry guidance.

## Inbox and notifications

The private Application Support journal stores projected event IDs and at most
200 recent inbox entries. Its filename identifies the configured source set.
IDs combine source-qualified key and durable event sequence; repeated progress,
polls and relaunches do not duplicate alerts. The seen set retains IDs beyond the
visible entry limit. First discovery populates old entries, but only events from
this launch onward request system delivery. Events that occurred while the app
was closed remain in the inbox without retrospective alerts.

Notifications use a task category with an Open task action; default clicks have
the same destination. The app asks macOS for authorization and explains denied
permissions with System Settings guidance. Delivery failures do not remove inbox
entries. Completion alerts are configurable and all alerts omit sound. Event
notification titles contain the request preview; system preview preferences apply.

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
