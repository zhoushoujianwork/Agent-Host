package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/host"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func groupInput(t *testing.T, f *serviceFixture, id, sender, group, text string) dingtalk.Message {
	t.Helper()
	m := f.message(t, id, text, "text", group)
	m.ConversationType, m.SenderID = "2", sender
	return m
}

// Wire the same callback boundary as the CLI while retaining a deterministic
// native harness. No platform fixtures or real identities are used here.
func instanceDispatch(t *testing.T, f *projectFixture, bindings map[string]string) {
	t.Helper()
	root := f.service.Store
	profile := t.TempDir()
	base := *f.service
	config := f.host.Config
	f.service.SelectAgent = func(_ context.Context, m dingtalk.Message) (AgentSelection, error) {
		a := "default"
		if bindings[m.ConversationID] != "" {
			a = bindings[m.ConversationID]
		}
		audience := "owner"
		if m.ConversationType == "2" {
			audience = Key("group", m.TenantID, m.ConversationID)
		}
		return AgentSelection{AgentID: a, AudienceID: audience}, nil
	}
	f.service.ResolveAgent = func(_ context.Context, m state.ChannelMessage) (*Service, error) {
		child := base
		child.Store = root.WithPartition(m.AgentID, m.AudienceID)
		child.Projects = func(ctx context.Context) ([]state.Project, error) {
			projects, err := root.Projects(ctx)
			if err != nil {
				return nil, err
			}
			if m.AgentID == "support" {
				return []state.Project{f.projects["alpha"]}, nil
			}
			return projects, nil
		}
		child.ResolveProjects = func(ctx context.Context, ids []string, primary string) (Host, assembly.Runtime, error) {
			var projects []state.ProjectRef
			for _, id := range ids {
				p, err := root.Project(ctx, id)
				if err != nil {
					return nil, assembly.Runtime{}, err
				}
				projects = append(projects, state.ProjectRef{ID: p.ID, Name: p.Name, Workspace: p.Workspace, Scope: p.Scope})
			}
			runtime, err := assembly.AssembleProjects(projects, primary, config.Scope, config.Scope, assembly.Configuration{Model: config.Model, ToolsDisabled: true, NativeProfile: profile})
			if err != nil {
				return nil, runtime, err
			}
			c := config
			c.AgentID, c.AudienceID = m.AgentID, m.AudienceID
			c.Scope, c.Workspace, c.ProjectID, c.ProjectName = runtime.Scope, runtime.Workspace, runtime.ProjectID, runtime.ProjectName
			c.RuntimeFingerprint, c.Projects, c.AdditionalDirectories = runtime.Fingerprint, runtime.Projects, runtime.AdditionalDirectories
			return &host.Host{Store: child.Store, Config: c, Runner: f.runner, Decider: f.host.Decider, Objects: f.host.Objects}, runtime, nil
		}
		child.Host = nil
		child.ResolveHost = nil
		return &child, nil
	}
}

func TestAgentDispatchSharesGroupAcrossMembersAndIsolatesOtherAudiences(t *testing.T) {
	f := newRoutingFixture(t)
	bindings := map[string]string{}
	instanceDispatch(t, f, bindings)
	process := func(id, sender, group, text string) state.ChannelMessage {
		m := f.process(t, groupInput(t, f.serviceFixture, id, sender, group, text))
		if m.Status != "completed" {
			t.Fatalf("group task failed: %+v", m)
		}
		return m
	}
	a := process("g1-a", "alice", "g1", "Inspect alpha")
	b := process("g1-b", "bob", "g1", "continue")
	c := process("g1-c", "alice", "g1", "continue")
	other := process("g2-a", "alice", "g2", "Inspect alpha")
	if a.Binding != b.Binding || b.Binding != c.Binding || b.SessionID != c.SessionID || b.SessionID == other.SessionID || b.AudienceID == other.AudienceID {
		t.Fatalf("group sharing or isolation failed: b=%+v c=%+v other=%+v", b, c, other)
	}
	dm := f.process(t, f.message(t, "owner-task", "Inspect alpha", "text", "owner-dm"))
	if dm.Status != "completed" || dm.SessionID == b.SessionID || dm.AudienceID != "owner" {
		t.Fatalf("DM leaked into group: %+v", dm)
	}
	process("reset", "bob", "g1", "/new")
	fresh := process("g1-fresh", "alice", "g1", "Inspect alpha")
	otherAgain := process("g2-again", "bob", "g2", "Inspect alpha")
	if fresh.SessionID == b.SessionID || otherAgain.SessionID != other.SessionID {
		t.Fatal("group reset affected wrong session")
	}
	bindings["g1"] = "support"
	switched := process("g1-support", "alice", "g1", "Inspect alpha")
	if switched.AgentID != "support" || switched.SessionID == fresh.SessionID || switched.Binding == fresh.Binding {
		t.Fatal("agent reassignment reused the old conversation")
	}
	before := len(f.runner.snapshot())
	f.receive(t, groupInput(t, f.serviceFixture, "g1-b", "bob", "g1", "continue"))
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate was requeued", err)
	}
	saved := f.receipt(t, "g1-b")
	if saved.AgentID != "default" || len(f.runner.snapshot()) != before {
		t.Fatal("redelivery changed the original agent or executed again")
	}
	for _, m := range []state.ChannelMessage{b, c, other, dm, fresh, switched} {
		run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(context.Background(), m.RunID)
		if err != nil || run.AgentID != m.AgentID || run.AudienceID != m.AudienceID || run.Source == nil || run.Source.SenderID != m.SenderID {
			t.Fatalf("run lost persisted actor/partition: %+v %v", run, err)
		}
		if run.Model != "" && !strings.Contains(m.ReplyText, "🤖 "+run.Model) {
			t.Fatal("public reply omitted actual model", m.ReplyText)
		}
		for _, hidden := range []string{run.SessionID, run.Workspace} {
			if hidden != "" && strings.Contains(m.ReplyText, hidden) {
				t.Fatal("public reply leaked runtime detail", m.ReplyText)
			}
		}
	}
}

func TestGroupClarificationRemainsOwnedByOriginalRequester(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	first := legacyPending(t, f, groupInput(t, f.serviceFixture, "ask", "alice", "g1", "ambiguous request"))
	if first.Status != "awaiting" {
		t.Fatalf("clarification not durable: %+v", first)
	}
	wrong := f.process(t, groupInput(t, f.serviceFixture, "wrong", "bob", "g1", "确认 "+choiceFor(first)+" alpha"))
	if wrong.Status != "failed" || len(f.runner.snapshot()) != 0 {
		t.Fatal("another group member confirmed the request", wrong)
	}
	choice, err := f.service.Store.ChannelChoice(context.Background(), choiceFor(first))
	if err != nil || choice.Status != "pending" {
		t.Fatal("rejected confirmation consumed choice", choice, err)
	}
	confirmed := f.process(t, groupInput(t, f.serviceFixture, "confirm", "alice", "g1", "确认 "+choiceFor(first)+" Project alpha"))
	if confirmed.Status != "completed" || len(f.runner.snapshot()) != 1 {
		t.Fatal("original requester could not continue", confirmed)
	}
}

func TestAgentCatalogFiltersRoutingAndRetiredCommandsDoNotChangeIt(t *testing.T) {
	f := newRoutingFixture(t)
	f.service.Router = serviceRouter(func(_ context.Context, r routing.Request) (routing.Decision, error) {
		if len(r.Projects) != 1 || r.Projects[0].ID != "alpha" {
			t.Fatalf("router saw another agent's catalog: %+v", r.Projects)
		}
		return routing.Decision{Kind: "default"}, nil
	})
	instanceDispatch(t, f, map[string]string{"g": "support"})
	list := f.process(t, groupInput(t, f.serviceFixture, "list", "alice", "g", "/help"))
	if strings.Contains(list.ReplyText, "beta") || strings.Contains(list.ReplyText, "Project alpha") || !strings.Contains(list.ReplyText, "/status") {
		t.Fatal("project list leaked another catalog", list.ReplyText)
	}
	invalid := f.process(t, groupInput(t, f.serviceFixture, "invalid", "alice", "g", "/project beta"))
	if invalid.Status != "completed" || invalid.RunID != "" || len(f.runner.snapshot()) != 0 {
		t.Fatal("unconfigured project selected", invalid)
	}
	task := f.process(t, groupInput(t, f.serviceFixture, "task", "alice", "g", "a general question"))
	if task.Status != "completed" {
		t.Fatal(task)
	}
}

func TestAgentResolutionFailureNeverFallsBackToDefaultHost(t *testing.T) {
	for _, resolve := range []func(context.Context, state.ChannelMessage) (*Service, error){
		func(context.Context, state.ChannelMessage) (*Service, error) {
			return nil, errors.New("private configuration path /secret")
		},
		func(context.Context, state.ChannelMessage) (*Service, error) { return nil, nil },
		func(context.Context, state.ChannelMessage) (*Service, error) { return &Service{}, nil },
	} {
		f := newServiceFixture(t)
		f.service.ResolveAgent = resolve
		m := f.process(t, f.message(t, "unavailable", "hello", "text", "owner-dm"))
		if m.Status != "failed" || m.ReplyStatus != "accepted" || len(f.runner.snapshot()) != 0 || strings.Contains(m.ReplyText, "/secret") {
			t.Fatalf("invalid agent fell back or disclosed config: %+v", m)
		}
	}
}

func TestSharedGroupRoutesDoNotChangeSeparateGroup(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	a := f.process(t, groupInput(t, f.serviceFixture, "task-a", "alice", "g1", "Inspect alpha"))
	b := f.process(t, groupInput(t, f.serviceFixture, "task-b", "bob", "g2", "Inspect beta"))
	one := f.process(t, groupInput(t, f.serviceFixture, "continue-a", "bob", "g1", "continue"))
	two := f.process(t, groupInput(t, f.serviceFixture, "continue-b", "alice", "g2", "continue"))
	if one.SessionID != a.SessionID || two.SessionID != b.SessionID || one.ProjectID != "alpha" || two.ProjectID != "beta" {
		t.Fatal("group task context crossed conversations", a, b, one, two)
	}
}

func TestNumberedLegacyGroupConfirmationDoesNotConsumeOtherRequests(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	for _, row := range []struct{ id, sender, text string }{{"a", "alice", "ambiguous secret-one"}, {"b", "alice", "ambiguous secret-two"}, {"c", "bob", "ambiguous secret-bob"}} {
		got := legacyPending(t, f, groupInput(t, f.serviceFixture, row.id, row.sender, "g", row.text))
		if got.Status != "awaiting" {
			t.Fatal(got)
		}
	}
	confirm := f.process(t, groupInput(t, f.serviceFixture, "number", "alice", "g", "确认 "+choiceFor(f.receipt(t, "a"))+" alpha"))
	if confirm.Status != "completed" || len(f.runner.snapshot()) != 1 {
		t.Fatal("numbered confirmation failed", confirm)
	}
	first := f.receipt(t, "a")
	second := f.receipt(t, "b")
	other := f.receipt(t, "c")
	if first.RunID == "" || second.RunID != "" || other.RunID != "" {
		t.Fatal("number selected wrong request", first, second, other)
	}
}

func TestOtherMembersPendingRequestsCannotHideOriginalConfirmation(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	own := legacyPending(t, f, groupInput(t, f.serviceFixture, "own-pending", "alice", "g", "ambiguous own task"))
	if own.Status != "awaiting" {
		t.Fatal(own)
	}
	for i := range 12 {
		id := fmt.Sprintf("other-%d", i)
		m := legacyPending(t, f, groupInput(t, f.serviceFixture, id, "bob", "g", "ambiguous other task"))
		if m.Status != "awaiting" {
			t.Fatal(m)
		}
	}
	confirmed := f.process(t, groupInput(t, f.serviceFixture, "own-confirm", "alice", "g", "确认 "+choiceFor(own)+" Project alpha"))
	if confirmed.Status != "completed" || len(f.runner.snapshot()) != 1 || f.receipt(t, "own-pending").RunID == "" {
		t.Fatal("other members displaced the original requester's pending task", confirmed)
	}
	for i := range 12 {
		if m := f.receipt(t, fmt.Sprintf("other-%d", i)); m.Status != "awaiting" || m.RunID != "" {
			t.Fatal("confirmation consumed another member's task", m)
		}
	}
}
