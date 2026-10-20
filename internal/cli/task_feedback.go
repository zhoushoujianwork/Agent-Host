package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"agent-host/internal/channel"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
	"agent-host/internal/taskfeedback"
)

func feedbackDirectory(o options) string {
	if o.feedbackConfigDir != "" {
		return o.feedbackConfigDir
	}
	if o.agentsConfig != "" {
		return filepath.Dir(o.agentsConfig)
	}
	return unifiedDirectory(o.home)
}

func feedbackResolver(directory string, client taskfeedback.Evaluator) (taskfeedback.Resolver, error) {
	r := taskfeedback.Resolver{Evaluator: client}
	if directory == "" {
		return r, nil
	}
	c, err := loadUsers(directory)
	if err != nil {
		return r, err
	}
	if c == nil {
		return r, nil
	}
	for _, u := range c.Users {
		v := taskfeedback.User{ID: u.ID, DisplayName: u.DisplayName, Aliases: u.Aliases, NotifyChannel: u.NotifyChannel}
		for _, a := range u.Accounts {
			v.Accounts = append(v.Accounts, taskfeedback.Account{Channel: a.Channel, AccountID: a.AccountID, TenantID: a.TenantID, SenderID: a.SenderID})
		}
		r.Users = append(r.Users, v)
	}
	return r, nil
}

func configureTaskFeedback(h *host.Host, o options, client *jev.Client) {
	dir := feedbackDirectory(o)
	h.Config.PrepareFeedback = func(ctx context.Context, run state.Run, previous *taskfeedback.Plan) (*taskfeedback.Plan, error) {
		r, err := feedbackResolver(dir, client)
		if err != nil {
			return nil, err
		}
		req := taskfeedback.Request{Text: run.Input, Requester: "owner", RequestFor: o.requestFor, NotifyTo: o.notifyTo, Channel: o.notifyChannel, Interpret: dir != ""}
		req.OtherUsersOnly = o.feedbackOtherUsersOnly
		for _, supplement := range run.Supplements {
			req.Text += "\n\nLater authenticated task clarification:\n" + supplement.Text
		}
		if user := state.PrivateUserID(o.audienceID); user != "" {
			req.Requester = user
		}
		if run.Source != nil {
			s := run.Source
			provider := s.Channel
			if provider == state.ReplyTargetFeishuMeeting {
				provider = state.ReplyTargetFeishu
			}
			req.Origin = &taskfeedback.Origin{Account: taskfeedback.Account{Channel: provider, AccountID: s.AccountID, TenantID: s.TenantID, SenderID: s.SenderID}, MessageID: s.ChannelMessageID}
			req.Requester = provider + ":" + s.SenderID
			for _, u := range r.Users {
				for _, a := range u.Accounts {
					if a == req.Origin.Account {
						req.Requester = u.ID
					}
				}
			}
			if s.SenderID == o.feedbackOwnerID {
				req.Requester = "owner"
			} else {
				req.Interpret = false
			}
		}
		if dir == "" && (o.requestFor != "" || o.notifyTo != "" || o.notifyChannel != "") {
			return nil, errors.New("task feedback requires --feedback-config-dir or --agents-config")
		}
		if req.Channel != "" && req.Channel != "dingtalk" && req.Channel != "feishu" {
			return nil, errors.New("notify-channel must be dingtalk or feishu")
		}
		req.Previous = previous
		resolveCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		p := r.Resolve(resolveCtx, req)
		return &p, nil
	}
	if dir == "" {
		return
	}
	h.Config.BindFeedback = func(ctx context.Context, run state.Run) error {
		if run.Feedback == nil || run.Feedback.Mode == "terminal" {
			return nil
		}
		q, err := taskfeedback.Open(filepath.Join(filepath.Dir(dir), "feedback"))
		if err != nil {
			return err
		}
		defer q.Close()
		home, err := filepath.EvalSymlinks(o.home)
		if err != nil {
			return err
		}
		return q.Bind(ctx, taskfeedback.Item{ID: channel.Key("task-feedback-v1", home, run.ID), RunID: run.ID, Home: home, AgentID: run.AgentID, AudienceID: run.AudienceID, Plan: *run.Feedback})
	}
}

type feedbackSender interface {
	NotifyUser(context.Context, string, string, string) (string, string, error)
}

func startTaskFeedbackWorker(ctx context.Context, o options, account taskfeedback.Account, service *channel.Service, sender feedbackSender, observe func(string, string) error) (func(), error) {
	dir := feedbackDirectory(o)
	if dir == "" {
		return func() {}, nil
	}
	q, err := taskfeedback.Open(filepath.Join(filepath.Dir(dir), "feedback"))
	if err != nil {
		return nil, err
	}
	if err = q.Recover(ctx, account); err != nil {
		q.Close()
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer q.Close()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := dispatchTaskFeedback(workerCtx, q, o, account, service, sender); err != nil && workerCtx.Err() == nil && observe != nil {
				_ = observe("feedback.worker.failed", err.Error())
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func dispatchTaskFeedback(ctx context.Context, q *taskfeedback.Queue, o options, account taskfeedback.Account, service *channel.Service, sender feedbackSender) error {
	return dispatchTaskFeedbackAt(ctx, q, o, account, service, sender, time.Now())
}

func dispatchTaskFeedbackAt(ctx context.Context, q *taskfeedback.Queue, o options, account taskfeedback.Account, service *channel.Service, sender feedbackSender, now time.Time) error {
	items, err := q.Pending(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !taskfeedback.Matches(item.Plan.Target, account) || item.Status != "awaiting_result" && item.Status != "failed" {
			continue
		}
		if item.Status == "failed" && now.Sub(item.UpdatedAt) < time.Duration(item.Attempts)*10*time.Second {
			continue
		}
		store, err := state.OpenForProjectInspection(item.Home)
		if err != nil {
			if err = recordFeedbackSourceFailure(ctx, q, item, "source_database_unavailable"); err != nil {
				return err
			}
			continue
		}
		partition := store.WithPartition(item.AgentID, item.AudienceID)
		run, readErr := partition.Run(ctx, item.RunID)
		if readErr != nil {
			store.Close()
			if err = recordFeedbackSourceFailure(ctx, q, item, "source_run_unavailable"); err != nil {
				return err
			}
			continue
		}
		finished := run.ExecutionStatus != "running" && run.ExecutionStatus != ""
		finalReceipt := false
		finishedAt := run.CreatedAt
		for _, e := range run.Events {
			if e.Type == "run.finished" {
				finished = true
				finalReceipt = true
				finishedAt = e.At
			}
		}
		// Completed native execution can precede memory processing. Wait for the
		// final receipt, except recovered unknown/failed runs with no live turn.
		if !finalReceipt && run.ExecutionStatus == "completed" && (run.MemoryStatus == "pending" || run.ExperienceStatus == "pending") {
			finished = false
		}
		if !finished && now.Sub(item.UpdatedAt) > 30*time.Second {
			active, probeErr := state.HostActive(item.Home)
			if probeErr == nil && !active {
				if run.ExecutionStatus == "running" {
					run.ExecutionStatus = "unknown"
					item.SourceObservation = "host_exited_without_final_run_receipt"
				} else {
					item.SourceObservation = "host_exited_during_postprocessing"
				}
				run.MemoryStatus = "failed"
				run.ExperienceStatus = "failed"
				finished = true
			}
		}
		if !finished {
			store.Close()
			continue
		}
		if item.Plan.Mode == "origin" {
			if item.Plan.Origin == nil || service == nil {
				store.Close()
				continue
			}
			m, e := partition.ChannelMessage(ctx, item.Plan.Origin.MessageID)
			store.Close()
			if e != nil {
				continue
			}
			if m.ReplyStatus == "accepted" || m.ReplyStatus == "unknown" {
				if err = q.Finish(ctx, item, m.ReplyStatus, "original_channel_reply", m.ReplyError); err != nil {
					return err
				}
				continue
			}
			if item.Attempts >= 3 || now.Sub(finishedAt) < 30*time.Second {
				continue
			}
			if m.ReplyStatus != "pending" && m.ReplyStatus != "failed" {
				continue
			}
			claimed, e := q.Claim(ctx, &item, m.ReplyText)
			if e != nil {
				return e
			}
			if !claimed {
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			e = service.DeliverRecoveredFeedback(sendCtx, m, run)
			cancel()
			saved, e2 := service.Store.WithPartition(m.AgentID, m.AudienceID).ChannelMessage(ctx, m.ID)
			status, reason := "unknown", "original reply delivery not confirmed"
			if e2 == nil && (saved.ReplyStatus == "accepted" || saved.ReplyStatus == "failed" || saved.ReplyStatus == "unknown") {
				status, reason = saved.ReplyStatus, saved.ReplyError
			}
			if e != nil && status != "accepted" {
				reason = e.Error()
			}
			if e = q.Finish(ctx, item, status, "original_channel_reply", reason); e != nil {
				return e
			}
			continue
		}
		store.Close()
		if item.Attempts >= 3 {
			continue
		}
		r, err := feedbackResolver(feedbackDirectory(o), nil)
		if err != nil {
			if e := q.Finish(ctx, item, "needs_recipient", "", "verified_user_directory_unavailable"); e != nil {
				return e
			}
			continue
		}
		if o.feedbackOwnerID != "" {
			catalog, e := loadUsers(feedbackDirectory(o))
			if e != nil || catalog.validateOwner(account.Channel, account.AccountID, account.TenantID, o.feedbackOwnerID) != nil {
				if e = q.Finish(ctx, item, "needs_recipient", "", "configured_owner_binding_invalid"); e != nil {
					return e
				}
				continue
			}
		}
		if err = r.ValidateTarget(item.Plan); err != nil {
			if e := q.Finish(ctx, item, "needs_recipient", "", err.Error()); e != nil {
				return e
			}
			continue
		}
		text := item.Text
		if text == "" {
			text = channel.TaskFeedbackText(run)
		}
		item.ExecutionStatus = run.ExecutionStatus
		claimed, err := q.Claim(ctx, &item, text)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, receipt, sendErr := sender.NotifyUser(sendCtx, item.Plan.Target.SenderID, channel.Key(item.ID)[:48], text)
		cancel()
		if status != "accepted" && status != "failed" && status != "unknown" {
			status = "unknown"
		}
		reason := ""
		if sendErr != nil {
			reason = sendErr.Error()
		}
		if status == "accepted" && sendErr != nil {
			status = "unknown"
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = q.Finish(finishCtx, item, status, receipt, reason)
		finishCancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func recordFeedbackSourceFailure(ctx context.Context, q *taskfeedback.Queue, item taskfeedback.Item, reason string) error {
	if item.Attempts >= 3 {
		return nil
	}
	claimed, err := q.Claim(ctx, &item, item.Text)
	if err != nil || !claimed {
		return err
	}
	return q.Finish(ctx, item, "failed", "", reason)
}

func runNotifications(ctx context.Context, args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" {
		_, err := fmt.Fprintln(out, "agent-host notifications list|show ID|resolve ID --config-dir ABSOLUTE_CONFIG_DIRECTORY [--notify-to USER_ID --notify-channel CHANNEL]")
		return err
	}
	if args[0] != "list" && args[0] != "show" && args[0] != "resolve" {
		return errors.New("notifications requires list, show or resolve")
	}
	command, id, rest := args[0], "", args[1:]
	if command != "list" {
		if len(rest) == 0 || rest[0] == "" {
			return errors.New("notification ID is required")
		}
		id, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("notifications "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	dir := fs.String("config-dir", "", "Unified configuration directory")
	to := fs.String("notify-to", "", "Verified recipient user ID for resolution")
	provider := fs.String("notify-channel", "", "Configured notification channel")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !filepath.IsAbs(*dir) || fs.NArg() != 0 {
		return errors.New("notifications requires an absolute config-dir and no extra arguments")
	}
	if command != "resolve" && (*to != "" || *provider != "") {
		return errors.New("recipient options require notifications resolve")
	}
	path := filepath.Join(filepath.Dir(*dir), "feedback")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && command == "list" {
		return json.NewEncoder(out).Encode([]taskfeedback.Item{})
	}
	var q *taskfeedback.Queue
	var err error
	if command == "resolve" {
		q, err = taskfeedback.Open(path)
	} else {
		q, err = taskfeedback.OpenForInspection(path)
	}
	if err != nil {
		return err
	}
	defer q.Close()
	items, err := q.List(ctx)
	if err != nil {
		return err
	}
	if command == "show" || command == "resolve" {
		for _, item := range items {
			if item.ID != id {
				continue
			}
			if command == "show" {
				attempts, err := q.Attempts(ctx, id)
				if err != nil {
					return err
				}
				return json.NewEncoder(out).Encode(map[string]any{"notification": item, "attempts": attempts})
			}
			if *to == "" {
				return errors.New("notifications resolve requires --notify-to USER_ID")
			}
			r, err := feedbackResolver(*dir, nil)
			if err != nil {
				return err
			}
			requestFor := item.Plan.RequestFor
			if requestFor == "" {
				requestFor = *to
			}
			p := r.Resolve(ctx, taskfeedback.Request{Requester: item.Plan.Requester, RequestFor: requestFor, NotifyTo: *to, Channel: *provider, Origin: item.Plan.Origin})
			if err = q.ResolveRecipient(ctx, item, p); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]string{"status": "awaiting_result", "id": item.ID})
		}
		return errors.New("notification not found")
	}
	return json.NewEncoder(out).Encode(items)
}

// Explicit CLI attribution failures are visible independently from task success.
func feedbackNotice(run state.Run) string {
	if run.Feedback != nil && run.Feedback.Mode == "needs_recipient" {
		return fmt.Sprintf("Result notification needs a recipient (%s). Resolve the verified user/account binding; the business task was not replayed.", run.Feedback.Reason)
	}
	return ""
}
