# Release follow-up

Remaining work from the [delivered daily-use release](release-daily-use.md).
This is a backlog, not a commitment to add every item to the next release.
See the [follow-up notes](release-follow-up-detail.md) for validation boundaries.

## Next checks

1. **Real non-owner group use.** Use a second authenticated account to verify
   ordinary @bot work and follow-ups that keep different members distinct.
2. **Daily-use observation.** Observe owner use for a week and record failures,
   continuity problems and memory outcomes using existing records.
3. **Database wait diagnosis.** Find the transient event-write wait's cause.
   Its coupling to business cancellation is fixed; the original cause is unknown.

## P0: contextual assistance and accessible memory

**Design approved; ready for development. Implementation has not started.**
This is a core memory increment in the release backlog; no release version or
date is assigned. The [main design](personal-assistant.md#p0-ready-for-development)
owns scope; [acceptance notes](release-follow-up-detail.md#p0-acceptance) define
what must be demonstrated before marking it delivered.

- Distinguish source evidence, evolving current matters, existing durable memory
  and recommendations, with their own lifecycle and source links.
- Let the AI find applicable memory and matters on demand, inspect cited evidence
  and use them in a fresh conversation without manual copying or record IDs.
- Complete selected DWS conversation → current matter → cited private suggestion
  → user-selected preparation task, with refresh and postpone/dismiss/resolve.

Start with the owner and selected sources under existing project mappings.
Personal-domain expansion and automatic conversation learning are P1; proactive
notifications and specialist scenarios are P2. P0 uses existing memory types and
publication behavior and adds no automatic external message delivery.

## Related memory increment

Automatic publication of Jev-selected execution experience and a blacklist that
prevents equivalent disabled lessons from being admitted again. Eligible user
statements already save automatically; execution experience still requires
`memory accept`. Follow the existing [memory design](experience-memory.md).
This remains separately validated: P0 can retrieve existing eligible memory
before it ships, while P1 conversation learning requires the publication and
blacklist policy to be in place.

## Deferred ideas

Consider `/retry`, `/remember`, DingTalk review and usage reporting only if daily
use shows a need. Keep `/new`, `/status` and `/help` as the current chat controls.

Scheduled digests, proactive messages, a Web interface, new knowledge sources,
Codex integration and project discovery remain separate future work. Member
permission roles and separate tool profiles are not part of the current plan;
private/group differences remain in system prompts.
