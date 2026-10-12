package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"agent-host/internal/assembly"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/state"
)

func (s *Service) projectsConfigured() bool { return s.ResolveHost != nil || s.ResolveProjects != nil }

func (s *Service) availableProjects(ctx context.Context) ([]state.Project, error) {
	if s.Projects != nil {
		return s.Projects(ctx)
	}
	return s.Store.Projects(ctx)
}

// A shared group can contain multiple pending requests. Only the original
// sender may view and confirm their own unresolved requests.
func (s *Service) senderChoices(ctx context.Context, m state.ChannelMessage) ([]state.ChannelChoice, error) {
	choices, err := s.Store.PendingChannelChoices(ctx, m.Binding, m.SenderID)
	if err != nil {
		return nil, err
	}
	var own []state.ChannelChoice
	for _, choice := range choices {
		original, err := s.Store.ChannelMessage(ctx, choice.ChannelMessageID)
		if err != nil {
			return nil, err
		}
		if original.SenderID == m.SenderID {
			own = append(own, choice)
		}
	}
	return own, nil
}

// Conversation commands share the native FIFO and never invoke a model.
func (s *Service) conversationCommand(ctx context.Context, m state.ChannelMessage) (bool, string, error) {
	// Only the local parsing copy changes; receipts and native input stay exact.
	m.Text = addressedText(m)
	parts := strings.Fields(m.Text)
	if len(parts) == 0 {
		return false, "", nil
	}
	switch parts[0] {
	case "/project", "/projects", "/routing", "/choose", "/cancel":
		return true, "聊天不再设置或切换项目。请直接描述任务，助手会接手处理。\n\n" + conversationHelp, nil
	case "/help", "/status", "/new":
		if len(parts) != 1 {
			return true, "这些会话命令不接受参数。\n\n" + conversationHelp, errors.New("conversation command has arguments")
		}
		if parts[0] == "/help" {
			return true, conversationHelp, nil
		}
		if parts[0] == "/status" {
			reply, err := s.conversationStatus(ctx, m)
			return true, reply, err
		}
		if !s.projectsConfigured() {
			_, err := s.session(ctx, m.Binding, true)
			if err != nil {
				return true, resultText(state.Run{}, err), err
			}
			return true, "已开始新对话，已保存的记忆仍保留。", nil
		}
		h, runtime, err := s.currentRuntime(ctx, m.Binding)
		if err != nil {
			return true, "当前处理环境不可用，没有开始新对话。请联系管理员。", err
		}
		_, _, err = s.routedSession(ctx, m, h, runtime, true)
		if err != nil {
			return true, resultText(state.Run{}, err), err
		}
		reply := "已开始当前处理范围的新对话，已保存的记忆仍保留。"
		if m.ConversationType == "2" {
			reply += "本群成员共享此对话。"
		}
		return true, reply, nil
	default:
		if dingtalk.IsSlashCommand(m.Text) {
			return true, "聊天仅支持以下会话命令，其他原生交互命令不可在此执行。\n\n" + conversationHelp, nil
		}
		return s.numberedProjectChoice(ctx, m)
	}
}

const conversationHelp = "直接描述你要处理的问题，助手会接手处理；需要补充的信息会在对话中询问。\n\n/new 开始新对话，保留已保存的记忆\n/status 查看当前对话状态\n/help 查看帮助"

func (s *Service) conversationStatus(ctx context.Context, m state.ChannelMessage) (string, error) {
	choices, err := s.senderChoices(ctx, m)
	if err != nil {
		return "暂时无法读取对话状态，请稍后重试。", err
	}
	recent, err := s.Store.RecentChannelRoutingContext(ctx, m.Binding, 1)
	if err != nil {
		return "暂时无法读取对话状态，请稍后重试。", err
	}
	text := "当前对话可用，任务会自动判断处理范围。"
	if len(recent) == 0 && s.projectsConfigured() {
		text += "\n本轮新对话尚无可延续的任务。"
	} else if len(recent) > 0 {
		status := "结果未确认"
		if recent[0].Status == "completed" {
			status = "已完成"
		} else if recent[0].Status == "failed" {
			status = "未完成"
		}
		text += "\n最近进入执行的任务：" + status + "。"
	}
	if len(choices) == 10 {
		text += "\n你有至少 10 项任务等待确认处理范围。"
	} else if len(choices) > 0 {
		text += fmt.Sprintf("\n你有 %d 项任务等待确认处理范围。", len(choices))
	}
	if m.ConversationType == "2" {
		text += "\n本群成员共享此对话。"
	}
	return text, nil
}

// Recognize only complete catalog names, aliases or IDs. Extra task text,
// negation and generic assent cannot authorize an older pending operation.
func projectReplyIDs(text string, projects []state.Project) ([]string, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 512 {
		return nil, false, nil
	}
	switch strings.ToLower(text) {
	case "yes", "ok", "okay", "是", "是的", "好", "好的", "可以", "确认", "取消":
		return nil, false, nil
	}
	lookup := func(term string) []string {
		var ids []string
		for _, p := range projects {
			for _, name := range append([]string{p.ID, p.Name}, p.Aliases...) {
				if name != "" && strings.EqualFold(strings.TrimSpace(term), name) {
					ids = append(ids, p.ID)
					break
				}
			}
		}
		return ids
	}
	parts := []string{text}
	if len(lookup(text)) == 0 {
		text = strings.ReplaceAll(text, " and ", "、")
		parts = strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune("，,、和\n", r) })
		if len(parts) == 1 {
			parts = strings.Fields(text)
		}
	}
	var ids []string
	seen := map[string]bool{}
	ambiguous := false
	for _, part := range parts {
		matches := lookup(part)
		if len(matches) == 0 {
			return nil, false, nil
		}
		if len(matches) != 1 || seen[matches[0]] {
			ambiguous = true
		}
		seen[matches[0]] = true
		ids = append(ids, matches[0])
	}
	if ambiguous {
		return nil, true, errors.New("ambiguous or repeated project reply")
	}
	return ids, len(ids) > 0, nil
}

// Reset follows the latest actual environment, never a historical manual pin.
func (s *Service) currentRuntime(ctx context.Context, binding string) (Host, assembly.Runtime, error) {
	recent, err := s.Store.RecentChannelRoutes(ctx, binding, 1)
	if err != nil {
		return nil, assembly.Runtime{}, err
	}
	if len(recent) > 0 {
		return s.resolveProjects(ctx, messageProjectIDs(recent[0]), recent[0].ProjectID)
	}
	return s.resolveProjects(ctx, nil, "")
}

func (s *Service) resolveProjects(ctx context.Context, ids []string, primary string) (Host, assembly.Runtime, error) {
	var h Host
	var runtime assembly.Runtime
	var err error
	if s.Projects != nil {
		projects, err := s.availableProjects(ctx)
		if err != nil {
			return nil, runtime, err
		}
		for _, id := range ids {
			if !slices.ContainsFunc(projects, func(p state.Project) bool { return p.ID == id }) {
				return nil, runtime, errors.New("project is not configured for this agent")
			}
		}
	}
	if s.ResolveProjects != nil {
		h, runtime, err = s.ResolveProjects(ctx, ids, primary)
	} else if s.ResolveHost != nil && len(ids) <= 1 {
		id := ""
		if len(ids) == 1 {
			id = ids[0]
		}
		h, runtime, err = s.ResolveHost(ctx, id)
	} else {
		return nil, runtime, errors.New("multi-project runtime is not configured")
	}
	if err != nil {
		return nil, runtime, err
	}
	if h == nil || runtime.Workspace == "" || runtime.Scope == "" || runtime.Fingerprint == "" {
		return nil, runtime, errors.New("project resolver returned incomplete runtime")
	}
	for _, directory := range append([]string{runtime.Workspace}, runtime.AdditionalDirectories...) {
		actual, err := state.CanonicalWorkspace(directory)
		if err != nil {
			return nil, runtime, fmt.Errorf("project workspace unavailable: %w", err)
		}
		if actual != directory {
			return nil, runtime, errors.New("project workspace changed its canonical path")
		}
	}
	if len(ids) > 0 && !equalIDs(ids, runtimeProjectIDs(runtime)) {
		return nil, runtime, errors.New("assembled runtime does not match selected projects")
	}
	return h, runtime, nil
}

func (s *Service) runMessage(ctx context.Context, m state.ChannelMessage) (state.Run, string, error) {
	if !s.projectsConfigured() {
		session, notice, err := s.sessionWithHost(ctx, s.Host, m.Binding, false)
		if err != nil {
			return state.Run{}, "", err
		}
		run, err := s.Host.RunChannel(ctx, session, m.Text, m.ID)
		return run, notice, err
	}
	ids, primary, decision, err := s.routeTask(ctx, m)
	if err != nil {
		return state.Run{}, "项目路由失败，任务没有执行。请稍后重试或联系管理员。", err
	}
	if decision.Kind != "" {
		encoded, err := json.Marshal(decision)
		if err != nil {
			return state.Run{}, "", err
		}
		if err := s.Store.SaveChannelRoutingDecision(ctx, m.ID, encoded); err != nil {
			return state.Run{}, "路由依据保存失败，任务没有执行。", err
		}
	}
	h, runtime, err := s.resolveProjects(ctx, ids, primary)
	if err != nil {
		return state.Run{}, "选定项目不可用，请检查登记目录；任务没有改到其他目录执行。", err
	}
	return s.runResolved(ctx, m, h, runtime)
}

func (s *Service) runResolved(ctx context.Context, m state.ChannelMessage, h Host, runtime assembly.Runtime) (state.Run, string, error) {
	session, notice, err := s.routedSession(ctx, m, h, runtime, false)
	if err != nil {
		return state.Run{ProjectID: runtime.ProjectID, ProjectName: runtime.ProjectName, Workspace: runtime.Workspace, Projects: runtime.Projects, AdditionalDirectories: runtime.AdditionalDirectories}, "", err
	}
	run, err := h.RunChannel(ctx, session, m.Text, m.ID)
	return run, notice, err
}

func (s *Service) routedSession(ctx context.Context, m state.ChannelMessage, h Host, runtime assembly.Runtime, fresh bool) (state.Session, string, error) {
	ids := runtimeProjectIDs(runtime)
	shape, _ := json.Marshal(struct{ IDs, Directories []string }{ids, runtime.AdditionalDirectories})
	binding := Key(m.Binding, runtime.ProjectID, runtime.Workspace, runtime.Fingerprint, string(shape))
	changed := false
	if !fresh {
		previous, err := s.Store.ChannelRouteProjects(ctx, m.Binding, ids)
		if err == nil {
			changed = previous.ProjectID != runtime.ProjectID || previous.Workspace != runtime.Workspace || previous.RuntimeFingerprint != runtime.Fingerprint || !equalIDs(previous.AdditionalDirectories, runtime.AdditionalDirectories)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return state.Session{}, "", err
		} else {
			legacyBinding := Key(s.Profile, m.TenantID, m.ConversationID, m.SenderID)
			_, err := s.Store.ChannelSession(ctx, legacyBinding)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return state.Session{}, "", err
			}
			changed = err == nil
		}
	}
	session, notice, err := s.sessionWithHost(ctx, h, binding, fresh || changed)
	if err != nil {
		return state.Session{}, "", err
	}
	if session.ProjectID != runtime.ProjectID || session.WorkingDirectory() != runtime.Workspace || session.Scope != runtime.Scope || session.RuntimeFingerprint != runtime.Fingerprint || !sameRuntimeProjects(session.Projects, runtime.Projects) || !equalIDs(session.AdditionalDirectories, runtime.AdditionalDirectories) {
		return state.Session{}, "", errors.New("native session does not match assembled runtime")
	}
	if err := s.Store.ResolveChannelProjects(ctx, m.ID, binding, runtime.ProjectID, runtime.Workspace, runtime.Scope, runtime.Fingerprint, runtime.Projects, runtime.AdditionalDirectories); err != nil {
		return state.Session{}, "", err
	}
	if changed {
		notice = "当前处理方式已更新，已开始新的对话。"
	}
	return session, notice, nil
}

func runtimeProjectIDs(runtime assembly.Runtime) []string {
	return messageProjectIDs(state.ChannelMessage{Projects: runtime.Projects, ProjectID: runtime.ProjectID})
}

func messageProjectIDs(message state.ChannelMessage) []string {
	ids := []string{}
	for _, project := range message.Projects {
		ids = append(ids, project.ID)
	}
	if len(ids) == 0 && message.ProjectID != "" {
		ids = append(ids, message.ProjectID)
	}
	sort.Strings(ids)
	return ids
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameRuntimeProjects(a, b []state.ProjectRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Scope != b[i].Scope || a[i].Workspace != b[i].Workspace {
			return false
		}
	}
	return true
}
