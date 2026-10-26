# Owner Answering Beta / Owner 代答 BETA

Status: selected-personal-DM source implementation on 2026-10-26; disabled by
default, not deployed or live-accepted. Broader coverage and learning remain proposed.
This feature extends
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

| Situation | Selected-DM behavior |
| --- | --- |
| Complete question with a supported, current, shareable answer | Reply to the original peer with the answer and a short “AI assistant replying for Owner / AI 代 Owner 回复” marker, preserving platform AI marking. |
| Fragmented or incomplete question | Collect the message burst; if still incomplete, send no peer placeholder, clarification question or progress message. Privately escalate to the owner. |
| Complete request within delegated scope | Use the selected Agent's existing authorized Skills/tools, then reply with the supported result. |
| Unmatched question, missing information, unsupported action or handling failure | Send nothing to the peer; privately escalate to the owner with the sender, source question, blocking reason and a concise clarification request. |
| Owner clarification in the private bot conversation | Associate it with the exact question and revision, then evaluate again. After tools were attempted, draft from saved evidence and the owner's answer without running tools again. Do not blindly forward the owner's text. |
| Owner takes over in the original conversation, or the peer asks for the owner | Stop automatic peer sends for that question. |
| Greeting, acknowledgement or other nonrequest | Ignore; no automatic answer or escalation. |
| Peer delivery is pending | Read its provider status; keep the question active until delivery is confirmed. Never create another send slot. |
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
One outstanding question per conversation retains subsequent peer details. New
message bursts cannot bypass interrupted work or a pending/unknown delivery.
Host observation/handling and owner activity are independent. An incoming message
is eligible even if already read. Read receipts or owner viewing never mean agh
processed it or that the owner took over. An actual owner outgoing message is
recorded as manual activity and stops automatic replies; it does not prove the
problem was resolved or manufacture an agh execution/delivery result.

The owner selected **personal DingTalk DMs first, including existing authorized
Skills/tools**. Every automatic handling attempt must:

1. Address an eligible question in an enabled conversation.
2. Match the owner-controlled handling scope with complete required details.
3. Be current, supported and shareable with the original recipient.
4. Stay within the configured Agent's project, knowledge and capability selection.
5. Pass a fresh check for takeover, conflicting messages and prior delivery.

Native execution uses a fresh question session in a dedicated audience, without
the owner's bot/CLI session or private profile. The classifier is tool-free;
the handler reuses Host and the selected Agent's configured capabilities. The
owner's explicit scope grants delegation; incoming messages cannot extend it.
Host owns peer delivery and private escalation. This is broader than the original
answer-only/FAQ proposal. An interrupted tool attempt or uncertain send is never
automatically replayed, including after owner clarification.
Successful execution remains successful if later learning fails; the learning
failure is retained separately as a warning.

## Learning and owner control

Proposed learning will learn communication style and source-linked solutions separately from selected
owner conversations. Keep observed content private; only explicitly approved,
recipient-appropriate answers enter the beta answer set. Preserve the distinction
between a suggested solution and an observed successful outcome. AI answers are
not owner teaching examples. Corrections withdraw affected answers.

Observation, learning and automatic-reply scopes are separate. Historical imports
support learning and evaluation, never replies to old questions. The owner can
disable answering in configuration or remove a binding; configuration changes
stop further work and require restart to reactivate. Explicit private bot commands
clarify or dismiss a question. Existing bot requests retain their current path.
A dedicated question workbench UI remains proposed.

## Activation and limits

Set the optional `owner_answering` block in discovery configuration with an Agent,
owner-controlled handling instructions and verified sender bindings for selected
DM sources. Match the exact observed ID and type. If the provider omits types,
explicitly configure that absence only after independently verifying the contact
ID. Unknown identities or conflicting types block work. See the [configuration example and
commands](owner-answering-beta-detail.md#configuration-and-commands).

Only the DingTalk channel service wires this consumer; standalone discovery rejects
enabled answering. Activation pins a fixed historical boundary; durable message
observations deduplicate later collection windows. Previously unseen messages after
that boundary remain eligible even when returned late behind the moving collection
watermark. Bot DMs remain ordinary bot work; they are not new
automatic personal questions. New contacts require explicit source/binding setup.

## Delivery order

1. Verify owner/peer identity and sender types, two-way history, coverage and
   both personal reply and private bot escalation capabilities.
2. Run shadow evaluation without external sends, including ignored and blocked
   requests and correlated owner clarification.
3. Pilot selected private conversations with authorized handling scope and deduplicated
   owner escalations; stop peer automation on takeover.
4. Expand coverage through verified source discovery/event integration and
   evidence. Validate groups separately before enabling them.

Release requires zero wrong-recipient, unauthorized-disclosure, duplicate-send or
observed-takeover violations in the agreed acceptance set. Measure answer quality,
escalation usefulness and owner corrections before expansion. The linked
[test plan](owner-answering-beta-detail.md#rollout-and-tests) separates offline
checks, shadow evidence and live acceptance. No activation follows from this design.
