# Mac app interaction design

Status: proposed. This design treats the Mac app as the single product and
interaction entry point. The existing Web workspace becomes the workspace
surface hosted by that app; it is not maintained as a separate Web product.

Source status: the AppKit shell, private local runtime, inbox, notifications,
guarded source updater and headless/native compilation checks are delivered in
this checkout. The private runtime inherits Offload's default and does not create
ACP or native Claude profiles. This source status is separate from installation
or production adoption. The focused visual smoke, VoiceOver, notification-delivery
and end-to-end desktop acceptance in criterion 7 remain pending.

## Product shape

Use a native macOS menu bar app as the persistent entry point and notification
owner. A normal app window hosts the existing workspace interface so memory,
execution records, settings and chat remain one continuous experience. Keep
navigation, selection and conversation context shared between the menu bar
popover and the window.

## Primary interactions

- The menu bar icon communicates idle, running and attention-needed states with
  a clear text label and accessible description; color is supplementary.
- Selecting the icon opens a compact popover with the current task, Agent,
  concise progress, and the latest actionable events. Provide Open Workspace,
  New Task and Stop when supported by the owning Host execution.
- Task events appear in a unified inbox: started, completed, failed, waiting
  for input and stopped. Each item opens its exact execution record or task
  conversation. Avoid duplicate alerts for repeated progress updates.
- The workspace window provides task conversations, execution evidence,
  memory and settings. A task conversation shows its state, progress, messages,
  evidence and supported controls together; selecting a task from the popover
  opens the same task in the workspace.
- Notifications are actionable and respect macOS notification preferences.
  Failures and requests for input can open the relevant task; completion
  notifications are quiet and configurable. Notification permission denial
  must leave the in-app inbox usable.

## Interaction rules

- A task has one stable identity and one visible state across popover, inbox,
  notification and workspace.
- Stop targets the selected active execution and shows the recorded result;
  do not imply success from a cancellation request alone.
- A request for information routes the user back to that task conversation.
- Show execution completion, task acceptance and memory processing as separate
  outcomes wherever the workspace already records them.
- Empty, loading, offline, permission-denied and unsupported-control states
  explain what the user can do next.
- Keyboard navigation and VoiceOver labels cover the menu bar popover, inbox
  and workspace; use system appearance and respect reduced motion.

## Delivery boundary

Build the native menu bar/window shell and notification bridge around the
existing workspace in one Mac app. Reuse its pages and local Host interfaces;
do not create a second standalone Web product or duplicate task state. Keep
the existing browser development entry available for engineering inspection
only if needed, outside the user-facing product flow.

## Acceptance criteria

1. Launching the Mac app exposes a menu bar status and opens the workspace in
   the app window.
2. A running task is identifiable from the menu bar popover and can be opened
   in the same task conversation in the workspace.
3. Completion, failure and input-needed events appear in an in-app inbox and
   produce appropriate actionable macOS notifications when allowed.
4. Stop is available only for supported active executions and the UI waits for
   the Host-recorded outcome.
5. Popover and workspace show consistent task identity and state, including
   explicit unknown or unavailable states.
6. The user-facing flow does not require maintaining a separate Web product.
7. Headless tests cover event-to-inbox/notification mapping, task selection,
   state consistency and stop-control states. A focused macOS smoke check
   verifies launch, menu bar popover and opening the workspace window.

## References

- [Current workspace scope](web-workspace.md)
- [Workspace implementation notes](web-workspace-detail.md)
- [Architecture](architecture.md)
