# Conversation branch design details

[User-facing proposal and scope](conversation-branches.md).

## Existing behavior and evidence

- `internal/cli/chat_state.go` shares CLI home ownership; session locks in
  `internal/state/lock_unix.go` protect one native conversation. Preserve those
  locks: parallelism comes from distinct child sessions, not removing ownership.
- `internal/harness/claude/adapter.go` currently chooses `--session-id` or
  `--resume`; it does not forward a fork option. The installed Claude help and
  [official CLI reference](https://code.claude.com/docs/en/cli-reference) describe
  `--resume <id> --fork-session` as creating a new session ID. This documents the
  primitive; it does not prove an atomic active-session checkpoint or Host support.
- `internal/harness/acp/adapter.go` currently creates sessions or reconnects using
  advertised resume/load capabilities. It does not call `session/fork`.
  The [ACP session setup draft](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/draft/session-setup.mdx)
  mentions fork on supporting agents. Protocol documentation is not evidence that
  the installed bridge implements the required behavior. Probe its advertised
  capability and test actual fork/reconnect behavior before implementation.
  Local inspection of the installed `claude-agent-acp` 0.81.2 adds concrete source
  evidence: `dist/acp-agent.js` advertises `sessionCapabilities.fork`, and
  `dist/fork-session.js` calls the Claude SDK's `forkSession`, optionally with
  `upToMessageId`. Its checkpoint metadata currently uses a vendor-specific
  extension; keep that mapping inside the adapter rather than treating it as a
  portable ACP field. The fork handler returns a persisted child ID; verify the
  subsequent child resume/load lifecycle before prompting. These source paths
  narrow the feasibility work but are not a completed live fork acceptance test.
- `internal/host/instances.go:invocationInstructions` supplies authenticated
  `sender_id` and `conversation_type` each turn, with group-specific attribution
  guidance. Existing fixture tests capture those instructions. Their inclusion
  alone does not prove how a real resumed model retains every earlier speaker.
- Group routing shares a conversation among members; private routing retains
  separate audiences. Branching must preserve channel/account/tenant, Agent,
  audience and project authorization boundaries.

## Ownership model

Keep three concepts distinct:

| Concept | Responsibility |
| --- | --- |
| Conversation | Authorized channel audience and the visible message timeline. |
| Task branch | Task owner, title, parent checkpoint, follow-up routing and native session binding. |
| Run | One execution attempt, actual effects, result and delivery evidence. |

A branch records its parent branch and completed source run/checkpoint, alongside
the actual child native session identifier returned by the adapter. Reuse existing
Session/Run identities where they fit; do not duplicate native transcripts into a
new Host-owned model loop. A shared conversation can have many branches, and a
branch can have many sequential runs.

Completed means a stable recorded turn boundary, with execution outcome and
memory/acceptance outcomes retained separately. Initial fork support uses an
eligible completed parent; unknown or interrupted parents do not become resumable
or replayable through a fork request. Preserve inspectable failure evidence.

## Fork and context semantics

Before launching child native work, validate the requested audience and source
checkpoint, record branch intent, obtain the adapter's actual child identity and
persist its binding. Enforce one execution lease per child session. Retain brief
checkpoint/fork setup coordination on the parent as required by the native
contract, releasing it before model/tool execution on the child.

If the engine cannot select an immutable historical checkpoint, do not promise
that forking a moving parent copies its last completed turn exactly. First prove
a reusable immutable base or require a quiescent source for that backend. The
first feasibility gate must resolve this before enabling busy-parent branches.
Do not copy private native files while an engine is writing them.

A fresh context assembled from authorized Host evidence is a separately named
operation with explicit reduced-history semantics; it is not a transparent
fallback for a failed native fork. A fork never replays the parent's tools.
Reapply current tool and audience policy; historical context cannot grant access.

Context is fixed at branch intake. A result from sibling B is not injected
mid-run into A. A later request can explicitly reference B's result and its
source. A synthesis branch reads selected completed results and reports
disagreements without rewriting their original histories or replaying effects.

## Intake and follow-up routing

Persist conversation/message-to-branch links before acknowledging intake:

1. Explicit branch choice or a reply to an attributed task/result message wins.
2. An explicit new-task action creates a sibling branch from the selected base.
3. A clear continuation of the requester's single active task may use that branch.
4. Ambiguous continuations with multiple candidates require a task choice.

Automatic semantic topic classification is not required for first delivery.
Group members may collaborate on the same branch by replying; do not replace
task identity with sender identity or create a permanent branch per person.
Queue follow-ups within a busy branch. Use existing channel concurrency limits
and expose capacity queueing independently from session ownership conflicts.
Cancellation targets the branch/run, leaving siblings active.

## Speaker attribution and replies

Build a structured envelope from verified channel metadata for every message:
platform/account/tenant, conversation and message IDs, sender ID, available display
name and quoted-message reference. Keep task initiator, current sender, demand
owner and notification recipient as distinct values. Names are display labels;
authorization uses authenticated identity and existing policy. Resolve missing
names only through authorized platform/directory data; otherwise use a stable
neutral label instead of guessing a person.

Retain speaker labels with each message in native history/context, not only in a
replaceable current-turn system prompt. Keep authorization in trusted Host
instructions. Message/quote contents remain source material and cannot override
the envelope. Validate this on real resumed and forked sessions: the model must
distinguish “Alice said X” from “Bob quoted Alice saying X”.

Group fork context contains only context visible to that audience. Private
profiles and other conversations are not imported because two accounts map to
the same user. Reply to the originating task's message/thread using the platform's
verified reply target. Keep requester identity separate from delivery audience;
a group result remains visible to the group. Duplicate delivery must not create
a new branch or repeat external actions.

## Resource effects and memory

Native session separation does not isolate a shared working directory or remote
resource. Read-only tasks may overlap; conflicting writes use resource ownership
at the narrow operation/task boundary. Repository edits can use separate
worktrees where appropriate; an external action needs its existing idempotency
and permission controls. Do not turn a resource conflict into a home-wide lock.

Shared memory retains its publication coordinator and source attribution. A
branch conclusion is not automatically accepted truth or group consensus.
Cancellation/failure does not publish an invented successful result. New turns
retrieve eligible published knowledge under existing policy; running contexts
are not retroactively mutated.

## Acceptance and rollout

The documentation-only check on 2026-10-21 in the current working tree failed
`TestIndependentCLIProcessesExecuteConcurrently` with `SQLITE_BUSY` during run
creation, and `TestCLIWorkbenchObservesOfficialChatAfterStartup` with a native
fixture startup timeout. These observations do not identify a proven root cause
in this design-only change. Restore a reliable persistence/startup baseline
before enabling branch intake; earlier bounded overlap success is not sufficient
evidence of robustness under concurrent test load.

Verify two child sessions inherit the intended completed context, execute
simultaneously, preserve their own follow-ups and leave the parent unchanged.
Verify a busy-parent fork uses the advertised checkpoint or clearly reports the
unsupported case. Exercise restart between fork creation and binding persistence,
request redelivery, child cancellation and uncertain outcomes without replay.

For groups, have Alice and Bob start different tasks, then cross-reply to one
task. Verify speaker labels, branch selection, quote provenance and result targets
on both supported platforms. Confirm no owner-private context crosses into the
group. For Web, verify branch switching and live task statuses through refresh.

Add branch metadata compatibly; existing linear sessions remain valid. Enable
the new intake path only after adapter acceptance passes. Rollback disables new
branch creation while retaining branch evidence and allowing supported existing
branches to finish. No destructive transcript conversion or automatic merge is
needed.
