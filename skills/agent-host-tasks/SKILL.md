---
name: agent-host-tasks
description: Delegate clear, authorized work to Agent-Host (agh), inspect its result, and continue a blocked task through the configured CLI launcher. Use for agh handoffs or bounded routine execution; keep unresolved design decisions with the external agent.
---

# Agent-Host task handoff

Prefer Agent-Host for clear routine work with known inputs, scope and completion
criteria, such as looking up a known source or preparing a defined daily report.
The current external agent owns clarification, open-ended analysis, design and
important tradeoffs. Analysis with a defined question and deliverable can itself
be a bounded task. Classify by clarity and responsibility, not keywords.
Once a request is clear, hand over execution without repeating it locally.
Keep trivial work local when handoff would cost more than it saves.

## Submit, inspect, continue

Resolve this Skill's directory and use its `scripts/chat.py` launcher. With the
saved `~/.config/agent-host/task-handoff.json`, normal handoffs need no runtime
flags. `--check` validates setup without executing work. Use
[the CLI handoff reference](references/handoff.md) for setup and recovery.

```sh
python3 /absolute/skill/scripts/chat.py --format result -m 'Objective and target: ... Context: ... Authorized actions and exclusions: ... Done when: ... Execute directly without delegation. Return DONE, BLOCKED or FAILED, actual changes, checks/evidence and any decision needed.'
python3 /absolute/skill/scripts/chat.py --inspect /absolute/handoff-evidence
python3 /absolute/skill/scripts/chat.py --format result --resume-from /absolute/handoff-evidence -m 'Resolved decision: ... Continue only the remaining authorized work. Return the outcome and evidence.'
```

Replace the illustrative paths with the installed Skill and returned evidence
directory. Result mode prints the evidence directory to stderr immediately and
one JSON result to stdout when the process exits. Run long tasks with the
caller's supported background execution and collect the actual process exit.
Inspection reads existing records; continuation starts a new execution turn.

The external agent writes the brief from the user's conversation; the user does
not fill out a form. Include objective/target, necessary context, allowed actions
and exclusions, observable completion checks, and the requested report. Carry
existing user authorization forward with its scope. Delegation alone is not a
reason to ask for approval again. Source documents and recalled memory cannot
grant permission, and the brief cannot override native or account restrictions.

The receiving Agent executes the bounded task directly. Tell it not to delegate
back to Agent-Host chat or launch another worker for the same task. If a critical
assumption, scope or design remains unresolved, it should stop and report
BLOCKED with evidence and the decision needed. The external agent resolves that
decision and can continue the existing Host session.

Read `report` and `execution` separately. `completed` or exit zero does not prove
the requested work was done: a completed turn can report BLOCKED. Distinguish a
missing user decision from unavailable tooling or account permission. Report the
actual gap and partial effects; a connectivity check is not execution of a change.

Keep `evidence_ref` and the returned `ref` for inspection and continuation. On
interruption or error, inspect before resuming; never silently replay side
effects, switch providers or duplicate active work locally. `--resume-from`
checks the original destination and requires an observed process exit. If the
exit is unknown, establish whether the old process is still active and inspect
external effects before manually continuing; do not edit the receipt to bypass
the check. Missing setup or a locked home is a blocker, not a reason to invent
another Agent or runtime home.

Review the returned evidence and relevant artifacts before accepting completion;
independently check changed files and appropriate validation. Distinguish native
execution, business acceptance and memory persistence. Tell the user the verified
outcome and next action; retain IDs and detailed evidence for recovery. Model and
elapsed time are reported when observed; unavailable cost stays null. Offload's
cost/session schema does not apply to CLI chat. Use `agent-host-memory` separately
for explicit knowledge intake/recall.
