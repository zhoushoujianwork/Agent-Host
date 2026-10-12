# Runtime knowledge acceptance

This runner performs one real Claude task against the configured Dokki and
Confluence accounts. It searches and reads bounded excerpts of one returned
resource from each, checks source attribution and answer citations, and tests a
unique empty keyword query. It also exercises each MCP subprocess's initialization
and tool catalog.
No-prefetch network behavior is proven by local connector tests, not by the live
protocol check.

Each first read requests 2,000 Unicode characters. A truncated first excerpt
requires exactly one continuation using the returned offset and full-content
digest. Validation checks advancing windows and consistent content; the Agent
does not need to read an entire large document. The connector currently fetches
the upstream body for each window and verifies the digest before returning an
excerpt. All validated results must appear inline in the Host's recorded tool
evidence. Native private-session files are
not used to reconstruct oversized output.

Build the current binary and run repository checks first. Then invoke:

```sh
python3 experiments/knowledge/run.py \
  --runtime-root ~/.agent-host/production \
  --source-credentials /absolute/private/path/knowledge-sources.json \
  --output experiments/knowledge/results/live-YYYY-MM-DD.json
```

The source credential input is memgov's private combined JSON with
`dokki.api_key` and `confluence.pat`. The runner copies only those fields into
temporary private files. It reads the installed runtime's Claude executable,
model and provider configuration without changing production. Native hooks and
automatic memory are disabled in a separate profile. There are no retries,
provider substitutions, deployments or messages to other users.

Choose a fresh report path for every attempt. Reports contain structural metrics
and fixed failure codes only; they exclude document content, titles, citation
URLs, credentials and local paths. Raw stdout, stderr, native state and the Host
database remain in the private temporary directory printed at completion,
outside the repository. Temporary credential copies are removed afterwards.
Keep that evidence private and remove it when no longer needed.

A passing result requires paired successful native calls, attributed retrieval
receipts, reads of actual search-result IDs, source citations, scoped empty
results, and zero experience candidates or published execution experience. It
proves this bounded live path; it does not establish search completeness, general
answer quality or performance at scale.

## Recorded acceptance, 2026-10-10

The first attempt (private validation evidence) completed native execution but
failed acceptance: a large Dokki response was externalized by Claude, leaving
only a truncated preview in Host tool evidence. The connector was changed to
return compact search results and bounded read excerpts with explicit coverage
and digest-pinned continuation. No native private-session reconstruction counts
as source evidence.

The second actual run (private validation evidence) passed on binary SHA-256
`c0b656c13f4be7345d83ffc65a397845e56a84da67eff10a35029e719ab6a899`, Host schema 7,
and Claude Code 2.1.223. Its 83.57-second native task produced seven successful
paired calls with inline source receipts. Both sources were searched, read and
cited; both unique keyword queries returned scoped empty windows. Dokki returned
two consecutive 2,000-character excerpts from 351,924 characters, with the same
full-content digest. Confluence returned its 1,315-character body. No experience
candidates or published execution experience were created. There were no task
retries or provider substitutions.

After that live build, configuration validation was tightened to reject
`source_url_base` for Confluence; it is supported only for Dokki. That negative
configuration case was validated offline. Source HTTP, tool dispatch and
retrieval code were unchanged, so the live report remains evidence for those
paths rather than a claim that every later binary byte was exercised live.
