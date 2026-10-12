# Personal assistance: research and technical notes

Reviewed: 2026-10-12. The [main design](personal-assistant.md) governs scope and
implementation order. The approved P0 is ready for development in the
[release backlog](release-follow-up.md#p0-contextual-assistance-and-accessible-memory).
This is documentation research and planning, not implementation or live product
acceptance. Schemas, command names and numerical ranking thresholds are left to
the implementation increments.

## Research findings

| Primary evidence | What it establishes | Implication and limit |
| --- | --- | --- |
| User-supplied Feishu screenshot | The displayed card combines a situation summary, preparation action, timing rationale, cited messages and feedback controls | Reference for the experience only; the source conversation and backend were not inspected. The exact public specification for “上下文智能推荐” was not verified |
| [Feishu Aily tasks](https://www.feishu.cn/content/xb36mver) | Tasks combine triggers with skills; documented triggers include schedules, webhooks and Feishu events | Supports source-independent triggering. Does not establish this card's model, memory architecture, account availability or inactivity threshold |
| [Microsoft Copilot in Viva Engage](https://support.microsoft.com/en-gb/viva/engage/get-started-with-microsoft-365-copilot-in-viva-engage) | Contextual prompts and catch-up cards; community/conversation cards require meaningful new updates, and selecting one invokes a summary prompt | Supports an attention threshold and explicit next action. This is a catch-up product, not evidence of accurate unresolved-work detection |
| [Microsoft Copilot personalization](https://support.microsoft.com/en-us/microsoft-365-copilot/personalize-what-microsoft-365-copilot-remembers) | Describes saved memories, chat-history inferences, work-content insights and user controls as distinguishable personalization inputs | Supports separating stored knowledge from inferred context. Does not prescribe our storage model or prove that every work insight should persist |
| [LangChain memory overview](https://docs.langchain.com/oss/python/concepts/memory) | Distinguishes thread state from cross-thread memory, and semantic, episodic and procedural uses; a general store can organize documents by namespace and key | Use a few semantic roles over shared infrastructure. The classification does not require separate databases. Host business matters also differ from native harness state |
| [Google Memory Bank](https://docs.cloud.google.com/gemini-enterprise-agent-platform/scale/memory-bank) and [revisions](https://docs.cloud.google.com/gemini-enterprise-agent-platform/scale/memory-bank/revisions) | Documents scoped collections, extraction/consolidation, expiration and revisions; current memory is distinguishable from its historical revisions | A generic model still needs ownership, time and change history. A design reference, not a proposal to adopt the managed service |
| [LongMemEval](https://arxiv.org/html/2410.10813v2) | Tests extraction, cross-session reasoning, temporal reasoning, knowledge updates and abstention; its experiments identify context loss from reducing history to isolated facts | Preserve supporting context and evaluate updates and uncertainty. Conversational QA does not establish workplace recommendation quality or an Agent-Host accuracy target |

These sources motivate our proposed separation; they do not prove that our
pipeline works. No vendor architecture is inferred from the screenshot. Personal
names, organization identifiers and source conversation content from the image
are not reproduced here.

## Current Agent-Host baseline

| Area | Delivered evidence | Gap for this proposal |
| --- | --- | --- |
| Collection | [Collector](../internal/discovery/collector.go), [configuration](../internal/discovery/config.go): fixed profile, selected conversations, bounded history and coverage | Current sources require a registered project; owner-personal association is not implemented |
| Source preservation | [Discovery state](../internal/state/discovery.go): immutable message revisions and transactional collection ledgers | Broader event/change coverage and sustained group quality remain unverified |
| Topic discovery | `TopicDraft` has title, summary, reported outcome, unresolved question and evidence IDs | A proposal is a snapshot, not a stable matter that tracks progress across new evidence |
| Deduplication | `SaveTopicExtraction` suppresses duplicate evidence fingerprints | New evidence can create another proposal; matter identity and reconciliation are needed before repeated recommendations |
| Acceptance | `AcceptTopic` exports bounded owner-private evidence | It neither creates a task, sends a message nor publishes memory; preserve this command's meaning |
| Memory | [Memory records](../internal/state/types.go), [validation](../internal/state/memory.go): generic types, scope, sources, revisions and expiration | Automatic conversation learning and the revised equivalent-lesson blacklist remain planned |
| Runtime | [Assembly](../internal/assembly), existing Host tasks and native harness adapters | Add a separate user-authorized recommendation handoff; do not make the collector a tool-execution loop |

The [history guide](conversation-history-detail.md) records selected live DM
acceptance on October 11–12. Its bounded results do not prove comprehensive
coverage, continuous operation or recommendation quality. The
[experience guide](experience-memory.md) separates the automatic-publication
target from current manual experience acceptance.

The installed `dingtalk-chat/SKILL.md` and `dingtalk-event/SKILL.md` were also
inspected. These local capability references are not public platform
specifications. Their documented incoming IM stream filters self-sent messages;
history reconciliation therefore remains necessary. Events can later reduce
latency, with bounded history reads to fill supported gaps; they do not establish
complete coverage. Verify installed leaf contracts during implementation. This
research did not log in, start listeners or read a live DWS account.

## General model and classification decision

Use a shared evidence and retrieval foundation with logically distinct records.
“Unified” does not require one physical table or one retrieval policy. Establish
these dimensions before introducing specialist categories:

| Dimension | Required meaning |
| --- | --- |
| Ownership and visibility | Which tenant/person/Agent owns it, which audience may see it, and which sources remain authorized |
| Applicability | Subject, project or personal domain, conditions, and optional people/topic associations |
| Time | Source event time, observation time, assertion validity, and refresh/expiry time |
| Support | Exact source revisions, attributed author, reported versus observed outcome, and uncertainty |
| Lifecycle | Current revision, superseded/conflicted/disabled status, and links to corrections |

Keep source kind separate from content kind. A DWS message can support a
preference, fact or procedure; a meeting is a source/context, not necessarily
a new memory type. A person can appear in several projects without their records
becoming mutually visible. Do not merge people using display name alone.

Existing `preference`, `constraint`, `project_fact`, `decision` and `procedure`
types are a sufficient starting vocabulary. Personal facts need appropriate
scope and validation changes; do not reinterpret existing `project_fact` rows as
global facts. Business-specific properties can initially be annotations.
Separate storage is justified by distinct operations or lifecycle requirements,
not a new navigation tab.

Current matters contain open questions, explicit commitments, responsible people
when supported, and the latest observed progress. “Waiting for an answer” belongs
here. Closure requires a supported outcome or the owner's resolution; age alone
makes the matter stale or unknown. Marking it resolved does not prove which
procedure worked. Native conversation state remains owned by the harness.

Do not promote an entire conversation summary into memory. Select reusable
claims while retaining enough cited context to interpret them. A proposed fix,
a participant-reported resolution and an observed tool result retain different
evidence strengths. Repeated behavior may suggest a hypothesis, but the first
scope learns personal preferences only from explicit statements.

## From sensing to a recommendation

1. **Collect and validate.** Admit evidence from selected sources under the fixed
   principal. Preserve revisions and coverage. Unknown or partial ranges must
   not support assertions about missing replies.
2. **Update a matter.** Prefer explicit reply/thread links and issue references;
   use bounded model judgment for ambiguity. Time proximity alone must not merge
   unrelated work. Attach new evidence to a stable matter and retain revisions.
3. **Consider useful actions.** Combine the matter with eligible memory and
   available capabilities. Relevance needs evidence: an owner question, explicit
   commitment or verified mention, rather than mere presence in a group. The
   current [provider projection](../internal/discovery/provider.go) lacks a
   structured mention/assignee field; inferred relevance must remain qualified.
4. **Apply an attention policy.** Require a supported open question or commitment,
   a useful bounded next step, fresh evidence and allowed visibility. Rank by
   relevance, urgency, expected benefit, uncertainty and interruption cost.
   These are decision factors, not calibrated probabilities or a fixed formula.
5. **Present and recheck.** Show the action, why now, supporting sources, last
   checked time and relevant uncertainty. Refresh before presentation and action;
   changed evidence can withdraw or revise the suggestion.
6. **Start the chosen task.** Record a distinct authenticated user request linked
   to the suggestion revision, route it normally, and assemble permitted context.
   Preserve task, execution, delivery and memory outcomes separately.

The first surface is an owner-private list, available on request or local
inspection. A plain list with selectable actions can validate the loop;
interactive DingTalk cards are not a prerequisite. Later proactive DM delivery
needs its own opt-in, outbox and live platform acceptance.

A timer means “re-evaluate,” not “send again.” Keep one active suggestion per
matter and equivalent action; unchanged evidence does not create another
interruption. Postponement sets a review time, dismissal suppresses the suggestion,
and resolution closes the matter. Private notifications later need quiet hours,
a per-owner frequency budget and a useful-change threshold. Do not copy the
screenshot's 2.5-hour interval into a global rule.

Dismissing a card does not invalidate its cited memory. Disabling a memory blocks
recall and equivalent re-admission; it does not erase evidence by implication.
Do not infer a permanent preference from one dismissal. Click-through alone is
not a sufficient measure of usefulness.

For the screenshot scenario, the observation is unanswered integration questions
in the collected window. The inference is that preparing a checklist could help.
Error codes in chat are reported evidence, not a fresh reproduction or proof of
root cause. After the owner chooses preparation, the native agent can consult
permitted documentation and produce a checklist with unknowns. Contacting the
colleague is a separate action.

## Personal scope, publication and lifecycle

The first recommendation increment can reuse selected-project sources. The
personal-memory increment explicitly introduces the owner-private domain instead
of repurposing `default` or broadening current project grants. Existing rows
retain their scope. Unmapped or multi-project evidence stays private and
unclassified until its permitted use is resolved; it is not guessed into the
active repository.

A personal assistant may combine authorized project facts for its owner without
republishing them as universally applicable personal memory. Generic owner
preferences can apply across permitted owner tasks; private work evidence remains
source-bound. Group replies do not inherit owner-private context. Apply scope
checks before retrieval and model processing, not only before display. This
does not claim OS-level isolation.

Conversation learning uses independent extraction/selection jobs, without a
synthetic completed run. Jev compares proposals with applicable current memory;
Host validation checks evidence, scope, revisions and exclusions before automatic
publication. No mandatory human memory-review queue is introduced. Existing
`discovery accept` continues to export evidence only. Implement source admission
and blacklist enforcement before enabling automatic conversation publication;
reviewed execution acceptance cannot prove chat-learning quality.

Keep updates and conflicts explicit. A later scoped decision may supersede an
earlier one; an unrelated newer statement cannot. Unresolved contradictions stay
out of factual recall. Expired or disabled records are excluded. Detected source
withdrawal or lost access invalidates affected suggestions and suspends dependent
memory until reassessed; visibility changes must invalidate reusable native
context where required. Retained evidence follows its authorized retention policy,
not an assumption of permanent source access.

Deleting content, disabling its use and revoking access are distinct operations.
Removal must cover derivatives and indexes; an exclusion marker must not retain
deleted sensitive content. Immediate detection of every upstream edit/deletion
is not promised by the current collector. Source text cannot authorize execution,
publication, expanded access or a changed memory policy. Learned procedures
describe methods; they do not become permission grants or system instructions.

## Implementation seams and evaluation

### P0 boundary and AI access

P0 combines matter tracking, read access and one recommendation-to-preparation
loop for the owner within existing selected-project sources. It can consume
eligible existing memory without waiting for P1 conversation learning or a new
personal domain. Existing `discovery accept` and memory-publication behavior
retain their meanings; no new memory approval workflow is introduced.

Task assembly provides short capability guidance and bounded applicable context.
A private read-only Host tool surface lets the native agent search matters and
eligible durable memory, inspect a result and follow its retained citations.
Exact tool names and protocol shape are implementation decisions. Return the
record's role, scope, relevant timestamps, revision and support so the agent can
distinguish a report, an inference and reusable knowledge. Querying must not
change matter state, publish memory or start an action.

Authorization derives from the admitted task's identity, Agent, audience and
project grants, intersected with selected-source access. Callers cannot choose
broader grants. Check them before matching, reading citations and model input;
apply current eligibility and revision checks when following a reference. An
unavailable source or incomplete window is an explicit limitation, not evidence
that no answer exists. Existing Host conversation-history access stays scoped
to its admitted conversation. This surface reads authorized retained context;
it does not mount the DWS CLI as a general runtime history tool.

Start P0 in owner-only, single-project tasks. General cross-project queries,
shared-group recommendations, new personal scopes and automatic conversation
publication remain later scope. Reuse existing memory exclusion checks now;
the stronger equivalent-re-admission blacklist remains the separate memory
increment. If that increment ships earlier, P0 consumes its eligible output
without adding another publication policy.

### Reuse and acceptance

Reuse `internal/discovery` for intake; add matter reconciliation and recommendation
policy as Host components. Reuse `internal/state` transactions and single-writer
service ownership. Selected actions enter normal routing/assembly and native
execution. Reuse `internal/memory`, `internal/experience` and applicability indexes;
extend provenance rather than duplicating a DWS-only memory engine. Begin with
bounded lexical/object retrieval; evaluate embeddings or graphs against measured
recall failures. No public plugin API is needed for this increment.

Establish a small anonymized replay set and a bounded owner pilot. Proposed
acceptance cases, not results already obtained:

| Case | Required behavior |
| --- | --- |
| Fresh conversation asks about open work and prior experience | Native retrieval finds the permitted current matter and relevant existing memory, reads supporting citations, and distinguishes their roles without user-supplied IDs |
| An excluded record, guessed reference or inaccessible citation | Neither search nor direct read reveals it; retrieval has no write or execution side effects |
| Unanswered question with a useful preparation step | One cited suggestion; chosen action produces the requested artifact |
| New answer, resolution elsewhere reported by the owner, or changed deadline | Refresh/withdraw the recommendation; preserve prior observations |
| Silence, incomplete collection or missing context | Qualify the observed window or abstain; do not assert a continuing blocker |
| Duplicate imports, new evidence for the same issue and restart | One evolving matter; no repeated equivalent action or implicit task replay |
| Interleaved issues or ambiguous people | Avoid incorrect merges and assignments |
| Group context, private memory and project changes | No unauthorized retrieval or private citation leakage |
| Changed fact, contradictory report, withdrawal or disabled equivalent lesson | Correct update/exclusion across recommendation and later recall |
| Suggested fix or unobserved success claim | No verified-success or successful-procedure memory |
| Dismiss, postpone and mark resolved | Apply feedback without inventing a long-term preference |

The [release acceptance notes](release-follow-up-detail.md#p0-acceptance)
select the P0 gates from this broader matrix. New conversation publication and
equivalent re-admission prevention need P1/separate memory-increment acceptance;
their absence is not hidden by a passing P0 retrieval check.

Report eligible cases and abstentions as well as suggestions. Measure supported
claim rate, useful suggestions among those shown, stale/duplicate rate, missed
important cases, interruptions per owner-day, action completion and later memory
usefulness. Record latency, collection gaps and model cost. Critical source and
permission failures must be absent in the acceptance set; numerical usefulness
targets require pilot calibration. Passing deterministic cases does not prove
general model quality, and a larger memory count is not a success criterion.
