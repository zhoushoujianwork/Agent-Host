# Codex Host execution

AGH can consume native Codex events through the Offload package while retaining
Host tasks, routing, MCP evidence and memory ownership. The implementation is
validated against the published Offload module. Runtime adoption and channel
acceptance are recorded separately from source validation.

The owner path explicitly enables the Codex app-server control. Codex runs
with a fresh, private cwd in the environment bound to the stable Host session;
the task workspace and additional directories are passed as explicit resource
roots. Codex generates the real thread identity, and AGH adopts it atomically.
Continuation requires that same Host environment and thread; a missing or
incompatible environment is an error, not permission to create a replacement.

Host-selected Skills come only from managed profile roots and a digest-verified
task package. Their resolved roots are passed explicitly to app-server; ambient
native settings, credentials, memory and old sessions are not imported. MCP tool
calls/results and raw RPC method/parameter payloads remain native evidence.
Requested and observed models stay separate, and the reported actual model comes
from Offload-observed app-server evidence.

Codex app-server does not currently satisfy strict tool-free extraction or
restricted member execution, so those requests fail before provider dispatch;
they do not fall back to Claude. Tool-free memory extraction is a separate
internal request using an explicitly configured compatible Claude profile and a
fresh disposable environment without business Skills, MCP or additional roots.
Codex image input and native tool allowlists also remain unsupported and fail
before dispatch; supported Claude Agent configurations remain available explicitly.
Native forks are unsupported for Codex and Claude on this isolated path because
the original parent environment cannot yet be bound to a child conversation.
Do not remove production Claude bindings until the capabilities used by that
deployment have equivalent enforcement and acceptance evidence.

The module pins a published companion Offload module version;
no absolute dependency replacement belongs in the committed module. Real
MCP/resume acceptance is opt-in via `AGH_CODEX_HOST_LIVE=1` and
`TestCodexHostLiveMCPAndResume`; it uses the existing configured account and a
temporary marker-only MCP server.
