# Codex Host execution

AGH can consume native Codex events through the Offload package while retaining
Host tasks, routing, MCP evidence and memory ownership. The implementation is
validated against a pinned companion Offload commit; production adoption remains
pending.

The initial supported path is an owner request with explicit bypass permissions,
stdio MCP servers and ordinary session continuation. Codex generates its session
identity; AGH binds that identity atomically before recording further evidence.
Requested and observed models remain separate, and reported actual models come
from Offload native evidence.

Tool-free extraction, native tool allowlists, restricted member execution, Skills,
images, structured extraction and native forks are not yet supported by the
companion controls implementation. They fail before provider dispatch. Do not
remove production Claude bindings until the capabilities used by that deployment
have equivalent enforcement and acceptance evidence.

The module pins the companion Offload commit by its immutable Go pseudo-version;
no absolute dependency replacement belongs in the committed module. Real
MCP/resume acceptance is opt-in via `AGH_CODEX_HOST_LIVE=1` and
`TestCodexHostLiveMCPAndResume`; it uses the existing configured account and a
temporary marker-only MCP server.
