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

**P0 implemented on 2026-10-12; validation is recorded separately below.**
This core memory increment has no assigned release version or production rollout
date. The [main guide](personal-assistant.md#current-p0) owns scope;
[acceptance notes](release-follow-up-detail.md#p0-acceptance) distinguish bounded
validation from remaining quality and scale work.

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

The [file memory and Skill proposal](file-memory-design.md) now owns the next
memory delivery order: file foundation and reversible migration, retrieval and
read controls, automatic publication and native skills, then live cutover.
The database-first ordering below is the earlier planning baseline. Neither this
proposal nor the historical acceptance records establish new implementation.

Automatic publication of Jev-selected execution experience and a blacklist that
prevents equivalent disabled lessons from being admitted again. Eligible user
statements already save automatically; execution experience still requires
`memory accept`. Follow the existing [memory design](experience-memory.md).
This remains separately validated: P0 can retrieve existing eligible memory
before it ships, while P1 conversation learning requires the publication and
blacklist policy to be in place.

## Issue #1 delivery plan

Reviewed [issue #1](https://github.com/zhoushoujianwork/Agent-Host/issues/1) on
2026-10-13: one open issue and no open PRs were returned. It proposes
[experience as skills](experience-skills.md) and
[per-person reply preferences](person-profiles.md). Both remain planned.
The repository already has attributed memory, scoped history and assistance;
the proposed layers build on those foundations.

| Order | Deliverable | Completion gate |
| --- | --- | --- |
| 1 | Automatic experience publication and blacklist | Save, fresh-task reuse, disable and equivalent re-admission protection without mandatory `memory accept` |
| 2 | Scoped procedure skills for Claude | Native discovery/use, source inspection, scope isolation and withdrawal in subsequent tasks |
| 3 | Owner-private explicit reply preferences | Opt-in, fresh-conversation use, current-request override, correction and disable |
| 4 | Explicit identity links and group-member preferences | Source visibility preserved; alternating members keep distinct preferences; no private profile enters shared history |

The table preserves the earlier issue planning baseline. For new memory work,
begin with the file foundation and reversible migration in the
[new design](file-memory-design.md#本期范围与实施顺序), then integrate retrieval
before automatic publication and native Skill use. Explicit person profiles
remain a separate proposal and should use the new knowledge lifecycle when
implemented. Group preference work follows real non-owner channel acceptance.

Existing reliability checks remain active: the historical Jev admission failure,
database wait diagnosis, and Feishu group/multi-turn/reaction platform acceptance
are not resolved by planning this issue. No release date is assigned. The
[planning notes](release-follow-up-detail.md#issue-1-planning-baseline) record
the evidence and separate design completion from feature acceptance.

## Deferred ideas

Consider `/retry`, `/remember`, DingTalk review and usage reporting only if daily
use shows a need. Keep `/new`, `/status` and `/help` as the current chat controls.

Scheduled digests, proactive messages, a Web interface, new knowledge sources,
Codex integration and project discovery remain separate future work. Member
permission roles and separate tool profiles are not part of the current plan;
private/group differences remain in system prompts.
