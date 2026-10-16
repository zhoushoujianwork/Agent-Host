# Per-person reply preferences: implementation notes

Companion to the [main proposal](person-profiles.md). These are implementation
directions, not delivered fields, commands or new channel permissions.

## Current baseline and reuse

Channel receipts retain channel, account, tenant, sender, audience and project
scope. `Memory.SenderID` and `internal/state/memory_attribution.go` distinguish
who made a statement. `internal/host/instances.go` supplies authenticated sender
metadata for each invocation. Existing history and assistance paths retain source
citations. Verified private users now have first-contact profile files and
optional configured overrides, implemented in `internal/cli/users.go` and
`internal/assembly/workspace.go`. See [workspace contracts](workspace-layout-detail.md#multi-user-recording-and-retrieval).
The proposal below concerns learned preference observations and their controls;
it must reuse those identity/profile boundaries rather than add a second registry.

Reuse preference publication, revision, exclusion and source-read checks. The
profile is a derived view, not another copy of durable truth or the current-matter
tracker. Its owner-private domain follows the
[personal-memory proposal](personal-assistant.md#later-increments); default cwd
must not become a global personal scope. The automatic publication/blacklist
increment is a prerequisite for new cross-conversation profile learning.

## Identity, observation and visibility

Use a channel identity qualified by account/tenant and authenticated sender;
display names and statements claiming another identity do not qualify. CLI maps
to the configured local owner. Explicit owner links relate verified identities
without rewriting sources. Unlinking stops future cross-identity selection.

An observation needs its subject, Agent, originating audience/project, visibility,
preference kind/value, source receipt/memory revision and evidence timestamps.
Reuse fields and indexes where they fit. Do not key person identity on project,
but evaluate authorization for every selected source. Cross-project use needs an
explicit owner-private preference scope; merely linking a person does not provide it.
Support language, detail and format only, derived from first-person explicit
requests. Assistant replies and third-party statements cannot establish consent
or a preference; quoted/attached text cannot impersonate the sender.

Owner-only profile inspection does not imply that private observations may enter
a shared task. Group application selects only the current sender's same-group,
explicitly shared presentation preference. Other members' observations and all
owner-private fields are excluded before native execution. Keep private profile
inspection tools out of group tasks. This Host selection boundary does not claim
OS isolation from the existing channel execution profile.

Start learning only after the owner enables the pilot; require subject opt-in
for later group persistence. An opt-in starts with subsequent explicit statements,
not a backfill of historical chats. Correction has explicit supersession evidence;
ambiguous conflicting claims are omitted pending clarification. Inferred interests
and stale-habit decay are deferred; explicit preferences remain until superseded,
expired by a configured policy, disabled or deleted.

## Application and shared native history

Select a bounded summary after routing and scope resolution, labeled as attributed
data with evidence references. Current instructions outrank saved presentation
preferences. Preferences cannot override role, audience or permission policy.
For owner tasks, keep profile inspection/source access within current grants.

Group native conversations currently persist across senders. Filtering this
turn's summary alone cannot remove another person's earlier injected summary.
Per-turn instructions must explicitly bind presentation to the authenticated
current sender and disregard previous senders' preferences. Since only same-group
public preferences enter this history, it contains no hidden private profile.
Validate alternating-member behavior with a real native model; do not equate
prompt wording with a guarantee of preference adherence.

On correction, disable, unlink, deletion or opt-out, rotate affected native
sessions before their next task. Handoff must exclude withdrawn profile summaries
and old preference statements that could immediately recreate the deleted view.
Keep exclusion anchors so refresh/retry cannot regenerate observations from the
same evidence. Explicit restoration is separate from a later opt-in. Profile
deletion removes the reusable view; original execution receipts remain governed
by their existing retention policy, so do not claim source erasure.

## Slices and validation

| Slice | Proposed change | Acceptance evidence |
| --- | --- | --- |
| Owner preference view | Shared memory, private domain and local controls | Explicit source, correction, exclusions and opt-out; no passive inference |
| Task application | Bounded assembly with requester binding | Fresh conversation; current override; same requests without profile |
| Account linking | Local verified mapping | Link/unlink, tenant collisions, unchanged source grants |
| Group increment | Same-group opted-in preferences only | Alternating members, resets, injected identity claims and private-source exclusion |

Add deterministic isolation/lifecycle fixtures and separate native preference
adherence examples, covering language, length and output format. Citation checks
inspect supporting records rather than require unsolicited source references in
every brief answer. Confirm real non-owner behavior with a second authenticated
account; synthetic senders do not replace platform acceptance. Run `make check`
and retain failed cases alongside passing experiments. General usefulness, habit
inference and production scale are outside the first acceptance claim.
