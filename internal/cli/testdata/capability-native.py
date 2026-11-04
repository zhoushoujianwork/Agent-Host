#!/usr/bin/env python3
"""Protocol fixture: real Host/MCP/Offload, deterministic native model output."""
import json
import pathlib
import subprocess
import sys

args = sys.argv[1:]
if args == ["--version"]:
    print("2.1.277 (Claude Code)")
    sys.exit(0)
if args == ["--help"]:
    print("--plugin-dir <path>")
    sys.exit(0)


def flag(name):
    return args[args.index(name) + 1]


session = flag("--session-id")
prompt = sys.stdin.read()
instructions = pathlib.Path(flag("--append-system-prompt-file")).read_text()
assert "KNOWLEDGE_CANARY" not in prompt + instructions, "document eagerly injected"
plugin = pathlib.Path(flag("--plugin-dir"))
assert any("Run go test ./... and inspect the report." in p.read_text() for p in plugin.rglob("SKILL.md")), "memory Skill absent"
mounts = json.loads(flag("--mcp-config"))
if isinstance(mounts, str):
    mounts = json.loads(pathlib.Path(mounts).read_text())
servers = mounts["mcpServers"]
assert "--strict-mcp-config" in args
selected = [(name, value) for name, value in servers.items() if name.startswith("knowledge_")]
assert len(selected) == 1, "unexpected knowledge mounts"
name, server = selected[0]
child = subprocess.Popen([server["command"], *server["args"]], stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


def rpc(number, method, params):
    child.stdin.write(json.dumps({"jsonrpc": "2.0", "id": number, "method": method, "params": params}) + "\n")
    child.stdin.flush()
    response = json.loads(child.stdout.readline())
    assert response.get("id") == number and "error" not in response, "MCP protocol failed"
    return response["result"]


def emit(value):
    print(json.dumps(value), flush=True)


try:
    rpc(1, "initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "acceptance-fixture", "version": "1"}})
    tools = rpc(2, "tools/list", {})["tools"]
    assert {tool["name"] for tool in tools} == {"search_local_markdown", "read_local_markdown"}
    emit({"type": "system", "subtype": "init", "session_id": session, "model": "fixture",
          "mcp_servers": [{"name": n, "status": "connected"} for n in servers]})
    calls = [
        ("search_local_markdown", {"query": "KNOWLEDGE_CANARY", "limit": 3}),
        ("read_local_markdown", {"path": "guide.md", "limit": 512}),
        ("read_local_markdown", {"path": "../outside.md", "limit": 512}),
    ]
    for i, (tool, arguments) in enumerate(calls, 3):
        result = rpc(i, "tools/call", {"name": tool, "arguments": arguments})
        encoded = json.dumps(result)
        if i == 5:
            assert result.get("isError"), "path escape accepted"
        else:
            assert not result.get("isError") and "KNOWLEDGE_CANARY" in encoded
            assert "EXCLUDED_CANARY" not in encoded, "unselected file exposed"
        emit({"type": "assistant", "session_id": session, "message": {"content": [
            {"type": "tool_use", "id": str(i), "name": "mcp__" + name + "__" + tool, "input": arguments}]}})
        emit({"type": "user", "session_id": session, "message": {"content": [
            {"type": "tool_result", "tool_use_id": str(i), "content": encoded, "is_error": bool(result.get("isError"))}]}})
finally:
    child.stdin.close()
    assert child.wait(timeout=5) == 0

# The same run-bound MCP capability exposes durable memory on demand.
server = servers["host_context"]
child = subprocess.Popen([server["command"], *server["args"]], stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
try:
    rpc(6, "initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "acceptance-fixture", "version": "1"}})
    tools = rpc(7, "tools/list", {})["tools"]
    assert {t["name"] for t in tools} == {"search_context", "read_context", "read_context_evidence"}
    result = rpc(8, "tools/call", {"name": "search_context", "arguments": {"query": "fixture test package", "kind": "memory", "limit": 5}})
    items = result["structuredContent"]["items"]
    assert len(items) == 1 and items[0]["reference"] == "memory/reviewed-memory/1"
    for i, (tool, arguments, fail) in enumerate([
        ("read_context", {"reference": items[0]["reference"]}, False),
        ("read_context_evidence", {"reference": items[0]["reference"]}, False),
        ("read_context", {"reference": "memory/reviewed-memory/999"}, True),
    ], 9):
        result = rpc(i, "tools/call", {"name": tool, "arguments": arguments})
        assert bool(result.get("isError")) == fail, "memory revision/evidence boundary failed"
        emit({"type": "assistant", "session_id": session, "message": {"content": [
            {"type": "tool_use", "id": str(i), "name": "mcp__host_context__" + tool, "input": arguments}]}})
        emit({"type": "user", "session_id": session, "message": {"content": [
            {"type": "tool_result", "tool_use_id": str(i), "content": json.dumps(result), "is_error": fail}]}})
finally:
    child.stdin.close()
    assert child.wait(timeout=5) == 0

emit({"type": "result", "subtype": "success", "session_id": session, "is_error": False,
      "result": "Capability fixture read knowledge and retained scoped memory; no external operation performed."})
