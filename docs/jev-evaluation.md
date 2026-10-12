# Jev gateway evaluation

Historical baseline: these measurements predate the integrated MVP and its
`user-statement-v2` policy. The current [MVP](mvp-validation.md) explicitly uses
the company gateway; integrated live evidence is recorded separately.

Run: 2026-10-10. Status: live synthetic experiment through the user-selected
Patsnap gateway, not an implemented Agent-Host memory integration. See the
[method and evidence](jev-evaluation-detail.md) for reproduction and limitations.

## Conclusion

Jev is a plausible component for bounded Chinese entity selection and record
classification. Keep it optional while measuring on representative user cases.
This small experiment does not establish an advantage for memory reranking:
both Jev and the simple lexical baseline passed all six retrieval scenarios.
The roughly one-second request cost therefore needs a demonstrated benefit.

Keep source authority, event verification, scope, and database writes in Go.
A model can identify an assistant claim correctly while still judging that
claim as supported. Better criteria helped in diagnostics, but do not replace
host evidence rules.

## Observed results

The main suite had 40 successful requests, including four unscored smoke/repeat
requests. Three later diagnostic requests are reported separately. One request
failed on a gateway protocol difference and was preserved before continuing
with an explicit encoding change. An initial connectivity probe was separate.

| Main-suite check | Result |
| --- | --- |
| Chinese request purpose | 9/10 |
| Entity selection, including none/ambiguous | 8/8 |
| Memory/source kind | 8/8 |
| Whether evidence supports a claim | 6/8: one wrong positive, one uncertain |
| Memory selection or abstention | 6/6; lexical baseline also 6/6 |
| Reversed option order | 4/4 labels remained correct |
| Response shape validation | 40/40 successful responses passed |

Successful main requests had median latency 0.991 s, nearest-rank P95 1.527 s,
and maximum 3.125 s. These include gateway/network time from this machine;
they are not model-only timings or a service-level guarantee.

The supplied English example selected `billing` (probability 0.89) and urgency
0.95 in the connectivity probe. Frustration was 1.05 on a 0–2 rubric. Repeated
requests retained the department choice but slightly changed probabilities
and scores. Do not treat the service as byte-for-byte deterministic.

## Integration consequences

- The requested `jev-latest` resolved to `typesafe/jev-1.13-20260917`; responses
  identified `openrouter` as provider. Record the actual returned model.
- This gateway rejected object-valued `instructions` with HTTP 400, although
  TypeSafe documents that shape. Explicit JSON-to-string encoding worked.
- Main-suite usage was 30,236 input and 3,070 output tokens. Gateway billing was
  not available, so this report does not claim a dollar cost.
- Clarifying the evidence policy changed the two support judgments from
  0.85 to 0.10 and from 0.67 to 0.89. Tightening the task taxonomy also corrected
  the chat-versus-status case. These are post-hoc diagnostics, not held-out gains.

The next adoption decision needs harder real-language examples, independently
labeled relevant memories, and comparison with the actual retrieval baseline.
No SQLite retrieval implementation, native Claude memory loop, or production
calibration was tested here. The [memory design](cli-memory-design.md) retains
Jev as an evaluation option.
