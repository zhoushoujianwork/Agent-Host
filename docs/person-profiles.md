# Per-person reply preferences

Status: proposed on 2026-10-13 for [issue #1](https://github.com/zhoushoujianwork/Agent-Host/issues/1).
Current memory preserves statement attribution, but a cross-channel person-profile
layer is not delivered. See the [implementation notes](person-profiles-detail.md)
and [delivery order](release-follow-up.md#issue-1-delivery-plan).

## Goal and usage

Remember how a person wants the assistant to reply without making them repeat
the preference in every conversation. Start with explicit language, detail and
format requests, such as “Use short English answers.” Apply them only to that
person's requests; a current request can override an earlier preference.

The owner can inspect each preference and its source, correct it, disable it or
delete its profile observation. The assistant does not claim a preference was
saved until the Host records that outcome. Ordinary replies need no profile IDs;
source citations are available when the person asks why a preference was applied.

## Core approach

Build a small derived view over source-linked preference memory. Keep source
evidence and publication under the existing memory policy, rather than creating
a second autonomous learning system. A preference changes reply presentation;
it grants no execution authority, project access or external action.

Identify people through authenticated channel identities. DingTalk, Feishu and
CLI identities stay separate unless the owner explicitly links verified accounts.
Linking identities does not merge private and group visibility or project grants.

Start with an opt-in owner pilot. Later group support uses only that requester's
explicit preferences shared in the same group and opted into for later use.
Owner-private preferences never enter group prompts. Because group native history
is shared, even a harmless presentation preference must be safe for that audience.

## First scope and order

1. Add inspectable, correctable owner-private preference observations with source
   links, opt-in controls and the shared publication/disable policy.
2. Apply a bounded summary in fresh owner conversations. Validate language,
   length and format against the same requests without personalization.
3. Add explicit cross-channel identity links while preserving each source's
   original visibility and authorization.
4. Add opted-in group-member preferences and verify alternating senders and
   fresh/reset conversations before declaring the full issue complete.

Recurring interests, inferred habits, task prediction, rich contact profiles and
background refresh are later work. Explicit preferences need no periodic collector.
Non-owner private channels require their own channel scope and are not enabled
by this proposal. Dates and commands will be chosen during implementation.

## Acceptance

- A fresh conversation applies the requester's published preference, exposes its
  source on inspection, and respects a conflicting current request.
- Account names cannot merge identities. Explicit linking never broadens source
  access, and one member's preference does not govern another member's reply.
- Group tasks receive no private profile; alternating senders in shared native
  history retain separate preferences.
- Correction, disable, deletion and opt-out affect later tasks without silent
  re-creation from old evidence. Automated and live checks remain distinct.
