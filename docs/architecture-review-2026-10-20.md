# Architecture acceptance review — 2026-10-20

The shared Host path works for the capabilities exercised here. Real CLI/Web
execution demonstrated automatic fact retention, fresh-conversation recall,
MCP document retrieval and native Skill use. A separate real-model experiment
demonstrated automatic procedure publication and later Skill-guided execution.
There is no need to add a second automatic-memory mechanism.

[Evidence, reproduction and limits](architecture-review-2026-10-20-detail.md)
separate real-model observations from automated fixtures. This review follows
the [architecture](architecture.md), rather than treating planned work as delivered.

| Capability | Review result |
| --- | --- |
| Shared CLI/Web/DingTalk/Feishu behavior | Same Host boundary; new regression compares selected knowledge tools, native Skill availability and file-memory identity for CLI and both IM adapters. CLI/Web also exercised with real ACP. |
| Tools and plugins | Real MCP search/read and selected native Skill succeeded. A plugin SDK/marketplace remains outside scope. |
| Memory recall | A randomly marked fact saved through CLI was recalled through a new Web conversation using the same authorized Agent, audience and scope. |
| Automatic retention | User statements are selected after successful execution. Evidence-backed procedures are assessed and automatically published on the file backend. Both paths have real-model evidence. |
| Procedure reuse | Real extraction published a Skill; a fresh native session invoked it and made one correct read-only command call. |
| Continuity and failure handling | ACP/session, cancellation, replay exclusion, scope isolation and memory-only recovery have automated coverage; real Web continuation is included in the new probe. |

Automatic retention is selective: conversation input can become a fact,
preference or decision; correlated execution evidence can become a procedure.
Reading an article does not make it memory. Passive learning from arbitrary
external chats, general learning quality and sustained production usefulness
remain unverified or planned. Unattributed and joint-project runs intentionally
skip automatic learning; legacy SQLite procedures retain manual acceptance.

Shared behavior does not mean shared private conversations or identical
deployment configuration. The inspected production channels still select the
explicit Claude print backend; ACP is available through the common boundary.
Different Agent catalogs, audiences, sources and memory scopes can yield
different capabilities. No production channel was switched by this review.

Remaining acceptance: live DingTalk/Feishu ACP messages and delivery, broader
multi-user platform checks, a second engine, interactive channel controls and
long-term memory quality. Channel history tools require a verified channel
receipt and are intentionally unavailable to standalone CLI/Web conversations.

The review adds repeatable acceptance probes and corrects a stale backlog
description of the suspended member-permission workflow. Production data and
unrelated in-progress UI changes were preserved.
