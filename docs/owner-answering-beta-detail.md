# Owner Answering Beta: technical notes

Companion to the [main design](owner-answering-beta.md), which owns scope.
The selected-personal-DM path is implemented in source, disabled by default and
not live-accepted. Account-wide discovery, automatic learning, groups and a
question workbench UI remain proposed. This document does not activate a service.

## Implemented path

- `internal/discovery` invokes the opt-in consumer only after saving a complete
  attributed provider window. The answering consumer receives that window directly,
  independently of topic/matter extraction and its cache limits.
- `internal/owneranswering` owns intake watermark, classifier, execution claims,
  latest-history checks, peer delivery and private escalation. It never implements
  a tool/model loop. Classification uses an isolated tool-free native invocation.
- `internal/cli/owner_answering.go` composes the selected Agent through existing
  Host assembly. It preserves the source project, Agent project allowlist,
  knowledge/plugin and Skill selection. It selects a dedicated audience and native
  profile and creates a fresh session for every question execution. Owner USER.md,
  owner bot/CLI sessions, personal-assistance/history mounts and ordinary result
  feedback are omitted. Native instructions reserve chat delivery for Host.
- In-scope handling uses the Agent's existing authorized capabilities and owner
  instructions. The existing Agent channel profile selects `bypassPermissions`;
  this slice does not introduce a narrower native capability profile. The
  configured Agent capability selection still applies;
  scope instructions and the classifier are not a new OS sandbox. Tool output is
  validated from one terminal marked answer/blocked JSON outcome; earlier narrated
  progress stays in the original run evidence. Execution failure or invalid final
  results escalate. Subsequent learning errors are separate question warnings and
  do not turn an otherwise valid successful execution into a handling failure.

## Configuration and commands

Add this **synthetic** block to an otherwise valid private discovery JSON file:

```json
{
  "owner_answering": {
    "enabled": true,
    "agent_id": "operations",
    "instructions": "Handle requests about the configured test service using this Agent's existing authorized Skills/tools. Escalate missing details, unsupported operations or requests outside that service. Never send chat messages yourself.",
    "bindings": [
      {
        "conversation_id": "fixture-dm",
        "peer_open_dingtalk_id": "fixture-peer-open",
        "owner": { "id": "fixture-owner-open", "sender_type": "", "verified_contact": true },
        "peer": { "id": "fixture-peer-open", "sender_type": "", "verified_contact": true }
      }
    ]
  }
}
```

The discovery file must also select exactly that DM source and its registered
project, specify `profile` as the configured bot owner's `corpId:userId`, and
set `dingtalk_workspace_config` to an embedded owner workspace with `chat` enabled.
Existing collection limits remain: 1–16 explicit sources, polling at least every
30 seconds, bounded pages/messages and registered non-default project mappings.
The selected Agent must permit the source project. Omission or `enabled:false`
disables answering. Standalone `agent-host discovery check|collect|serve` rejects
enabled answering; only the DingTalk channel service wires the consumer through
its existing `--discovery-config PATH` option. No extra listener/provider is added.

Bindings are owner-verified activation prerequisites. Observe the exact author
IDs and sender types in the selected provider history; do not infer them from
names or assume that a personal open ID equals a staff ID. IDs must be distinct,
and targets must match selected DM sources. A nonempty configured type must match
exactly. For history that omits `senderType`, first independently verify each
human's stable contact ID against contact lookup and the selected history. Then
explicitly set `sender_type:""` and `verified_contact:true` for that author, as in
the synthetic example. Omitting the configuration field or setting it to null
does not grant this exception. An explicit conflicting provider type still blocks
work. Display names and “not the owner” are never identity evidence. Nonempty
attributed human text with absent `messageType` reaches classification; forwarded
and automation/unknown identities remain excluded. Unknown identities/types are retained as explicit
`blocked_identity` source reasons, without execution or a guessed recipient.
Source content cannot create or extend a binding. A changed discovery policy
stops further work until restart; reactivation with a changed policy starts a new
current-time watermark. Existing unresolved work remains linked and cannot gain a
fresh execution/reply slot through a policy change. Disabling configuration is
checked before effects/sends. Each evaluation, including lock wait and owner
clarification, uses discovery `timeout_seconds`. Cancellation retains a waiting
owner outcome through a separate bounded finish context and prevents peer send.
Notification/finalization contexts are independently bounded to 20 seconds.

Owner clarification uses the private bot conversation:

```text
/answer QUESTION_ID.REVISION clarification text
/dismiss QUESTION_ID.REVISION
```

The notification supplies the exact reference. Only authenticated configured-owner
private messages in the same bot account/tenant and owner audience reach this
callback, before ordinary native dispatch. Other text remains normal bot work.
Unknown, stale, closed or malformed references produce an owner-only explanation;
no recency or quoted source text substitutes for an explicit current reference.
Clarification increments the revision and reevaluates support/scope and history.
Before any tool attempt it can resume normal handling. After tools were attempted,
it uses a separate isolated tool-free draft with the saved handler outcome, original
run evidence and explicit owner answer; it cannot rerun Handle or resume its native
session. Unsupported drafts remain private. A pending/unknown/attempted peer send
cannot be replayed by clarification. Dismissing an in-flight delivery cannot recall
it; read-only reconciliation continues and no replacement send is created.

The configured handler backend is preserved, including ACP. Its final output is:

```text
<agh-owner-answering-outcome>
{"outcome":"answer","text":"Supported answer","reason":"","clarification":""}
</agh-owner-answering-outcome>
```

Markers occupy their own lines. Parse exactly one terminal block with strict JSON
fields; earlier commentary is permitted, but missing/multiple/malformed blocks or
trailing non-whitespace are rejected. `outcome` is `answer` or `blocked`. No ACP
JSONSchema request or backend fallback is introduced. Classifier and clarification
drafting are explicitly separate isolated tool-free native tasks.

## Durable state and delivery

State migration 16 → 17 adds `owner_answering` records with compare-and-save
revisions. It preserves existing runs and channel state. Source records retain
the policy fingerprint, fixed `activation_baseline`, moving collection `through`,
active question link and identity blocker. Existing records without a baseline
migrate conservatively to their old `through` boundary. A new activation policy
pins its boundary at now; normal polling never moves that baseline. Stable source
and provider-message IDs key per-message digest observation records in the existing
KV table. Source, observations and question revision commit atomically before
classification or tool execution; no schema change is needed for these fields.
Question records retain exact attributed evidence, question/revision, source,
execution claim/run ID, clarification, state, answer and separate delivery receipts.
These are execution/control records, not a second memory store.

First activation records now before polling; historical imports never trigger
handling. The moving `through` cursor never suppresses a previously unseen message:
late messages after the fixed baseline are intaken once, even when their creation
time is older than `through`. Existing assigned-message anti-replay remains in
place. A question ID is anchored to the first new provider message. Subsequent
peer details merge into that same outstanding question/revision without resetting
the execution claim or creating another send slot. Only after a truly terminal
question may a later message start a new question. Pending/unknown delivery remains
active across restarts and expanded overlapping polls. The
classifier ignores greetings/nonrequests, handles complete in-scope requests and
asks the owner about unsupported, incomplete or ambiguous multi-question bursts.
Unrelated requests mixed into an outstanding question require owner clarification;
more advanced topic-aware splitting remains proposed.

The execution claim is saved before Host/tool execution. Restart converts an
interrupted execution or unsent completed draft into waiting-for-owner, without
rerunning tools. A pending unclaimed question can be reconsidered. Failure never
becomes a peer placeholder/progress reply.
Message membership is retained independently of the source cursor, so overlapping
or changed collection bursts cannot package an already assigned message as a new
execution. Unresolved prior effects in the conversation block further automatic
tool attempts. Authenticated owner clarification may finish via tool-free drafting.

Before tools and again immediately before peer delivery, fetch fresh complete
two-way history through the current decision time. Missing/partial history,
unbound authors, changed/omitted peer evidence, or policy/target drift block the
answer. Observed owner activity cancels automatic peer delivery. Known saved AI
message IDs are exempt from takeover only in their bound conversation. History
read and personal send are not transactional; a manual owner send can race after
the final read. Live acceptance must measure this remaining collision window.

Host processing and owner activity have independent evidence. `Attempted`, `RunID`,
`HandlerOutcome` and `Peer` describe actual agh execution/delivery only. The
question's `owner_activity` records the provider message/evidence IDs, source time
and `outgoing_message` type for bound owner-authored messages. No entry means
activity is unknown, not proof the owner has not seen or handled the question.
Actual outgoing owner messages set takeover but never fabricate an agh attempt,
run, handled result or send; observed activity also does not establish resolution.
Owner activity discovered by intake or the final fresh-history check retains this
evidence while preserving any existing agh run outcome. Previously observed owner
messages are still considered for takeover in relevant two-way history. No
read/unread, read-receipt or viewing metadata filters participate in intake,
deduplication, handling completion or takeover decisions.

Peer replies use `dingtalkworkspace.Service.send_message`, a stable `oa-QUESTION`
operation ID, original conversation target and visible AI marker. Preserve the
official platform AI marking. Personal transport receipts remain accepted,
provider-pending, failed or unknown; acceptance does not prove recipient reading
or question resolution. An `openTaskId` remains provider-pending, even when the
send command itself reports successful completion. Each complete collection cycle
can read `get_send_status` for the active task, including after restart. Only
provider `SUCCESS` with a real message ID confirms delivery and grants AI echo
suppression. Original operation and latest status receipts remain separate; task
and returned conversation IDs are checked. Failed/unknown status stays visible,
never triggers a new send, and unknown status can only be reconciled read-only.

Blocked handling reserves one private bot escalation before transport. It goes
only to the verified configured owner and includes peer identity, a bounded cited
source excerpt, reason, needed clarification and exact command reference. The full
source remains in the question record. Repeated polls/unchanged waiting questions
do not notify again. Peer and escalation claims/receipts stay independent.
Notification failure remains a failed/unknown private receipt; the peer stays
silent. Restart converts reserved delivery into unknown, without replay. This
slice does not automatically retry failed notifications either. Follow-up details
and unchanged blockers retain the prior escalation receipt instead of renotifying;
stale owner references receive the current exact reference privately.

## Proposed learning, freshness and feedback

Separate style candidates from factual/procedural knowledge. Style is scoped by
language, audience and context; repeated examples and explicit owner corrections
outweigh one-off phrasing. Tone must not copy names, private facts or old promises.
Learned style uses versioned preference records and a derived bounded summary;
never overwrite user-maintained configuration or `USER.md` automatically.

Solutions retain question conditions, owner/peer authorship, proposed steps,
observed outcomes, contradictions and source revisions. An explicit “worked”
reply is a reported outcome; silence or a thank-you alone does not prove all steps.
Uncertain authorship and unresolved conflicts withhold promotion. Corrections and
source withdrawal revoke dependent answer versions before subsequent sends.

Join known AI replies to the send ledger before learning. They may provide episode
context, but are excluded as owner-style examples and as independent success
support. An owner's explicit correction can teach; it does not retroactively turn
an AI answer into a human-authored solution. Keep replay suppression and disabled
answer exclusions across reimports. Preserve original evidence and review reasons.

Learning access and disclosure permission are separate: privately observed content
can remain useful for owner-only assistance without entering recipient-visible
answers. Retain only authorized sources, with a configurable raw-history retention
policy; source expiry makes dependent answers unavailable unless independently
supported. The owner can inspect and withdraw learned style and solution entries.

## Rollout and tests

Focused headless checks use `go test ./internal/owneranswering ./internal/discovery
./internal/state` and the `TestOwnerAnswering*` / `TestOwnerPrivateCommand*` cases
in `internal/cli` and `internal/channel`. The composed Host tool test requires the
existing localhost run-control listener; sandbox denial of that listener is a
validation limitation, not permission to bypass it. Supervisor runs `make check`.
These fixture checks do not establish real sender bindings, owner delegation,
native tool success or DingTalk delivery. The following are rollout acceptance
requirements, including future learning; they are not claims of deployed controls.

1. **Capability probe:** selected test contact, verified owner/recipient IDs,
   inbound and outbound history, latest-window completeness, real AI marking,
   personal send/status/readback correlation, private bot notification acceptance
   and owner reply correlation. Validate auth loss and rate-limit behavior.
2. **Historical replay:** use temporal splits and held-out conversations. At each
   replay point expose only earlier evidence and the then-valid answer versions.
   Later owner answers are evaluation labels, not retrieval input. Include complex,
   incomplete and non-question messages as well as easy FAQs.
3. **Shadow pilot:** no external sends. Owner labels intended answer/wait/ignore,
   proposed answer/escalation quality, missing details and potential disclosure. Validate
   takeover timing against live two-way history. Unlabeled decisions remain unknown.
4. **Restricted live pilot:** activate selected chats and reviewed handling scope,
   deduplicated private owner escalations and operational volume monitoring.
   Disable the configuration to pause further effects. Expand only after
   reviewing the acceptance report; do not optimize reply count at the cost of errors.

Mandatory cases: fragmented question then update; owner replies during analysis
and just before send; identical redelivery; reordered messages; unrelated group
traffic; bot/AI echo; ambiguous recipient; private-source leakage; stale solution;
prompt injection; missing/partial history; restart at send reservation; async
failure/unknown outcome; policy withdrawal; corrected solution; multiple questions;
peer asks for a human; old backlog after restart; AI output re-entering learning.

Escalation cases: unmatched/incomplete/unhandleable request produces zero peer
messages and one private owner escalation containing sender, source, reason and
clarification request; greetings/nonrequests produce neither; repeated polls and
unchanged revisions do not renotify; material blocker updates are versioned;
missing owner binding fails closed; bot failure/unknown acceptance leaves the peer
silent; restart before/after notification reservation avoids replay; concurrent
questions and ambiguous/stale/wrong-account replies cannot cross-associate;
valid clarification triggers fresh evaluation rather than verbatim forwarding;
owner takeover during clarification prevents peer delivery; owner-private content
is not automatically disclosed. Verify automatic selected-DM intake without manual
forwarding and report unseen new contacts as coverage gaps, not successful intake.
Tools/Skills are included in the selected scope; their actual permissions and
effect evidence need live acceptance. If bot-DM intake is added later, add its
distinct routing cases before that expansion is accepted.

Report auto-answer precision (correct sends / reviewed sends), eligible-question
coverage, useful silent waits, missed safe answers, owner corrections, duplicate
or collision rate, escalation delivery/duplication, clarification association and
usefulness, coverage gaps, latency and analysis cost. Keep denominators, sample sizes,
unlabeled cases and collection gaps. Proposed promotion target: at least 99%
correctness across at least 100 reviewed auto-answer candidates covering the
enabled topics, with all critical boundary tests passing and zero critical
incidents. Low traffic extends observation rather than inventing a passing sample. A small perfect pilot does not prove safety
at account scale. Critical incidents pause sending while diagnosis proceeds.

Run `make check` for each implementation slice. Offline tests, native model checks,
shadow evidence and real delivery acceptance are separate gates. Deploy a verified
build into the independent production runtime only after inspecting its migration
receipt and recording backup/recovery steps; development tests use isolated state
and test identities. No production activation follows from this design document.
