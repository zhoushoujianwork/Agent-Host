# Offload canary acceptance — 2026-10-30

Authorized live acceptance used an isolated, private local workspace and state.
Production channels, their configuration and the running Mac app were unchanged.
Source baseline: `54d8497`; Offload `0.4.0`; existing profile
`codex-gpt-6.1-sol-medium-fast`, native model `gpt-6.1-sol`.
Memory used the existing explicit custom Jev configuration, model `jev-latest`.
No credential values were added to source or evidence summaries.

| Check | Observed result |
| --- | --- |
| Web → AGH → real Offload | Exact canary-file value returned; engine `offload`, backend `codex` |
| Native continuation | Two distinct AGH receipts used the same native session; second reply retained the value without a new file read |
| Accepted retry | Repeated request returned the same turn; run count stayed at two |
| Memory processing | Exact canary assertion saved by the real classifier |
| Fresh-session recall | Different native session received one scoped memory and returned the exact marker |
| Withdrawal | After explicit invalidation, another fresh session received zero records and returned `NO_MARKER` |
| Workspace effects | Only the two prepared fixture files remained; fixture content was unchanged |

Read/continuation runs: `6464c5bc-fd1a-42a1-bee8-4c0535d59bf1` and
`96eeb799-6b36-484b-84eb-fe5bf176289e`. Memory write/recall/withdrawal runs:
`45cf606c-df66-48d3-bc71-d0d1be066f5b`,
`4233d8cb-a6b0-473b-8db0-c0b49fbca65c`, and
`e064b3c0-f12e-4d31-9b54-206d2fa5a742`.
Detailed records remain in the isolated canary state, not the production store.

Native execution is functionally connected. These small tasks still took about
30–51 seconds with the existing profile. The first two AGH native-completion
spans were 50.720s and 33.715s; Offload reported 50.674s and 33.661s respectively.
The additional AGH time was about 46–54ms, so this sample's latency is primarily
inside native execution. Codex reported subscription usage without a USD cost;
no dollar amount is inferred.

The live check exposed Chat showing `0s`: Offload journal events did not populate
`elapsed_ms`. This change derives it from the saved run start and event timestamp,
clamping a backward clock to zero. Original records remain unchanged. A fixed-clock
regression and race checks cover the correction. Canary deployment verification
is retained with the runtime's deployment receipt.

This accepts the bounded CLI/Web and memory cases above. It does not establish
real DingTalk/Feishu delivery, desktop rendering, live cancellation or production
rollout acceptance. Host business acceptance remains independently unverified.

[Usage and configuration](offload-chat.md).
