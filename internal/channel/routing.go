package channel

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func (s *Service) routeTask(ctx context.Context, m state.ChannelMessage) ([]string, string, routing.Decision, error) {
	projects, err := s.availableProjects(ctx)
	if err != nil {
		return nil, "", routing.Decision{}, err
	}
	if len(projects) == 0 {
		return nil, "", routing.Decision{Kind: "default", Reason: "No registered projects are available; the native agent handles the request in its default workspace."}, nil
	}
	messages, err := s.Store.RecentChannelRoutingContext(ctx, m.Binding, routing.MaxRecent)
	if err != nil {
		return nil, "", routing.Decision{}, err
	}
	recent := make([]routing.TaskContext, 0, len(messages))
	for _, message := range messages {
		recent = append(recent, routing.TaskContext{MessageID: message.MessageID, RunID: message.RunID, Text: addressedText(message), ProjectIDs: messageProjectIDs(message), PrimaryID: message.ProjectID})
	}
	text := addressedText(m)
	request := routing.Request{Text: text, Projects: projects, Recent: recent}
	decider := s.Router
	if decider == nil {
		decider = routing.New(nil)
	}
	decision, err := decider.Decide(ctx, request)
	if err != nil {
		return nil, "", decision, err
	}
	if decision.ContinuationMessageID != "" || decision.ContinuationRunID != "" {
		if len(recent) == 0 || decision.ContinuationMessageID != "" && decision.ContinuationMessageID != recent[0].MessageID || decision.ContinuationRunID != "" && decision.ContinuationRunID != recent[0].RunID || !equalIDs(decision.ProjectIDs, recent[0].ProjectIDs) || decision.PrimaryID != recent[0].PrimaryID {
			return nil, "", decision, errors.New("routing continuation does not match the latest actual task")
		}
	}
	switch decision.Kind {
	case "project":
		if len(decision.ProjectIDs) == 0 {
			return nil, "", decision, errors.New("project routing returned an empty project set")
		}
		return decision.ProjectIDs, decision.PrimaryID, decision, nil
	case "default":
		if len(decision.ProjectIDs) > 0 || decision.PrimaryID != "" {
			return nil, "", decision, errors.New("default routing returned a project selection")
		}
		if decision.ContinuationMessageID != "" || decision.ContinuationRunID != "" {
			audit, err := json.Marshal(struct {
				Policy   string           `json:"policy"`
				Original routing.Decision `json:"original_decision"`
			}{"channel-default-continuation-v2", decision})
			if err != nil {
				return nil, "", routing.Decision{}, err
			}
			decision.Details = audit
		}
		return nil, "", decision, nil
	case "clarify":
		// Classification uncertainty is context for the native agent, not a
		// prerequisite project-selection form. Keep the complete original
		// judgment as evidence while recording the actual execution decision.
		audit, err := json.Marshal(struct {
			Policy   string           `json:"policy"`
			Original routing.Decision `json:"original_decision"`
		}{"channel-native-handoff-v2", decision})
		if err != nil {
			return nil, "", routing.Decision{}, err
		}
		return nil, "", routing.Decision{Kind: "default", Reason: "Project routing was inconclusive; the native agent handles the request in its default workspace.", Details: audit}, nil
	default:
		return nil, "", decision, errors.New("project router returned an unknown decision")
	}
}

// A numbered natural-language answer identifies one pending request without
// changing the conversation's routing policy or future task environment.
func (s *Service) numberedProjectChoice(ctx context.Context, confirmation state.ChannelMessage) (bool, string, error) {
	selectionText, supplement, hasSupplement := strings.Cut(strings.TrimSpace(confirmation.Text), " -- ")
	parts := strings.Fields(selectionText)
	if len(parts) < 2 || parts[0] != "确认" && parts[0] != "取消" || !choiceNumber.MatchString(parts[1]) {
		return false, "", nil
	}
	if parts[0] == "取消" {
		if len(parts) != 2 || hasSupplement {
			return true, "取消待确认任务时，请回复“取消 任务编号”。", errors.New("invalid numbered cancellation")
		}
		choice, err := s.Store.ChannelChoice(ctx, parts[1])
		if err != nil || choice.Binding != confirmation.Binding {
			return true, "确认编号不存在或不属于当前会话。", errors.New("clarification is not available for this conversation")
		}
		original, err := s.Store.ChannelMessage(ctx, choice.ChannelMessageID)
		if err != nil || original.SenderID != confirmation.SenderID {
			return true, "请由原提问者取消这项任务。", errors.New("clarification cancellation requires its original sender")
		}
		if choice.Status != "pending" {
			return true, "该确认已处理或已中断，原任务没有重新执行。", nil
		}
		if err := s.Store.CancelChannelChoice(ctx, choice.ID, confirmation.ID); err != nil {
			return true, "无法取消待确认任务。", err
		}
		return true, "已取消这条待确认任务，没有执行。", nil
	}
	if len(parts) < 3 {
		return true, "请回复“确认 任务编号 项目名称”；多个项目用顿号分隔。", errors.New("clarification requires project names")
	}
	if hasSupplement {
		supplement = strings.TrimSpace(supplement)
		if supplement == "" {
			return true, "补充限制不能为空。", errors.New("clarification supplement is empty")
		}
	}
	projects, err := s.availableProjects(ctx)
	if err != nil {
		return true, "无法读取可用项目。", err
	}
	ids, recognized, err := projectReplyIDs(strings.Join(parts[2:], " "), projects)
	if err != nil || !recognized {
		return true, "请回复明确的可用项目名称；多个项目用顿号分隔。", errors.New("clarification project names are unknown or ambiguous")
	}
	primary := ""
	if len(ids) == 1 {
		primary = ids[0]
	}
	reply, err := s.confirmProject(ctx, confirmation, parts[1], ids, primary, supplement)
	return true, reply, err
}

var choiceNumber = regexp.MustCompile(`^[a-f0-9]{16}$`)

// Explicit numbered answers retain the exact confirmation receipt and validate
// identity and directories before claiming historical work exactly once.
func (s *Service) confirmProject(ctx context.Context, confirmation state.ChannelMessage, choiceID string, ids []string, primary, supplement string) (string, error) {
	choice, err := s.Store.ChannelChoice(ctx, choiceID)
	if err != nil || choice.Binding != confirmation.Binding {
		return "确认编号不存在或不属于当前会话。", errors.New("clarification is not available for this conversation")
	}
	originalMessage, err := s.Store.ChannelMessage(ctx, choice.ChannelMessageID)
	if err != nil || originalMessage.SenderID != confirmation.SenderID {
		return "请由原提问者确认这项任务。", errors.New("clarification requires its original sender")
	}
	if choice.Status != "pending" {
		return "该确认已处理或已中断，原任务没有重新执行。请查看本机记录；如需重做，请明确发送新任务。", nil
	}
	h, runtime, err := s.resolveProjects(ctx, ids, primary)
	if err != nil {
		return "所选项目当前不可用，待确认任务已保留。请联系管理员。", err
	}
	original, err := s.Store.ClaimChannelChoice(ctx, choice.ID, confirmation.ID, supplement)
	if err != nil {
		return "原任务没有执行，无法认领此确认。", err
	}
	run, notice, runErr := s.runResolved(ctx, original, h, runtime)
	status, cause := "completed", ""
	if runErr != nil {
		status, cause = "failed", runErr.Error()
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Store.FinishChannelMessage(finishCtx, original.ID, status, cause); err != nil {
		return "原请求结果无法确认，请在本机检查记录；任务没有重做。", err
	}
	return resultTextWithNotice(run, runErr, notice, replyBodyBudget(confirmation)), runErr
}
