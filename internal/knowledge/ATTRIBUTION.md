# memgov connector attribution

`client.go` and `mcp.go` adapt code from
[memgov](https://github.com/zhoushoujianwork/memgov), revision
`8300e2a535690b2eef2e1ff01b851bb6189942bb`:

- `plugins/data/knowledge/client.go`
- `plugins/data/knowledge/mcp.go`

The original code is licensed under Apache License 2.0; the complete license is
included in [memgov-LICENSE](memgov-LICENSE). No upstream `NOTICE` file was present
in the inspected source tree.

Agent-Host modifies these files to expose a standalone, read-only Dokki and
Confluence connector. Source configuration and credentials are resolved locally
for each operation against a pinned launch binding. Advanced Confluence dispatch,
exports and memgov runtime/database integration are excluded. The modified source
files identify their origin and changes in their headers.
