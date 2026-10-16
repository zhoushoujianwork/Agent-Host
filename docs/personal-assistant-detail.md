# Personal assistance: research and technical notes

Reviewed: 2026-10-12. The [main design](personal-assistant.md) governs scope and
implementation order. P0 is implemented in the
[release record](release-follow-up-detail.md#p0-acceptance).
Implementation and bounded acceptance are described below; research and later
personal-memory proposals retain their own evidence boundaries.

## P0 implementation

`internal/discovery` still collects exactly configured sources under one fixed
personal DWS profile and preserves topic review/export semantics. Complete
windows also invoke `internal/assistance.Processor`: an isolated, tool-free
native call reconciles questions and commitments against current snapshots and
prior matter identities. It executes no task and publishes no memory.
Explicit cancellation, withdrawal or replacement closes the requested matter
without claiming successful execution, and suppresses stale preparation choices.
Superseded source prose is scrubbed before model input; missing/withdrawn
citations exclude dependent observations. Upstream edit/deletion detection is
limited to what the provider reports, never inferred from a missing message.

SQLite schema v13 adds jobs, matters, revisions, pinned citations and suggestions
in separate tables. Source digests and expected revisions reject stale analysis.
Each matter has one stable preparation suggestion. Failed/cancelled analysis
records a retryable job using an independent finalization deadline. Immutable
citations stay inspectable, but expired current cache pointers cannot become
active input again. The execution viewer accepts v13 without exposing these
private tables.

Owner-only single-project tasks receive `host_context` MCP unless tools are
disabled. Its binding derives Agent, audience, project, scope and lifetime from
the saved running task. Selected-source config intersects that project and is
revalidated per call; selection also affects native session compatibility.
Default-workspace, multi-project and shared-group tasks do not get this surface.

| Read tool | Contract |
| --- | --- |
| `search_context` | Matter/memory navigation snippets with role, scope, revision, time and source coverage. Memory retains existing applicability checks; matter subject/open-item matches outrank incidental summary mentions. |
| `read_context` | Current matter or eligible memory revision. Memory includes original supporting evidence within 8 KiB; larger evidence remains a separate read. |
| `read_context_evidence` | Original available matter citations or memory's attributed user input/selected tool observations; the same scope, revision and eligibility checks apply. Matter citations support offset/limit pagination. |
| `list_private_suggestions` | Current choices with reasons, matter citations and explicit local selection fields. Reading cannot select an action. |

Search accepts 256 query bytes and 20 records per kind; snippets are 80 Unicode
characters, not source citations. Analysis requires the complete retained set
within 200 snapshots/128 KiB and at most 100 matters per source. Encoded MCP
responses are capped at 64 KiB; excessive source content fails explicitly.
Context reads are excluded from execution-experience extraction. Existing
memory admission and the separate publication/blacklist increment retain their
own semantics and validation.

The extractor policy is `owner-project-context-v2`; an unchanged evidence digest
is reused only under the same policy. This lets policy corrections reconcile
retained real sources again without manufacturing new messages. Memory annotation
v2 separately fixes the readiness "success token"/authentication-token ambiguity;
derived metadata upgrades preserve source memory and revisions.

Suggestions require an open, available matter, successful reconciliation of
complete collection, coverage caught up through collection time and collection
within 24 hours. This conservative freshness bound is not an urgency prediction.
Partial, failed, missing and stale coverage is visible; silence does not prove a
blocker. Postpone sets a review time; dismissal persists across refresh; owner
resolution remains distinct from the model's observed outcome. None alters memory.

Preparation fixes the action to a cited brief/checklist, supplies source content
as data and enters ordinary Host/native execution and existing finalization.
Claiming a suggestion revision and creating its business run are one transaction.
Repeated selection or restart cannot replay it; its linked run stays inspectable.
Preparation does not authorize messages or operational changes. An executed
suggestion can still be resolved.

### Commands

Use a registered project and owner-private discovery config. `--home` selects
state; normal chat model/provider options apply to preparation. Inspection and
feedback require no model credentials. Stop a managed service before standalone
mutations of its state.

```sh
agent-host assistance refresh --home "$host_home" --project app --config "$discovery_config"
agent-host assistance suggestions --home "$host_home" --project app --config "$discovery_config"
agent-host assistance prepare --home "$host_home" --project app --config "$discovery_config" --revision N SUGGESTION_ID
agent-host assistance postpone --home "$host_home" --project app --config "$discovery_config" --revision N --until RFC3339 SUGGESTION_ID
agent-host assistance dismiss --home "$host_home" --project app --config "$discovery_config" --revision N SUGGESTION_ID
agent-host assistance resolve --home "$host_home" --project app --config "$discovery_config" --revision N SUGGESTION_ID
agent-host chat --home "$host_home" --project app --discovery-config "$discovery_config" -m 'What remains open and what guidance is already saved?'
```

Use the actual revision from the list or `assistance suggestion`; stale feedback
or selection fails. `assistance matters` lists matters, `assistance show` reads
original citations, and `assistance search` accepts a lexical query. A managed
DingTalk service reuses its existing `discovery_config` collector and owner-task
configuration. P0 does not select new sources or alter production configuration.

### P0 validation

Automated checks cover stable identities/progress, interleaved issues, corrected
and withdrawn sources before model input, partial/failed coverage, feedback,
read-only access and atomic preparation/recovery without replay. Native and
selected-source acceptance are recorded separately in `experiments/assistance`;
raw messages and model answers stay in private state outside Git.

On 2026-10-12, `make check` passed. Real native English/Chinese fresh sessions
recovered source-only saved values and read original citations, then completed
chosen preparation and blocked replay. Native progress and requester cancellation
updated stable matters and suppressed stale suggestions. The real selected-DWS
pilot retained 12 messages and four matters, with source reads in a fresh native
conversation. Review ended with two reported resolutions, one closed cancelled
request, one conflicting report and zero active suggestions.

The [acceptance reports](../experiments/assistance/README.md) keep synthetic/native
and real-source evidence separate. An earlier 48-hour/99-message probe timed out
in native analysis despite complete intake; it is not passing acceptance.
The successful three-hour pilot does not establish wide-window performance,
account-wide recall, upstream deletion coverage or long-term usefulness.

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
