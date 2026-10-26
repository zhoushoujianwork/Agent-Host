package feishu_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This is a separate Go module, not a package inside the Host repository. It
// proves consumers can compile and exercise the library without Host internals,
// its module graph, installed lark-cli credentials or a running Host service.
func TestIndependentConsumer(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate module")
	}
	moduleRoot := filepath.Dir(file)
	dir := t.TempDir()
	quotedRoot, _ := json.Marshal(moduleRoot)
	mod := "module independent-consumer\n\ngo 1.26.0\n\nrequire github.com/zhoushoujianwork/Agent-Host/plugins/feishu v0.0.0\nreplace github.com/zhoushoujianwork/Agent-Host/plugins/feishu => " + string(quotedRoot) + "\n"
	for name, body := range map[string]string{"go.mod": mod, "main.go": consumerSource} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "-mod=readonly", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "external-consumer-ok" {
		t.Fatalf("consumer: %v\n%s", err, out)
	}
}

const consumerSource = `package main
import (
 "context"
 "fmt"
 "strings"
 feishu "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"
)
type transport struct{}
func (transport) Run(ctx context.Context, args ...string) (feishu.Output,error) {
 raw := ""
 switch args[2] {
 case "whoami": raw = "{\"profile\":\"example\",\"appId\":\"cli_example\",\"identity\":\"bot\",\"available\":true}"
 case "vc":
  switch args[3] {
  case "+meeting-join": raw = "{\"ok\":true,\"data\":{\"meeting\":{\"id\":\"700000000000000001\",\"meeting_no\":\"123456789\"}}}"
  case "meeting": raw = "{\"ok\":true,\"data\":{\"meeting\":{\"id\":\"700000000000000001\",\"status\":2,\"participants\":[{\"id\":\"ou_bot\",\"status\":2}]}}}"
  case "+meeting-events": raw = "{\"ok\":true,\"data\":{\"identity\":{\"id\":\"ou_bot\"},\"events\":[{\"event_id\":\"event\",\"event_type\":\"chat_received\",\"payload\":{\"chat_received_items\":[{\"content\":\"hello\",\"operator\":{\"id\":\"ou_person\"}}]}}],\"page_token\":\"next\"}}"
  case "+meeting-message-send", "+meeting-leave": raw = "{\"ok\":true,\"data\":{}}"
  }
 }
 if raw == "" { panic(strings.Join(args," ")) }
 return feishu.Output{Stdout:[]byte(raw),Started:true},nil
}
func check(err error) { if err != nil { panic(err) } }
func main() {
 ctx := context.Background()
 c,err := feishu.NewMeetingClient(feishu.Config{Profile:"example",AppID:"cli_example",BotOpenID:"ou_bot"},transport{});check(err)
 check(c.CheckIdentity(ctx))
 joined,err := c.Join(ctx,feishu.JoinRequest{MeetingNumber:"123456789"});check(err)
 if joined.Outcome != feishu.Accepted { panic("join") }
 member,err := c.Membership(ctx,joined.MeetingID);check(err)
 if member.Status != "present" { panic("membership") }
 page,err := c.PollEvents(ctx,joined.MeetingID,"");check(err)
 items,err := page.Events[0].ChatItems();check(err)
 if items[0].Content != "hello" || page.Cursor != "next" { panic("events") }
 sent,err := c.SendText(ctx,joined.MeetingID,"persisted-uuid","consumer reply");check(err)
 if sent.Outcome != feishu.Accepted { panic("send") }
 left,err := c.Leave(ctx,joined.MeetingID);check(err)
 if left.Outcome != feishu.Accepted { panic("leave") }
 fmt.Println("external-consumer-ok")
}
`
