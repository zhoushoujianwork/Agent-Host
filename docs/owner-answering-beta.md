# Owner Answering Beta / Owner 代答 BETA

Status: proposed on 2026-10-16, revised on 2026-10-26; not implemented or deployed. This design extends
[personal assistance](personal-assistant.md) and [DingTalk workspace](dingtalk-workspace.md).
This main document owns scope; [technical notes](owner-answering-beta-detail.md)
cover state, correlation, delivery and validation.

## Goal

Automatically detect and handle incoming personal DingTalk DM questions addressed
to the owner, without asking the owner to forward them to agh. Send supported
answers to the original peer. When a request is unmatched, incomplete or cannot
be handled, keep silent to the peer and privately ask the verified owner for
clarification through the application bot. Ignore greetings and other nonrequests.

The desired coverage includes new contacts across the owner's account. DWS /
DingTalk Workspace（钉钉个人工作空间）provides personal-chat access separately
from the application bot, but the existing integration only polls selected
conversations. Account-wide new-contact coverage is unimplemented and needs
source discovery/event integration; selected-source polling does not deliver it.
Private chats come first; groups require separate validation.

## Expected behavior

| Situation | Proposed behavior |
| --- | --- |
| Complete question with a supported, current, shareable answer | Reply to the original peer with the answer and a short “AI assistant replying for Owner / AI 代 Owner 回复” marker, preserving platform AI marking. |
| Fragmented or incomplete question | Collect the message burst; if still incomplete, send no peer placeholder, clarification question or progress message. Privately escalate to the owner. |
| Unmatched question, diagnosis, judgment or unsupported action | Send nothing to the peer; privately escalate to the owner with the sender, source question, blocking reason and a concise clarification request. |
| Owner clarification in the private bot conversation | Associate it with the exact question and revision, then evaluate again. Do not blindly forward the owner's text to the peer. |
| Owner takes over in the original conversation, or the peer asks for the owner | Stop automatic peer sends for that question. |
| Greeting, acknowledgement or other nonrequest | Ignore; no automatic answer or escalation. |
| Peer delivery is uncertain | Suppress further peer sends, retain the evidence and privately escalate the uncertainty. |

“Silent” applies to the peer. **Private bot escalation to the verified owner is
required for blocked requests**, replacing the earlier no-routine-notification
policy. Deduplicate escalations so repeated polls or unchanged analysis do not
repeatedly notify the owner. The owner can inspect the evidence and optional draft,
clarify, dismiss or take over. A timer cannot turn waiting into permission to send.

## Core approach

Host tracks related messages as one question, classifies the request and checks
the available support and delegation. Supported answers go through a fresh
conversation check before delivery to the original peer. Blocked requests enter
private owner clarification; a correlated response resumes evaluation against the
latest evidence and permissions. Owner takeover cancels pending peer replies.

For the existing proposed **answer-only beta**, every automatic answer must:

1. Address an eligible question in an enabled conversation.
2. Match an approved answer's conditions with complete required details.
3. Be current, supported and shareable with the original recipient.
4. Need no new diagnosis, consequential commitment or business operation.
5. Pass a fresh check for takeover, conflicting messages and prior delivery.

The model proposes; Host validates and owns sending. Delegation is an owner policy,
not permission supplied by an incoming message or model confidence. The approved
answer set is an initial beta mechanism, not a decision to limit the requested
product permanently to FAQs. Expanded handling using authorized tools or Skills
remains an open scope decision; this proposal does not yet authorize execution.

## Learning and owner control

Learn communication style and source-linked solutions separately from selected
owner conversations. Keep observed content private; only explicitly approved,
recipient-appropriate answers enter the beta answer set. Preserve the distinction
between a suggested solution and an observed successful outcome. AI answers are
not owner teaching examples. Corrections withdraw affected answers.

Observation, learning and automatic-reply scopes are separate. Historical imports
support learning and evaluation, never replies to old questions. The owner can
pause, exclude a conversation, disable an answer and inspect sent, waiting or
failed items. Existing bot requests retain their current path until scope changes
are agreed.

## Open scope decisions

Two scope decisions remain before implementation:

- Start with personal DingTalk DMs only, or also include incoming bot DMs?
- Keep initial handling answer-only, or include execution through explicitly
  authorized tools/Skills? Execution permissions and acceptance need definition
  before implementation of that expanded scope.

## Delivery order

1. Resolve scope and verify owner/peer identity, two-way history, coverage and
   both personal reply and private bot escalation capabilities.
2. Run shadow evaluation without external sends, including ignored and blocked
   requests and correlated owner clarification.
3. Pilot selected private conversations with approved answers and deduplicated
   owner escalations; stop peer automation on takeover.
4. Expand coverage through verified source discovery/event integration and
   evidence. Validate groups separately before enabling them.

Release requires zero wrong-recipient, unauthorized-disclosure, duplicate-send or
observed-takeover violations in the agreed acceptance set. Measure answer quality,
escalation usefulness and owner corrections before expansion. The linked
[test plan](owner-answering-beta-detail.md#rollout-and-tests) separates offline
checks, shadow evidence and live acceptance. No activation follows from this design.
