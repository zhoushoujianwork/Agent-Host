# Owner Answering Beta: technical notes

Companion to the [main design](owner-answering-beta.md), which owns scope and
rollout choices. Proposed only; no automatic owner-answering service is delivered.
Keep exact schemas, command names and tuning values for implementation.

## Existing foundation and gaps

| Foundation | Reuse and limitation |
| --- | --- |
| `internal/discovery` | Selected-source polling, message revisions, coverage/checkpoints and immutable evidence. It does not discover and monitor every account conversation. |
| `internal/assistance` | Matter reconciliation, pinned citations and stale-revision rejection. Current preparation is owner-selected and does not authorize automatic replies. |
| `internal/dingtalkworkspace` | Pinned owner identity, bounded personal-history reads, personal sending, operation ledger and platform-status queries. A saved send operation is not yet a tracked question or delegation policy. |
| Bot notifications and task feedback | Verified-user private bot delivery and queued task-result receipts exist. Blocked-question escalation and owner-clarification correlation are not implemented. |
| File memory and experience | Publication, source attribution, correction and exclusion. Conversation-derived solutions need their own evidence path; never manufacture a successful Host run. |
| Channel authorization | Authenticated sender and execution separation. A peer's question must not become an owner-privileged native task. |

The pinned integration does not expose personal event subscriptions. Its current
history decoder retains sender/thread/quote information, but group @owner targeting
needs capability verification and may require adapter work. The documented event
contract excludes the owner's own sent messages; incoming events alone cannot
establish human takeover or supply owner solutions. Verify this against the actual
provider during implementation. Polling remains required for two-way reconciliation.

Personal task reads are currently bounded to 24 hours and 1–20 messages. The
collector has its own configured pagination and coverage ledger. Current discovery
configuration allows 1–16 explicit sources, requires registered project mappings,
and enforces a polling interval of at least 30 seconds (`internal/discovery/config.go`).
An owner-wide or faster service therefore needs deliberate configuration and
scheduling work, including rate-limit acceptance. Do not implement
an account-wide service by repeatedly pretending a partial single page is complete.
Source links: [DWS integration](dingtalk-workspace-detail.md),
[selected-source intake](knowledge-sources-detail.md#dws-observation-and-episode-intake),
[current matters](personal-assistant-detail.md#p0-implementation).

## Two independent loops

**Online answering:** acquire evidence → update a question → classify intent and
completeness → retrieve permitted answer entries → draft → validate → recheck
latest conversation → claim and send → reconcile delivery.

An unmatched, incomplete or unhandleable request branches into private bot
escalation to the verified owner, with no outgoing peer message. Correlated owner
clarification updates the question revision and returns to evaluation; it does
not skip support, recipient, permission, takeover or delivery checks. Greetings
and nonrequests end at classification without either delivery path.

**Offline learning:** reconcile both authors' messages → form source-linked
problem episodes → extract style and solution candidates independently → compare
existing knowledge → publish eligible knowledge under Host policy → promote a
version into the automatic answer set after beta review.

The online loop must not wait for learning. Learned corrections can invalidate
pending drafts, but learning jobs do not send messages. Independent queues and
bounded work budgets keep history imports from delaying owner bot requests.
Reuse native tool-free analysis and narrow read capabilities; Host remains the
business coordinator rather than implementing another model/tool loop.

## Coverage, targeting and question identity

- Bind every source to the exact authorized owner account and conversation.
  Stable provider identity and revision deduplicate overlapping polls/events.
- New direct-chat requests to the owner may be candidates. Greetings, receipts,
  advertisements, FYI messages, forwarded questions and the owner's own messages
  are not automatically requests to answer. A question need not contain “?”.
- Exclude the configured bot and known automation conversations to avoid loops.
  Preserve bot-request routing; do not answer the same platform message twice
  through bot and owner paths. Cross-path deduplication needs stable IDs, not text.
- After the private pilot, group eligibility requires a verified owner mention,
  direct reply or unambiguous owner-directed continuation. Ordinary group traffic
  stays ineligible. Group identity and addressing must pass live acceptance first.
- Group related fragments using thread/reply identity and supported topic context.
  Use a short configurable settling period; a pause in typing does not establish
  completeness. Interleaved questions retain different identities.
- Each question retains its triggering messages, latest evidence revision,
  analysis basis, answer-set version, recipient and delegation revision.
- New information invalidates unsent drafts. Edits/deletions invalidate affected
  evidence when reported by the provider; do not claim unobserved deletion coverage.
- Startup/import establishes a live-activation watermark. Older questions are
  learning/evaluation data. Downtime backlog requires fresh revalidation and a
  bounded relevance window, rather than a flood of delayed answers.

“All questions” is a coverage goal. Beta reports selected conversations, gaps and
freshness honestly. Account-wide discovery, event durability, encrypted payloads
and rate limits require provider-specific acceptance before expanding coverage.
Automatic intake must not depend on manual forwarding to agh. Detecting questions
from newly contacting peers across the account requires source discovery/event
integration that is not delivered by the current selected-source collector.
The main document leaves incoming bot-DM inclusion open; until resolved, preserve
ordinary bot routing and keep escalation replies out of personal-question intake.

## Answer and permission boundary

These boundaries describe the existing proposed answer-only beta. They do not
settle the main document's open decision about authorized tools/Skills execution.
Implementing that expansion requires explicit execution scope and acceptance;
neither incoming peer text nor an owner clarification silently enables tools.

Delegation is an owner-controlled policy: enabled conversations, answer topics,
recipient grants, effective time and send limits. Its version binds each decision.
A model cannot create or extend it. Disabling it revokes pending send eligibility.

The answer set is a reviewed projection of versioned memory or approved source
answers, with pinned revisions and reply policy. Reuse the file-memory foundation;
do not create a second independent knowledge store. Keep episode evidence and
question/delivery state separate from that reusable knowledge.

Project-associated lessons retain their project scope. Projectless personal
questions require an explicit owner-private scope with tested recipient grants;
the default execution workspace must not become a global memory bucket. This
scope integration is implementation work, not an existing P0 guarantee.

A draft can use the current authorized conversation and a recipient-permitted
answer set. General owner-private memory and other chats are not exposed to the
responder merely because DWS runs under the owner's account. Knowledge reused
across chats needs explicit publication into an appropriately shareable answer.

An answer entry retains its source revision, applicability conditions, required
question details, intended recipients, validity/expiry and excluded situations.
Owner statements can support a factual FAQ; procedures additionally retain whether
steps were merely suggested, reported successful, or directly observed. One old
solution is insufficient evidence for a new environment or incident.

The first answer set contains stable links, definitions and low-risk documented
instructions. Requests involving approvals, spending, access changes, production
operations, sensitive personal matters or promises stay for the owner. “Simple”
means the supported answer requires no such judgment or action, not a short prompt.

Use a dedicated responder profile and native-session partition for the authorized
conversation. Never resume the owner's ordinary bot/CLI session or another peer's
session: earlier private context can survive even when the current prompt is filtered.
The responder receives no generic shell, business-write or unrestricted DWS tools.
Host alone can issue one constrained send to the bound recipient with the validated
content. The permission to answer a question does not delegate the owner's other
capabilities. Incoming messages and retrieved documents remain untrusted content.

An answer uses the selected style but retains AI identity. Preserve the official
platform marking, and add the owner's chosen short visible marker. Do not claim
“I checked” without an actual check or “I will do it” for an owner commitment.

## Silent waiting and owner takeover

Proposed states express behavior, not a final database schema:

| State | Meaning and allowed next step |
| --- | --- |
| Collecting | Wait for a message burst and complete relevant coverage. |
| Analyzing | Evaluate one evidence/policy revision; newer input supersedes it. |
| Waiting for details | No peer message; persist a private escalation obligation and reconsider on relevant new evidence. |
| Waiting for owner | No peer message; privately request clarification and retain reasons/draft. Correlated clarification or material new evidence resumes evaluation. |
| Ready to send | Eligible draft, subject to final latest-history/policy checks. |
| Sending / pending | One reserved operation; inspect its platform status. |
| Replied | Delivered reply linked to the question; follow-ups update the episode. |
| Human handled / closed | Cancel outstanding automation; no timed reopening. |
| Delivery unknown | Suppress further sends for this question until reconciled. |

Any observed owner-authored message cancels pending replies in that conversation
for the pilot, even if topic association is uncertain. Distinguish Host-generated
owner-account messages by saved operation/message IDs. Other owner-account
messages cause takeover conservatively; do not assume they are human teaching
examples if they may come from another automation.

A separate clearly new question may be eligible after the conversation settles.
A peer request for human attention or an explicit owner pause persists for the
current question; a timer cannot override it. The owner can dismiss, take over,
resume or edit a private draft. Required blocked-request escalation replaces the
previous optional-notification policy; unrelated routine review remains optional.

## Private escalation and clarification

Persist question state and its escalation obligation together. Bind each item to
the exact owner account, verified private bot recipient, source channel/account,
conversation, peer identity, question ID and evidence revision. Never choose the
original peer as the escalation recipient or resolve the owner from a display name.
Notify through the application bot, separately from personal DWS peer replies.
Include the sender's display name and stable identity, the attributed source
question or bounded excerpt with source reference, why handling is blocked, and
one concise request for the missing information or decision. Do not include
unrelated private history. Retain the complete authorized source privately.

Use a durable escalation slot keyed by owner and question with a blocking-reason
fingerprint and associated question revision. Repeated polls, message redelivery,
unchanged analysis and restart reuse that slot and never create another send.
A new evidence revision alone is not a reason to notify again: only a material
change in the blocker or information needed may create a new escalation revision.
Keep the two delivery ledgers separate so an owner notification cannot be mistaken
for a peer answer. Reserve the notification attempt before sending and retain its
content digest, provider receipt and accepted/failed/unknown outcome. Provider
acceptance does not prove owner reading or question resolution.

Definite notification failure remains visible as an undelivered escalation in the
owner workbench; it never causes a peer placeholder or progress message. Allow only
bounded retries of confirmed failures under an explicit retry policy. Unknown or
interrupted attempts require reconciliation before any retry; do not issue a fresh
slot to bypass uncertainty. After restart, recover pending obligations and inspect
reserved attempts instead of replaying sends. Missing owner binding or bot access
blocks escalation visibly and leaves the peer silent.

Give each escalation a durable question/revision reference. Match an owner response
using a verified quote/reply identity or explicit reference within the same bound
private bot account, conversation and authenticated owner identity. Plain text or
recency alone cannot select among pending questions. An ambiguous, stale or
unmatched response stays private; ask the owner to select the question or confirm
the current revision. Never guess an association or forward it to a peer.

A valid clarification is additional attributed evidence: increment the question
revision, invalidate any old draft and evaluate the latest conversation, answer
support, shareability, delegation and send state again. Owner-private clarification
is not automatically recipient-shareable or an approved reusable answer. An owner
response in the escalation bot conversation is distinct from takeover in the
original peer conversation. Takeover, dismissal or pause prevents automatic peer
sends; clarification alone cannot reopen a human-handled question without explicit
owner release. Recheck for takeover before sending even after valid clarification.

## Sending, recovery and the unavoidable race

Before reserving a send, obtain fresh two-way history through the decision point.
If coverage is incomplete, stale or unavailable, retain a wait reason. Check
owner activity, newer peer messages, revoked answers and prior sends again.
Revalidate delegation and question revision atomically when reserving the local
send operation. Serialize sends per owner conversation.

Use a stable operation identity derived from owner, conversation, question and
reply slot, binding content to its digest. Retrying the same analysis must not
produce a new send key. Corrected drafts may replace an unsent slot; a reserved,
pending or unknown send cannot be bypassed with a new key. Use the existing
workspace ledger and official idempotency key; retain async task and message IDs.
The current send API has no validated reply-to field: initial private replies use
the conversation target and retain their question linkage locally. Do not invent
platform threading support; group rollout must validate addressing separately.

DingTalk history reads and manual owner sending are not one transaction. The
owner can send after the final read but before AI delivery. Minimize that interval,
use observed takeover cancellation where available, measure collisions in the
pilot, and never promise absolute exclusion. If a collision is observed, stop
further automation for the episode and expose it privately; do not automatically
recall or send an apology message without an applicable policy.

Provider acceptance, confirmed delivery, recipient reading and question resolution
are different outcomes. Unknown delivery is inspected without automatic replay.
Global pause cancels unsent work; it cannot undo an already dispatched message.

## Learning, freshness and feedback

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
4. **Restricted live pilot:** activate selected chats and reviewed answer entries,
   deduplicated private owner escalations, a low configurable volume cap and a
   global pause control. Expand only after
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
If bot DMs or tools/Skills are selected later, add their distinct routing and
execution-authorization acceptance cases before implementation is accepted.

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
