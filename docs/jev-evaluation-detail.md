# Jev evaluation method and evidence

The [main report](jev-evaluation.md) contains the conclusion and adoption scope.
All input content was synthetic. User authorization covered the specified
Patsnap endpoint and the key configured in the local `llm` alias.

## Requests and credential handling

The evaluator read the literal `ANTHROPIC_AUTH_TOKEN` assignment from that
alias in memory after checking its base hostname against the requested gateway.
It did not execute the alias, change shell configuration, expose the token,
or store authorization headers. The committed runner also supports the explicit
`PATSNAP_API_KEY` environment variable. Runtime endpoint:
`https://gateway.example.invalid/v1/systemone`.

The full record is one connectivity probe, 40 main-suite successes, one failed
main-suite attempt, and three post-hoc diagnostic successes: 45 HTTP requests.
The runner used sequential calls with a 45-second timeout and no automatic
retries or provider switching. API response metadata identified OpenRouter;
that gateway-selected route was not changed by the evaluator.

Official TypeSafe accepts structured instructions according to its
[API reference](https://docs.typesafe.ai/api). The gateway instead returned:

```text
json: cannot unmarshal object into Go struct field
TypesafeQuestion.questions.instructions of type string
```

The first batch stopped on this error. The remaining twelve cases were then
run with an explicit `--string-instructions` conversion that JSON-encodes the
same instruction content. The failed request remains in the first artifact;
neither it nor its time is included among successful-response latency samples.
This is a recorded protocol adaptation, not a provider or model fallback.

## Cases and scoring

[Fixtures](../experiments/jev/fixtures.py) define the 40 main cases and labels
before their requests. Each artifact saves exact requests, expected labels,
responses, judgments, timestamps, and a fixture hash. No expected labels were
changed after seeing responses. The separate diagnostic function explicitly
changes criteria after inspecting failures.

The main set consists of 10 purpose cases, 8 entity cases, 8 evidence cases
with two questions each, 6 ranking queries, 4 option-order variants, and 4
unscored demonstrations/repeats. These are small hand-authored synthetic cases,
not a random sample, independent benchmark, or calibrated production dataset.
Scores for the different tasks must not be pooled as general accuracy.

- Choice is graded by the supplied categorical label. The taxonomy itself has
  interpretation limits: the model classified a happy-mood statement as a
  report rather than casual chat. A later task-specific definition clarified it.
- Evidence Nouls used a preset yes threshold of 0.8 and no threshold of 0.2.
  Intermediate values count as unresolved rather than correct automatic answers.
  These thresholds were not calibrated. Six answers met their expected labels,
  one was a false positive, and one was unresolved.
- Ranking scored each of seven eligible candidates on a 0–3 rubric. The preset
  selection threshold was 2.0; top selected record or abstention was graded.
  Five relevant records were selected, with zero labeled false positives or
  false negatives across these six easy queries. One query needed no memory.
- The lexical baseline uses the fraction of query character bigrams present
  in each candidate. It is not SQLite FTS5 or BM25. It also passed 6/6, so this
  suite shows no measured improvement over even this simple baseline.
- Out-of-project and superseded records were filtered in code before model
  input. Their exclusion is a host property, not model accuracy. One synthetic
  candidate tried to manipulate scores; it was rejected in this limited setup,
  which does not establish general prompt-injection resistance.
- P50 is the median of 40 successful main requests; P95 uses nearest rank.
  Each call includes network/HTTP processing, with no concurrency. The probe,
  failed request, and diagnostics are excluded. Token counts are gateway-reported.

## Failure analysis

The assistant-only statement "部署已经成功了" was correctly classified as an
assistant claim, but the proposed "生产部署成功" received support probability
0.85. The original broad support question did not spell out all evidence-policy
rules. That result cannot establish an independently verified production action.
The rule belongs in host code regardless of the model's answer.

An explicit tool write event received only 0.67 support for the narrow claim
that the tool reported a successful write. This was unresolved under the preset
threshold. Adding precise rules about source authority and the scope of a
tool observation changed the two probabilities to 0.10 and 0.89 respectively.
Both diagnostics then met the labels. This demonstrates rubric sensitivity on
known cases, not an independently measured accuracy improvement.

The English example and Chinese translation both chose billing, but differed
in frustration and confidence. Both state and instructions were translated;
this single pair cannot isolate language effects or validate multilingual parity.
Three main-suite English repeats gave billing probability 0.90, 0.87, and 0.90,
while frustration was 1.05, 1.04, and 1.04. Typed output does not imply identical
numeric answers on repeated requests.

## Reproduction and artifacts

See the [experiment instructions](../experiments/jev/README.md) and
runner (private validation evidence). No credential is needed to inspect results.

- Connectivity probe (private validation evidence)
- Initial batch, including HTTP 400 (private validation evidence)
- Remaining cases with string instructions (private validation evidence)
- Post-hoc diagnostics (private validation evidence)
- Machine-readable summary (private validation evidence)
