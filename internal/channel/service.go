package channel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

type Host interface {
	Session(context.Context, string) (state.Session, error)
	RunChannel(context.Context, state.Session, string, string) (state.Run, error)
}

type Replier interface {
	Reply(context.Context, string, time.Time, dingtalk.ReplyMessage) (string, error)
}

type Reactor interface {
	AddReaction(context.Context, string, string, string) error
	RemoveReaction(context.Context, string, string, string) error
}

// APIReactor returns explicit delivery evidence for APIs that delete by a
// provider-issued reaction ID rather than by the displayed label.
type APIReactor interface {
	AddReaction(context.Context, state.ChannelReplyTarget, string) (providerID, status string, err error)
	RemoveReaction(context.Context, state.ChannelReplyTarget, string) (status string, err error)
}

type AgentSelection struct {
	AgentID    string
	AudienceID string
}

// Service owns durable channel intake and automatic project dispatch.
// Inbound text cannot invent directories or change the routing policy.
type Service struct {
	// OwnerPrivateCommand handles explicit Host clarification references before
	// ordinary dispatch. Admission is authenticated and owner-private only.
	OwnerPrivateCommand func(context.Context, state.ChannelMessage) (bool, string, error)
	StartFeedback       func(context.Context) (func(), error)
	OwnerID             string
	ApprovalSender      ApprovalSender
	// Empty Channel preserves the existing DingTalk adapter and receipt shape.
	Channel            string
	DisableReactions   bool
	TextReplier        TextReplier
	SelectMessageAgent func(context.Context, Message) (AgentSelection, error)

	Downloader        MediaDownloader
	ReadForwardedCard func(context.Context, state.ChannelMessage) (string, error)
	MediaRoot         string
	Store             *state.Store
	Host              Host
	ResolveHost       func(context.Context, string) (Host, assembly.Runtime, error)
	ResolveProjects   func(context.Context, []string, string) (Host, assembly.Runtime, error)
	Router            routing.Decider
	SelectAgent       func(context.Context, dingtalk.Message) (AgentSelection, error)
	ResolveAgent      func(context.Context, state.ChannelMessage) (*Service, error)
	Projects          func(context.Context) ([]state.Project, error)
	// Runtime lookup may expose local candidates without registering them. Only
	// projects selected by a validated routing decision are enrolled.
	PrepareProjects func(context.Context, state.ChannelMessage, []state.Project) ([]state.Project, error)
	EnrollProjects  func(context.Context, state.ChannelMessage, []state.Project) error
	Replier         Replier
	Reactor         Reactor
	APIReactor      APIReactor
	ClientID        string
	Scope           string
	Profile         string
	Timeout         time.Duration
	// MaxConcurrentConversations bounds active conversation bindings. Zero uses
	// the default; a binding remains occupied through its final reply attempt.
	MaxConcurrentConversations int
	Observe                    func(kind, message string) error
	ObserveReaction            func(kind string, reaction state.ChannelReaction, duration time.Duration) error
}

// Key avoids concatenation collisions between independent provider identifiers.
func Key(parts ...string) string {
	data, _ := json.Marshal(parts)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Service) Validate() error {
	if s.Store == nil || s.Host == nil && s.ResolveHost == nil && s.ResolveProjects == nil && s.ResolveAgent == nil || s.ClientID == "" || s.Scope == "" || s.Profile == "" || s.Timeout <= 0 {
		return errors.New("channel service requires store, host, replier, reactor, identity, scope, profile and positive timeout")
	}
	switch s.Channel {
	case "", "dingtalk":
		if s.Replier == nil || s.Reactor == nil || s.DisableReactions {
			return errors.New("DingTalk service requires its reply and reaction adapters")
		}
	case state.ReplyTargetFeishu:
		if s.TextReplier == nil || !s.DisableReactions && s.APIReactor == nil {
			return errors.New("Feishu service requires a text reply adapter and an API reaction adapter when feedback is enabled")
		}
	case state.ReplyTargetFeishuMeeting:
		if s.TextReplier == nil || !s.DisableReactions {
			return errors.New("Feishu service requires a text reply adapter and disabled reactions")
		}
	default:
		return errors.New("unsupported channel service")
	}
	if s.MaxConcurrentConversations < 0 || s.MaxConcurrentConversations > 32 {
		return errors.New("channel concurrent conversations must be between 1 and 32, or zero for the default")
	}
	return nil
}

// Receive must be called only with a provider-authenticated, authorized
// message. Completion means durable receipt, not execution or delivery success.
func (s *Service) Receive(ctx context.Context, m dingtalk.Message) error {
	if s.Channel != "" && s.Channel != "dingtalk" {
		return errors.New("DingTalk input does not match the configured channel")
	}
	selection := AgentSelection{AgentID: "default", AudienceID: "owner"}
	if m.ConversationType == "2" {
		selection.AudienceID = Key("dingtalk-group-v1", s.ClientID, m.TenantID, m.ConversationID)
	}
	if s.SelectAgent != nil {
		var err error
		selection, err = s.SelectAgent(ctx, m)
		if err != nil {
			return fmt.Errorf("select conversation agent: %w", err)
		}
	}
	if strings.TrimSpace(selection.AgentID) == "" || strings.TrimSpace(selection.AudienceID) == "" {
		return errors.New("conversation agent selection is incomplete")
	}
	binding := Key(s.Profile, m.TenantID, m.ConversationID, m.SenderID)
	if s.ResolveHost != nil || s.ResolveProjects != nil {
		// Selection follows verified identity across runtime configuration changes.
		// Profile still restricts which queued configuration the worker can claim.
		binding = Key("project-conversation-v1", "dingtalk", s.ClientID, m.TenantID, m.ConversationID, m.SenderID)
	}
	if m.ConversationType == "2" || selection.AgentID != "default" || selection.AudienceID != "owner" {
		sender := m.SenderID
		if m.ConversationType == "2" {
			sender = ""
		}
		binding = Key("agent-conversation-v1", "dingtalk", s.ClientID, m.TenantID, m.ConversationID, sender, selection.AgentID, selection.AudienceID)
	}
	message := state.ChannelMessage{
		ReplyToID: m.ReplyToID,
		ID:        Key("dingtalk", s.ClientID, m.ID), Channel: "dingtalk", AccountID: s.ClientID,
		TenantID: m.TenantID, MessageID: m.ID, ConversationID: m.ConversationID,
		SenderName: m.SenderName, SenderID: m.SenderID, Profile: s.Profile, ConversationType: m.ConversationType,
		AgentID: selection.AgentID, AudienceID: selection.AudienceID,
		Binding: binding,
		Scope:   s.Scope, MessageType: m.MessageType, Text: m.Text,
		ReceivedAt: time.Now().UTC(), SentAt: m.CreatedAt, ReplyExpiresAt: m.ReplyExpiresAt,
	}
	var codes []string
	message.Input, codes = inputMaterial(m)
	return s.enqueue(ctx, message, m.ReplyURL, codes)
}

func (s *Service) enqueue(ctx context.Context, message state.ChannelMessage, replyURL string, codes []string) error {
	inserted, err := s.Store.EnqueueChannelMessage(ctx, message, replyURL, codes)
	if err != nil {
		return fmt.Errorf("persist channel input: %w", err)
	}
	status := "duplicate"
	if inserted {
		status = "queued"
	}
	if err := s.observe("channel.received", fmt.Sprintf("message=%s status=%s", message.ID, status)); err != nil {
		return err
	}
	return nil
}

// Work joins independent execution and reaction workers. Only durable state is
// shared: slow platform feedback cannot hold up ACKs, native turns, or replies.
func (s *Service) Work(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.StartFeedback != nil {
		stop, err := s.StartFeedback(ctx)
		if err != nil {
			return err
		}
		defer stop()
	}
	if s.DisableReactions {
		return s.workMessages(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- s.workMessages(ctx) }()
	go func() { done <- s.workReactions(ctx) }()
	err := <-done
	cancel()
	return errors.Join(err, <-done)
}

// Fork-capable Hosts execute bounded independent message sessions, even in one
// room. Legacy Hosts retain binding/memory-writer exclusion. Publication still
// protects durable memory; interrupted claimed work is never replayed.
func (s *Service) workMessages(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	limit := s.MaxConcurrentConversations
	if limit == 0 {
		limit = 4
	}
	type completion struct {
		binding string
		err     error
	}
	done := make(chan completion, limit)
	active := make(map[string]state.ChannelMessage, limit)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var nextApprovalSweep time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(nextApprovalSweep) {
			kind := s.Channel
			if kind == "" {
				kind = "dingtalk"
			}
			if err := s.Store.RefreshOperationApprovals(ctx, kind, s.ClientID, s.OwnerID, time.Now().UTC()); err != nil {
				return err
			}
			nextApprovalSweep = time.Now().Add(time.Minute)
		}
		for len(active) < limit {
			busy := make([]state.ChannelMessage, 0, len(active))
			for _, message := range active {
				busy = append(busy, message)
			}
			if supportsRooms(s.Host) {
				busy = nil
			}
			m, err := s.Store.ClaimNextEligibleChannelMessage(ctx, s.Profile, busy)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return fmt.Errorf("claim channel input: %w", err)
			}
			active[m.ID] = m
			workers.Add(1)
			go func() {
				defer workers.Done()
				done <- completion{binding: m.ID, err: s.process(ctx, m)}
			}()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-done:
			delete(active, result.binding)
			if result.err != nil {
				return result.err
			}
		case <-ticker.C:
		}
	}
}

func (s *Service) process(ctx context.Context, m state.ChannelMessage) error {
	if s.ResolveAgent != nil {
		resolved, err := s.ResolveAgent(ctx, m)
		if err == nil && resolved == nil {
			err = errors.New("agent resolver returned no service")
		}
		if err != nil {
			return s.processMessage(ctx, m, err)
		}
		child := *resolved
		child.ResolveAgent = nil
		if child.Replier == nil {
			child.Replier = s.Replier
		}
		if child.TextReplier == nil {
			child.TextReplier = s.TextReplier
		}
		if child.Channel == "" {
			child.Channel = s.Channel
		}
		if s.DisableReactions {
			child.DisableReactions = true
		}
		if child.Reactor == nil {
			child.Reactor = s.Reactor
		}
		if child.APIReactor == nil {
			child.APIReactor = s.APIReactor
		}
		if child.Observe == nil {
			child.Observe = s.Observe
		}
		if child.ObserveReaction == nil {
			child.ObserveReaction = s.ObserveReaction
		}
		if child.Timeout == 0 {
			child.Timeout = s.Timeout
		}
		if child.ClientID == "" {
			child.ClientID = s.ClientID
		}
		if child.Profile == "" {
			child.Profile = s.Profile
		}
		if err := child.Validate(); err != nil {
			return s.processMessage(ctx, m, err)
		}
		return child.processMessage(ctx, m, nil)
	}
	return s.processMessage(ctx, m, nil)
}

func (s *Service) processMessage(ctx context.Context, m state.ChannelMessage, resolutionErr error) error {
	if err := s.observe("channel.processing", "message="+m.ID); err != nil {
		return err
	}
	turnCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	status, reply, cause := "completed", "", ""
	var run state.Run
	var runErr error
	waiting := false
	if resolutionErr == nil && m.MessageType == "interactiveCard" && !supportedInput(m) && s.ReadForwardedCard != nil &&
		m.ConversationType == "1" && s.OwnerID != "" && m.SenderID == s.OwnerID && m.AudienceID == "owner" && m.ReplyExpiresAt.After(time.Now()) {
		readCtx, stopRead := context.WithTimeout(turnCtx, 30*time.Second)
		body, err := s.ReadForwardedCard(readCtx, m)
		stopRead()
		if err != nil {
			resolutionErr = errors.New("forwarded card message read failed")
		} else {
			q := dingtalk.ForwardedCardText(body)
			resolutionErr = s.Store.SaveForwardedCardText(turnCtx, m.ID, q.Body, q.Truncated)
			if resolutionErr == nil {
				m, resolutionErr = s.Store.ChannelMessage(turnCtx, m.ID)
			}
		}
	}
	if resolutionErr != nil {
		status, cause, reply = "failed", resolutionErr.Error(), "当前助手配置不可用，本次请求没有执行。请联系管理员。"
		if m.MessageType == "interactiveCard" {
			reply = "已收到卡片，但未能读取它的正文，本次没有解读。请发送截图或复制审批文字。"
		}
	} else if m.ReplyTarget == nil && !m.ReplyExpiresAt.After(time.Now()) {
		status, cause, reply = "failed", "reply capability expired before execution", "消息回复凭据已过期，本次请求没有执行。请重新发送问题。"
	} else if m.AmbiguousTask {
		reply = "未能唯一关联到原工单，请引用目标工单卡片或提供工单链接，避免串单。本次没有执行任何操作。"
	} else if !supportedInput(m) {
		status, cause, reply = "failed", "unsupported message type", "这条消息的内容格式暂不支持，请发送文字、图片或文件。"
		if m.MessageType == "interactiveCard" {
			cause, reply = "card preview unavailable", "已收到转发的卡片，但钉钉没有提供可读取的预览内容。请引用卡片并发送解读请求，或发送截图、复制审批文字。"
		}
	} else if strings.TrimSpace(m.Text) == "" || len(m.Text) > 16*1024 {
		status, cause, reply = "failed", "invalid text length", "文字消息不能为空，且不能超过 16 KiB。"
	} else {
		var handled bool
		m, runErr = s.prepareInput(turnCtx, m)
		if runErr != nil {
			handled, reply = true, "附件未能下载或保存，本次任务没有执行。请重新发送附件。"
		} else {
			// A normal reply can carry quote material. Match only the current
			// authenticated text, never the quoted body, before native dispatch.
			if m.MessageType == "text" {
				if s.OwnerPrivateCommand != nil && s.OwnerID != "" && m.SenderID == s.OwnerID && m.ConversationType == "1" && m.AudienceID == state.OwnerAudienceID {
					handled, reply, runErr = s.OwnerPrivateCommand(turnCtx, m)
				}
				if !handled && runErr == nil {
					handled, reply, runErr = s.approvalReply(turnCtx, m)
				}
			}
			if !handled && runErr == nil && m.Input == nil {
				handled, reply, runErr = s.conversationCommand(turnCtx, m)
			}
		}
		if !handled {
			var notice string
			run, notice, runErr = s.runMessage(turnCtx, m)
			reply = resultTextWithNotice(run, runErr, notice, replyBodyBudget(m))
			if run.ID == "" && runErr == nil {
				stored, err := s.Store.ChannelMessage(turnCtx, m.ID)
				if err != nil {
					return err
				}
				if stored.Status == "awaiting" {
					waiting, status, reply = true, "awaiting", notice
				}
			}
		}
		if runErr != nil {
			status, cause = "failed", runErr.Error()
		}
	}
	// Outcome recording and one delivery attempt must remain possible after a
	// turn timeout. The bounded context never retries the native task.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer finishCancel()
	if !waiting {
		if err := s.Store.FinishChannelMessage(finishCtx, m.ID, status, cause); err != nil {
			return fmt.Errorf("finish channel input: %w", err)
		}
	}
	if err := s.observe("channel.processed", fmt.Sprintf("message=%s status=%s run=%s", m.ID, status, run.ID)); err != nil {
		return err
	}
	message := replyForMessage(m, reply)
	if m.ReplyTarget != nil {
		message.Text = apiReplyText(reply, replyBodyBudget(m))
	}
	if err := s.Store.PrepareChannelReply(finishCtx, m.ID, message.Text); err != nil {
		return fmt.Errorf("prepare channel reply: %w", err)
	}
	return s.deliverChannelReply(finishCtx, m, message)
}

func (s *Service) deliverChannelReply(finishCtx context.Context, m state.ChannelMessage, message dingtalk.ReplyMessage) error {
	var delivery string
	var sendErr error
	if m.ReplyTarget != nil {
		if s.TextReplier == nil {
			delivery, sendErr = "failed", errors.New("channel text reply adapter is unavailable")
		} else {
			delivery, sendErr = s.TextReplier.ReplyText(finishCtx, *m.ReplyTarget, message.Text)
		}
	} else {
		url, err := s.Store.ChannelReplyURL(finishCtx, m.ID)
		if err != nil {
			return fmt.Errorf("read channel reply capability: %w", err)
		}
		delivery, sendErr = s.Replier.Reply(finishCtx, url, m.ReplyExpiresAt, message)
	}
	if delivery != "accepted" && delivery != "failed" && delivery != "unknown" {
		return errors.New("replier returned invalid delivery outcome")
	}
	if delivery == "accepted" && sendErr != nil || delivery != "accepted" && sendErr == nil {
		return errors.New("replier returned inconsistent delivery outcome")
	}
	errorText := ""
	if sendErr != nil {
		errorText = sendErr.Error()
	}
	if err := s.Store.FinishChannelReply(finishCtx, m.ID, delivery, errorText); err != nil {
		return fmt.Errorf("record channel delivery: %w", err)
	}
	return s.observe("channel.delivery", fmt.Sprintf("message=%s status=%s", m.ID, delivery))
}

func (s *Service) session(ctx context.Context, binding string, fresh bool) (state.Session, error) {
	session, _, err := s.sessionWithHost(ctx, s.Host, binding, fresh)
	return session, err
}

func (s *Service) sessionWithHost(ctx context.Context, h Host, binding string, fresh bool) (state.Session, string, error) {
	id := ""
	if !fresh {
		var err error
		id, err = s.Store.ChannelSession(ctx, binding)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return state.Session{}, "", err
		}
	}
	session, err := h.Session(ctx, id)
	var cause *state.SessionResumeError
	if id != "" && errors.As(err, &cause) && cause.CanStartFresh() {
		session, err = h.Session(ctx, "")
		if err != nil {
			return state.Session{}, "", err
		}
		if err := s.Store.RecoverChannelSession(ctx, binding, id, session.ID, cause.RunID); err != nil {
			return state.Session{}, "", err
		}
		session.PreviousSessionID, session.RecoveryRunID = id, cause.RunID
		notice := "已恢复处理本条消息；旧任务没有自动重做。"
		if cause.ExecutionStatus == "completed" {
			notice = "本条消息将重新开始对话；此前已完成的任务没有重复执行。"
		}
		return session, notice, nil
	}
	if err != nil {
		return state.Session{}, "", err
	}
	if id == "" {
		if err := s.Store.BindChannelSession(ctx, binding, session.ID); err != nil {
			return state.Session{}, "", err
		}
	}
	return session, "", nil
}

func (s *Service) observe(kind, message string) error {
	if s.Observe == nil {
		return nil
	}
	return s.Observe(kind, message)
}
