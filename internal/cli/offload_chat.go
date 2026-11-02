package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"agent-host/internal/offloadchannel"
	"agent-host/internal/state"
	"agent-host/internal/terminal"
)

// Local terminal and Web room entries share the same host. Terminal Chat holds
// its session lease across all turns; rooms own durable request deduplication.
func runOffloadChat(ctx context.Context, o options, in io.Reader, out, errout io.Writer) (runErr error) {
	if o.resumeCanceledRun != "" && (!o.oneShot || o.resume == "" || o.room != "") {
		return errors.New("--resume-canceled-run requires direct one-shot chat with --resume")
	}
	if o.takeoverSession != "" || o.renewRuntime {
		return errors.New("Offload Chat does not support takeover or runtime renewal; start a new session")
	}
	if o.room != "" {
		if !o.oneShot || o.requestID == "" || o.resume != "" || len(o.room) > 200 || len(o.requestID) > 200 {
			return errors.New("room Chat requires a bounded request ID without --resume")
		}
	} else if o.requestID != "" || o.receivedAt != "" {
		return errors.New("request ID and receipt time require a room")
	}
	if o.requestFor != "" || o.notifyTo != "" || o.notifyChannel != "" || o.feedbackConfigDir != "" || o.feedbackOtherUsersOnly || o.knowledgeChannel != "" {
		return errors.New("Offload Chat does not support requester notifications or Host knowledge injection yet")
	}
	if o.oneShot && (strings.TrimSpace(o.message) == "" || len(o.message) > 16*1024) {
		return errors.New("provide a nonempty message up to 16 KiB")
	}
	if err := resolveOffloadChannelOptions(&o); err != nil {
		return err
	}
	store, release, err := openExecutionState(ctx, o.home, true)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close(), release()) }()
	var releaseSession func() error
	defer func() {
		if releaseSession != nil {
			runErr = errors.Join(runErr, releaseSession())
		}
	}()
	if o.resume != "" {
		releaseSession, err = state.AcquireChatSession(o.home, o.resume)
		if err != nil {
			return err
		}
	}
	renderer := terminal.Renderer{Out: out, JSON: o.format == "json"}
	h := composeOffloadChannelHost(store, o, renderer.Render).(*offloadchannel.Host)
	h.Config.ResumeCanceledRun = o.resumeCanceledRun
	if o.room == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		h.Config.ResumeCommand = func(run state.Run, session state.Session) ([]string, string) {
			args := append([]string{executable, "chat"}, o.monitorResumeArgs...)
			args = append(args, "--home", o.home, "--resume", session.ID, "--resume-canceled-run", run.ID, "--format", "json", "--detach", "-m", "Continue the unfinished task from the existing session. Inspect prior output and existing effects before taking further action; do not blindly replay completed operations.")
			return args, o.monitorDirectory
		}
	}
	if o.room != "" {
		var received time.Time
		if o.receivedAt != "" {
			received, err = time.Parse(time.RFC3339Nano, o.receivedAt)
			if err != nil {
				return err
			}
		}
		turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		_, err = h.RunRoom(turnCtx, state.RoomInput{Key: "web-cli:" + o.room, RequestID: o.requestID, SenderID: "local-owner", SenderName: "Local owner", Text: o.message, ReceivedAt: received})
		return err
	}
	session, err := h.Session(ctx, o.resume)
	if err != nil {
		return err
	}
	if releaseSession == nil {
		releaseSession, err = state.AcquireChatSession(o.home, session.ID)
		if err != nil {
			return err
		}
	}
	execute := func(input string) error {
		if strings.TrimSpace(input) == "" || len(input) > 16*1024 {
			return errors.New("provide a nonempty message up to 16 KiB")
		}
		turnCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		_, err := h.Run(turnCtx, session, input)
		return err
	}
	if o.oneShot {
		return execute(o.message)
	}
	return runChatInput(ctx, in, errout, execute)
}
