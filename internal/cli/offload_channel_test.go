package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/harness"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
)

const (
	offloadTestOwner  = "owner-1"
	offloadTestTenant = "tenant-1"
	offloadTestWait   = 5 * time.Second
)

// offloadFakeScript records argv and stdin of every call and answers with one
// valid JSON record. A fresh run gets a new session; -r keeps its session.
func offloadFakeScript(dir string) string {
	return `#!/bin/sh
dir='` + dir + `'
n=$(cat "$dir/count" 2>/dev/null || echo 0)
n=$((n+1))
echo $n > "$dir/count"
printf '%s\n' "$@" > "$dir/argv.$n"
cat > "$dir/stdin.$n"
session="native-$n"
caller=""
while [ $# -gt 0 ]; do
  case "$1" in -r) session="$2"; shift ;; --caller-run-id) caller="$2"; shift ;; esac
  shift
done
printf '{"status":"ok","exit_code":0,"profile":"canonical","backend":"b","model":"m","session":"%s","result":"answer-%s","usage":{},"run_id":"offload-%s","caller_run_id":"%s"}\n' "$session" "$n" "$n" "$caller"
`
}

type offloadReplies struct {
	mu    sync.Mutex
	texts []string
}

func (r *offloadReplies) add(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
}

func (r *offloadReplies) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

func (r *offloadReplies) Reply(_ context.Context, _ string, _ time.Time, m dingtalk.ReplyMessage) (string, error) {
	r.add(m.Text)
	return "accepted", nil
}

func (r *offloadReplies) ReplyText(_ context.Context, _ state.ChannelReplyTarget, text string) (string, error) {
	r.add(text)
	return "accepted", nil
}

type offloadRig struct {
	t       *testing.T
	kind    string
	service *channel.Service
	store   *state.Store
	replies *offloadReplies
	dir     string
	o       options
}

func (r *offloadRig) argv(n int) string {
	b, err := os.ReadFile(filepath.Join(r.dir, fmt.Sprintf("argv.%d", n)))
	if err != nil {
		r.t.Fatalf("offload call %d did not happen: %v", n, err)
	}
	return "\n" + string(b)
}

func (r *offloadRig) stdin(n int) string {
	b, err := os.ReadFile(filepath.Join(r.dir, fmt.Sprintf("stdin.%d", n)))
	if err != nil {
		r.t.Fatalf("offload call %d has no stdin: %v", n, err)
	}
	return string(b)
}

func (r *offloadRig) calls() int {
	b, err := os.ReadFile(filepath.Join(r.dir, "count"))
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscan(string(b), &n)
	return n
}

// offloadOptions builds resolved Offload channel options with no Claude, Jev or
// provider credential available.
func offloadOptions(t *testing.T, kind, dir, workspace string) options {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	command := filepath.Join(dir, "offload")
	if err := os.WriteFile(command, []byte(offloadFakeScript(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "profile.json")
	writeBotJSON(t, profile, map[string]any{"version": 1, "command": command, "profile": "alias", "timeout": "30s"})
	entry := "dingtalk serve"
	if kind == "feishu" {
		entry = "feishu-meeting serve" // runFeishuMeeting parses both Feishu commands under this name
	}
	o, _, err := parse(entry, []string{"--offload-config", profile, "--workspace", workspace, "--home", filepath.Join(dir, "home")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o.binary = filepath.Join(dir, "absent-claude")
	o.timeout = 10 * time.Second
	if err := resolveOffloadChannelOptions(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func newOffloadRig(t *testing.T, kind string) *offloadRig {
	t.Helper()
	dir := canonicalTemp(t)
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return newOffloadRigWith(t, kind, dir, workspace, nil)
}

func newOffloadRigWith(t *testing.T, kind, dir, workspace string, store *state.Store) *offloadRig {
	t.Helper()
	o := offloadOptions(t, kind, dir, workspace)
	if store == nil {
		var err error
		store, err = state.Open(filepath.Join(dir, "state"), false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	replies := &offloadReplies{}
	var service *channel.Service
	if kind == "dingtalk" {
		service = &channel.Service{Store: store, ClientID: "app", OwnerID: offloadTestOwner, Replier: replies, Reactor: cliReactor{}}
	} else {
		service = &channel.Service{Channel: state.ReplyTargetFeishu, Store: store, ClientID: "app", OwnerID: offloadTestOwner, TextReplier: replies, DisableReactions: true}
	}
	configureOffloadChannel(service, o, offloadTestTenant, nil)
	if err := service.Validate(); err != nil {
		t.Fatal(err)
	}
	return &offloadRig{t: t, kind: kind, service: service, store: store, replies: replies, dir: dir, o: o}
}

// receive submits one authenticated message through the real transport entry
// and returns the durable channel message ID.
func (r *offloadRig) receive(id, sender, conversation, conversationType, text string) string {
	r.t.Helper()
	ctx := context.Background()
	if r.kind == "dingtalk" {
		m := dingtalk.Message{ID: id, ConversationID: conversation, ConversationType: conversationType, SenderID: sender, TenantID: offloadTestTenant,
			MessageType: "text", Text: text, ReplyURL: "https://oapi.dingtalk.com/robot/sendBySession?session=fixture", ReplyExpiresAt: time.Now().Add(time.Hour)}
		if err := r.service.Receive(ctx, m); err != nil {
			r.t.Fatal(err)
		}
		return channel.Key("dingtalk", r.service.ClientID, id)
	}
	m := channel.Message{ID: "om_" + id, ConversationID: conversation, ConversationType: conversationType, SenderID: sender, TenantID: offloadTestTenant,
		MessageType: "text", Text: text, CreatedAt: time.Now(), ReplyTarget: state.ChannelReplyTarget{Kind: state.ReplyTargetFeishu, ID: conversation, MessageID: "om_" + id}}
	if err := r.service.ReceiveMessage(ctx, m); err != nil {
		r.t.Fatal(err)
	}
	return channel.Key(state.ReplyTargetFeishu, r.service.ClientID, offloadTestTenant, conversation, m.ID)
}

// work runs the real Work loop in the background; the returned function cancels
// and joins it, so no goroutine outlives the test.
func startOffloadWork(t *testing.T, s *channel.Service) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Work(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("worker stopped with %v", err)
				}
			case <-time.After(offloadTestWait):
				t.Error("worker was not joined after cancellation")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (r *offloadRig) waitReplies(n int) {
	r.t.Helper()
	deadline := time.Now().Add(offloadTestWait)
	for time.Now().Before(deadline) {
		if len(r.replies.snapshot()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("expected %d replies, got %q", n, r.replies.snapshot())
}

func (r *offloadRig) run(messageID string) (state.ChannelMessage, state.Run) {
	r.t.Helper()
	ctx := context.Background()
	m, err := r.service.Store.ChannelMessage(ctx, messageID)
	if err != nil || m.RunID == "" {
		r.t.Fatalf("message has no run: %+v %v", m, err)
	}
	run, err := r.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(ctx, m.RunID)
	if err != nil {
		r.t.Fatal(err)
	}
	return m, run
}

func TestOffloadChannelFactoryAndNoLegacyRequirements(t *testing.T) {
	dir := canonicalTemp(t)
	write(t, dir, "lark-cli", "#!/bin/sh\nexit 91\n", 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	o := offloadOptions(t, "dingtalk", dir, workspace)
	if o.binary == "" || !strings.HasPrefix(o.runtimeFingerprint, offloadchannel.FingerprintPrefix) || o.model != "" {
		t.Fatalf("unexpected resolved options: %+v", o)
	}
	store, err := state.Open(filepath.Join(dir, "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h, ok := composeOffloadChannelHost(store, o, nil).(*offloadchannel.Host)
	if !ok || h.Client == nil || !h.Config.Write || h.Config.Workspace != workspace || h.Config.RuntimeFingerprint != o.runtimeFingerprint {
		t.Fatalf("factory did not return the thin Offload host: %#v", h)
	}
	for _, kind := range []string{"dingtalk", "feishu"} {
		var out strings.Builder
		var err error
		if kind == "dingtalk" {
			err = runOffloadDingTalk(context.Background(), "check", dingtalk.Config{}, o, &out)
		} else {
			err = runOffloadFeishu(context.Background(), "check", feishumeeting.Config{IMChat: true}, o, &out)
		}
		if err != nil || !strings.Contains(out.String(), `"engine":"offload"`) || !strings.Contains(out.String(), `"memory_enabled":false`) {
			t.Fatalf("%s check: %q %v", kind, out.String(), err)
		}
	}
	if _, err := os.Stat(o.home); !os.IsNotExist(err) {
		t.Fatalf("check created Host state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "count")); !os.IsNotExist(err) {
		t.Fatal("check launched Offload")
	}
}

func TestOffloadChannelTwoTurnsReuseNativeSessionAndCanonicalProfile(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			r := newOffloadRig(t, kind)
			stop := startOffloadWork(t, r.service)
			first := r.receive("m1", offloadTestOwner, "dm-1", "1", "first question")
			r.waitReplies(1)
			second := r.receive("m2", offloadTestOwner, "dm-1", "1", "follow up")
			r.waitReplies(2)
			stop()

			if got := r.replies.snapshot(); !strings.Contains(got[0], "answer-1") || !strings.Contains(got[1], "answer-2") {
				t.Fatalf("replies = %q", got)
			}
			m1, run1 := r.run(first)
			m2, run2 := r.run(second)
			if run1.SessionID != run2.SessionID || m1.SessionID != m2.SessionID {
				t.Fatalf("turns used different Host sessions: %s %s", run1.SessionID, run2.SessionID)
			}
			if run1.NativeSessionID != "native-1" || run2.NativeSessionID != "native-1" {
				t.Fatalf("native sessions = %q %q", run1.NativeSessionID, run2.NativeSessionID)
			}
			for _, run := range []state.Run{run1, run2} {
				if run.ExecutionStatus != "completed" || run.AcceptanceStatus != "unverified" || run.MemoryStatus != "skipped" || !run.MemoryPluginDisabled || run.Agent != "offload" {
					t.Fatalf("run state = exec %q accept %q memory %q disabled %v agent %q", run.ExecutionStatus, run.AcceptanceStatus, run.MemoryStatus, run.MemoryPluginDisabled, run.Agent)
				}
			}

			for index, run := range []state.Run{run1, run2} {
				dispatched, returned := false, false
				for _, e := range run.Events {
					if e.Type == "agent.dispatched" && e.Data["caller_run_id"] == "agh:"+run.ID {
						dispatched = true
					}
					if e.Type == "offload.result" && e.Data["caller_run_id"] == "agh:"+run.ID && e.Data["reported_caller_run_id"] == "agh:"+run.ID && e.Data["offload_run_id"] == fmt.Sprintf("offload-%d", index+1) && e.Data["raw"] != nil {
						returned = true
					}
				}
				if !dispatched || !returned {
					t.Fatal("correlation missing from durable dispatch/result evidence")
				}
			}
			first1, second1 := r.argv(1), r.argv(2)
			if run1.ID == run2.ID || !strings.Contains(first1, "\n--caller-run-id\nagh:"+run1.ID+"\n") || !strings.Contains(second1, "\n--caller-run-id\nagh:"+run2.ID+"\n") {
				t.Fatal("missing distinct per-turn correlation")
			}
			if !strings.Contains(first1, "\n-p\nalias\n") || !strings.Contains(first1, "\n-w\n") || strings.Contains(first1, "\n-r\n") || !strings.Contains(first1, "\n-no-salvage\n") {
				t.Fatalf("first argv = %q", first1)
			}
			if !strings.Contains(second1, "\n-p\ncanonical\n") || strings.Contains(second1, "\nalias\n") || !strings.Contains(second1, "\n-r\nnative-1\n") || !strings.Contains(second1, "\n-w\n") {
				t.Fatalf("continuation argv = %q", second1)
			}
			for i, text := range []string{"first question", "follow up"} {
				if strings.Contains(r.argv(i+1), text) || !strings.Contains(r.stdin(i+1), text) {
					t.Fatalf("call %d prompt not exclusively on stdin", i+1)
				}
			}
			if r.calls() != 2 {
				t.Fatalf("offload calls = %d", r.calls())
			}
		})
	}
}

func TestOffloadChannelGroupAudienceIsDistinctAndOwnerEquivalent(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			r := newOffloadRig(t, kind)
			stop := startOffloadWork(t, r.service)
			private := r.receive("p1", offloadTestOwner, "dm-1", "1", "private task")
			r.waitReplies(1)
			group := r.receive("g1", "member-2", "group-1", "2", "group task")
			r.waitReplies(2)
			stop()

			wantAudience := channel.Key("group-audience-v1", map[string]string{"dingtalk": "dingtalk", "feishu": state.ReplyTargetFeishu}[kind], "app", offloadTestTenant, "group-1")
			pm, pr := r.run(private)
			gm, gr := r.run(group)
			if pm.AudienceID != state.OwnerAudienceID || pr.AudienceID != state.OwnerAudienceID {
				t.Fatalf("private audience = %q / %q", pm.AudienceID, pr.AudienceID)
			}
			if gm.AudienceID != wantAudience || gr.AudienceID != wantAudience || gr.AudienceID == pr.AudienceID {
				t.Fatalf("group audience = %q / %q, want %q", gm.AudienceID, gr.AudienceID, wantAudience)
			}
			if pr.SessionID == gr.SessionID || pr.NativeSessionID == gr.NativeSessionID {
				t.Fatal("group and private conversations shared a session")
			}
			// Partition is not a restriction: a non-owner group sender is owner-equivalent.
			if in := r.stdin(2); !strings.Contains(in, `"is_owner":false`) || !strings.Contains(in, `"execution_access":"owner-equivalent"`) || !strings.Contains(in, "member-2") {
				t.Fatalf("group stdin lacks authenticated provenance: %q", in)
			}
			if in := r.stdin(1); !strings.Contains(in, `"is_owner":true`) {
				t.Fatalf("owner stdin lacks owner provenance: %q", in)
			}
		})
	}
}

func TestOffloadChannelRejectsTamperedPartition(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			r := newOffloadRig(t, kind)
			channelKind := map[string]string{"dingtalk": "dingtalk", "feishu": state.ReplyTargetFeishu}[kind]
			group := channel.Key("group-audience-v1", channelKind, "app", offloadTestTenant, "group-1")
			valid := state.ChannelMessage{Channel: channelKind, AccountID: "app", TenantID: offloadTestTenant, ConversationID: "group-1", ConversationType: "2", SenderID: "member-2", AgentID: "default", AudienceID: group}
			child, err := r.service.ResolveAgent(context.Background(), valid)
			if err != nil {
				t.Fatal(err)
			}
			host, ok := child.Host.(*offloadchannel.Host)
			if !ok || host.Config.AudienceID != group || host.Config.AgentID != "default" || child.ResolveAgent != nil {
				t.Fatalf("resolved child = %#v", child.Host)
			}
			private := valid
			private.ConversationType, private.SenderID, private.AudienceID = "1", offloadTestOwner, state.OwnerAudienceID
			if _, err := r.service.ResolveAgent(context.Background(), private); err != nil {
				t.Fatalf("authentic owner private rejected: %v", err)
			}
			tampered := map[string]func(*state.ChannelMessage){
				"owner audience on group": func(m *state.ChannelMessage) { m.AudienceID = state.OwnerAudienceID },
				"other group audience": func(m *state.ChannelMessage) {
					m.AudienceID = channel.Key("group-audience-v1", channelKind, "app", offloadTestTenant, "group-2")
				},
				"empty audience":            func(m *state.ChannelMessage) { m.AudienceID = "" },
				"other agent":               func(m *state.ChannelMessage) { m.AgentID = "intruder" },
				"other account":             func(m *state.ChannelMessage) { m.AccountID = "other-app" },
				"other tenant":              func(m *state.ChannelMessage) { m.TenantID = "other-tenant" },
				"other channel":             func(m *state.ChannelMessage) { m.Channel = "unknown" },
				"group audience on private": func(m *state.ChannelMessage) { m.ConversationType, m.SenderID = "1", offloadTestOwner },
				"non-owner private":         func(m *state.ChannelMessage) { m.ConversationType, m.AudienceID = "1", state.OwnerAudienceID },
			}
			for name, mutate := range tampered {
				m := valid
				mutate(&m)
				if child, err := r.service.ResolveAgent(context.Background(), m); err == nil {
					t.Errorf("%s: accepted with %#v", name, child.Host)
				}
			}
			if r.calls() != 0 {
				t.Fatal("rejection launched Offload")
			}
		})
	}
}

func TestOffloadChannelChangedFingerprintCannotClaimOldQueuedMessage(t *testing.T) {
	for _, kind := range []string{"dingtalk", "feishu"} {
		t.Run(kind, func(t *testing.T) {
			old := newOffloadRig(t, kind)
			id := old.receive("m1", offloadTestOwner, "dm-1", "1", "queued under the old profile")

			dir := canonicalTemp(t)
			otherWorkspace := filepath.Join(dir, "other-workspace")
			if err := os.Mkdir(otherWorkspace, 0o700); err != nil {
				t.Fatal(err)
			}
			changed := newOffloadRigWith(t, kind, dir, otherWorkspace, old.store)
			if changed.o.runtimeFingerprint == old.o.runtimeFingerprint || changed.service.Profile == old.service.Profile {
				t.Fatal("changed configuration kept its queue identity")
			}
			stop := startOffloadWork(t, changed.service)
			time.Sleep(1500 * time.Millisecond)
			stop()
			m, err := old.store.ChannelMessage(context.Background(), id)
			if err != nil || m.Status != "queued" || m.RunID != "" || changed.calls() != 0 || old.calls() != 0 || len(changed.replies.snapshot()) != 0 {
				t.Fatalf("changed profile claimed old work: %+v %v calls=%d", m, err, changed.calls())
			}
			// The original configuration still owns and completes it.
			stop = startOffloadWork(t, old.service)
			old.waitReplies(1)
			stop()
			if old.calls() != 1 || changed.calls() != 0 {
				t.Fatalf("calls old=%d changed=%d", old.calls(), changed.calls())
			}
		})
	}
}

func TestOffloadChannelRejectsUnsupportedSettingsBeforeLaunch(t *testing.T) {
	dir := canonicalTemp(t)
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	base := offloadOptions(t, "dingtalk", dir, workspace)
	cases := map[string]func(*options){
		"no tools":             func(o *options) { o.noTools = true },
		"allowed tools":        func(o *options) { o.allowedTools = []string{"Read"} },
		"debug non-owner":      func(o *options) { o.debugNonOwner = true },
		"permission mode":      func(o *options) { o.permissionMode = "default" },
		"agents catalog":       func(o *options) { o.agentsConfig = "/agents.json" },
		"project":              func(o *options) { o.projectID = "project" },
		"project set":          func(o *options) { o.projectIDs = []string{"a", "b"} },
		"primary project":      func(o *options) { o.primaryProject = "a" },
		"additional directory": func(o *options) { o.additionalDirectories = []string{"/extra"} },
		"email":                func(o *options) { o.emailConfig = "/email.json" },
		"knowledge":            func(o *options) { o.knowledgeConfig = "/knowledge.json" },
		"knowledge set":        func(o *options) { o.knowledgeConfigs = []string{"/knowledge.json"} },
		"dingtalk workspace":   func(o *options) { o.dingtalkWorkspaceConfig = "/dws.json" },
		"discovery":            func(o *options) { o.discoveryConfig = "/discovery.json" },
		"managed skills":       func(o *options) { o.skillsRoot = "/skills" },
		"mcp":                  func(o *options) { o.mcpServers = []harness.MCPServer{{}} },
		"plugins":              func(o *options) { o.pluginNames = []string{"email"} },
		"non-owner audience":   func(o *options) { o.audienceID = "group" },
		"missing profile":      func(o *options) { o.offloadProfile = nil },
		"missing client":       func(o *options) { o.offloadClient = nil },
		"relative workspace":   func(o *options) { o.workspace = "relative" },
		"absent workspace":     func(o *options) { o.workspace = filepath.Join(dir, "absent") },
	}
	for name, mutate := range cases {
		o := base
		mutate(&o)
		if err := resolveOffloadChannelOptions(&o); err == nil {
			t.Errorf("%s: accepted", name)
		}
		var out strings.Builder
		if err := runOffloadDingTalk(context.Background(), "serve", dingtalk.Config{}, o, &out); err == nil {
			t.Errorf("%s: serve accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "count")); !os.IsNotExist(err) {
		t.Fatal("an unsupported setting launched Offload")
	}
	if _, err := os.Stat(base.home); !os.IsNotExist(err) {
		t.Fatalf("an unsupported setting created Host state: %v", err)
	}
}

func TestOffloadChannelRejectsFeishuMeetingCombinations(t *testing.T) {
	dir := canonicalTemp(t)
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	o := offloadOptions(t, "feishu", dir, workspace)
	cases := map[string]struct {
		command string
		cfg     feishumeeting.Config
	}{
		"meeting only":     {"serve", feishumeeting.Config{}},
		"IM with meeting":  {"serve", feishumeeting.Config{IMChat: true, MeetingID: "m1"}},
		"IM with autojoin": {"serve", feishumeeting.Config{IMChat: true, AutoJoin: true}},
		"probe":            {"probe", feishumeeting.Config{IMChat: true}},
	}
	for name, tc := range cases {
		var out strings.Builder
		err := runOffloadFeishu(context.Background(), tc.command, tc.cfg, o, &out)
		if err == nil || !strings.Contains(err.Error(), "IM text only") || out.Len() != 0 {
			t.Errorf("%s: %v %q", name, err, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "count")); !os.IsNotExist(err) {
		t.Fatal("meeting combination launched Offload")
	}
	if _, err := os.Stat(o.home); !os.IsNotExist(err) {
		t.Fatalf("meeting combination created Host state: %v", err)
	}
}
