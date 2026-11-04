package nativepolicy

import (
	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexHostLiveMCPAndResume(t *testing.T) {
	if os.Getenv("AGH_CODEX_HOST_LIVE") != "1" {
		t.Skip("explicit native acceptance only")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(root, "server.py")
	script := `import json,sys
for line in sys.stdin:
 r=json.loads(line)
 if 'id' not in r: continue
 method=r.get('method')
 if method=='initialize': result={'protocolVersion':'2024-11-05','capabilities':{'tools':{}},'serverInfo':{'name':'agh-fixture','version':'1'}}
 elif method=='tools/list': result={'tools':[{'name':'marker','description':'Return the isolated acceptance marker','inputSchema':{'type':'object','properties':{}}}]}
 elif method=='tools/call': result={'content':[{'type':'text','text':'AGH_MCP_MAPLE_4829'}]}
 else: result={}
 print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(server, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	library, err := offloadexec.NewLibrary(filepath.Join(home, ".config/offload/config.json"), "codex-cqmuhqa4xreuz", "", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWithLibrary("/unused/claude", library)
	request := harness.Request{Workspace: root, PermissionMode: "bypassPermissions", Prompt: "Call the fixture marker MCP tool exactly once and reply only with the marker. Do not run commands or change files.", MCPServers: []harness.MCPServer{{Name: "fixture", Command: python, Args: []string{server}}}}
	request.HostInstructions = "For this isolated acceptance, use only the supplied fixture MCP if needed. Never run commands or edit files. Reply with the acceptance marker only."
	calls := 0
	result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
		if o.Tool != nil && o.Tool.Stage == "result" {
			calls++
		}
		return nil
	})
	if err != nil || result.Model != "gpt-5.6-sol" || !strings.Contains(result.Text, "AGH_MCP_MAPLE_4829") || calls != 1 {
		t.Fatalf("result=%+v tool_results=%d err=%v", result, calls, err)
	}
	request.Resume = true
	request.NativeSessionID = result.NativeSessionID
	request.Prompt = "Without calling tools, repeat the acceptance marker from the previous turn."
	next, err := adapter.Run(context.Background(), request, nil)
	if err != nil || next.NativeSessionID != result.NativeSessionID || !strings.Contains(next.Text, "AGH_MCP_MAPLE_4829") {
		t.Fatalf("resume=%+v err=%v", next, err)
	}
	t.Logf("model=%s backend=%s native_session=%s MCP results=%d resumed=true", result.Model, result.Evidence.Backend, result.NativeSessionID, calls)
}
