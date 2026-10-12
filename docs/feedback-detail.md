# Feedback report: evidence boundaries

Companion to the [feedback guide](feedback.md).

## Local diagnostic projection

`feedback report` produces versioned JSON with a random report identifier and
build version/revision/dirty state, platform and database schema information.
Build identity describes the report generator; the historical execution build
remains unknown when the run did not record it.
An optional source run contributes recorded execution, memory, experience and
acceptance states, reply delivery, valid phase timings, tool counts and injected
memory counts. Unknown observations remain unknown. Actual model information is
reported only when it matches the supported format; requested model is not used
as a substitute for an unobserved model.

Projection uses an explicit field and enum allowlist. Source input/output,
memory bodies, raw judgments/errors, tool arguments/results, filesystem paths,
credentials and Agent/member/platform/session identifiers are excluded. The
report is written to a new owner-only file. Reading does not migrate the state,
take its writer lock, replay a task or change memory.

No-run reports support installation feedback even when state is unavailable.
An explicitly requested missing or inaccessible run is an error; it must not
produce an invented empty successful run. Existing partitions remain enforced.

## Feedback handling

Triage each submitted issue by its observed symptom, affected build and available
evidence. Ask for a smaller fictional reproduction when details are insufficient.
Retain unresolved feedback as unresolved, then turn reproduced incidents into
the existing [evaluation/regression workflow](evaluation.md). Record the fix and
validation on the issue; do not silently relabel the original outcome.

Local observation distinguishes memory creation, selection, injection, observed
use and usefulness. Report counts establish only the recorded stages. User
feedback and bounded case inspection supply the missing judgment; neither
native success nor accepted delivery verifies business task acceptance.

The daily observer reads this owner's local evidence and writes dated private
records. It does not see independent testers' databases. Current memory rows do
not reconstruct every daily transition; use prior snapshots or mark coverage
incomplete rather than inventing counts of disabling or correction.
