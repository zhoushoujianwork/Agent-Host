package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

const (
	Accepted = "accepted"
	Failed   = "failed"
	Unknown  = "unknown"
)

var (
	profilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	appPattern     = regexp.MustCompile(`^cli_[A-Za-z0-9]{1,64}$`)
	openIDPattern  = regexp.MustCompile(`^ou_[A-Za-z0-9]{1,64}$`)
	meetingPattern = regexp.MustCompile(`^[0-9]{10,30}$`)
	numberPattern  = regexp.MustCompile(`^[0-9]{9}$`)
)

// Config identifies the credential profile and expected application and bot.
// Credentials remain with lark-cli. Call CheckIdentity before using a new profile.
type Config struct {
	Profile   string
	AppID     string
	BotOpenID string
}

// MeetingClient performs bounded calls. It starts no background work and owns no
// persistent state. A custom Transport must honor cancellation and be safe for
// any concurrency used by its caller.
type MeetingClient struct {
	config    Config
	transport Transport
}

// NewMeetingClient validates local configuration without calling Feishu.
// A nil transport selects CLITransport with the default lark-cli executable.
func NewMeetingClient(config Config, transport Transport) (*MeetingClient, error) {
	if !profilePattern.MatchString(config.Profile) || !appPattern.MatchString(config.AppID) || !openIDPattern.MatchString(config.BotOpenID) {
		return nil, errors.New("Feishu requires an explicit profile, application ID and bot Open ID")
	}
	if transport == nil {
		transport = CLITransport{}
	}
	return &MeetingClient{config: config, transport: transport}, nil
}

func (c *MeetingClient) arguments(command, meetingID string) []string {
	return []string{"--profile", c.config.Profile, "vc", command, "--as", "bot", "--meeting-id", meetingID}
}

// CheckIdentity checks the local CLI's selected profile/app/bot credential
// readiness. It does not prove server-side scopes, bot Open ID or audio access.
func (c *MeetingClient) CheckIdentity(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := c.transport.Run(ctx, "--profile", c.config.Profile, "whoami", "--as", "bot")
	if err != nil {
		return errors.New("Feishu profile identity could not be checked")
	}
	var identity struct {
		Profile   string `json:"profile"`
		AppID     string `json:"appId"`
		Identity  string `json:"identity"`
		Available bool   `json:"available"`
	}
	if !singleJSON(out.Stdout, &identity) || identity.Profile != c.config.Profile || identity.AppID != c.config.AppID || identity.Identity != "bot" || !identity.Available {
		return errors.New("Feishu profile does not match the configured app or bot credentials are unavailable")
	}
	return nil
}

// MutationResult distinguishes confirmed acceptance from failure before a write
// and an uncertain remote outcome. Acceptance does not prove bot membership or
// that a participant has read/heard the result.
type MutationResult struct {
	Outcome   string
	MeetingID string
}

// JoinRequest selects a nine-digit meeting number. ExpectedMeetingID and CallID
// may come from a verified invitation. Persist intent before calling Join.
type JoinRequest struct {
	MeetingNumber     string
	ExpectedMeetingID string
	CallID            string
}

// Join makes one entry attempt. Check Membership after an accepted result;
// neither acceptance nor a join-user field proves the configured bot is present.
func (c *MeetingClient) Join(ctx context.Context, request JoinRequest) (MutationResult, error) {
	if !numberPattern.MatchString(request.MeetingNumber) || request.ExpectedMeetingID != "" && !meetingPattern.MatchString(request.ExpectedMeetingID) || !boundedIdentifier(request.CallID, 256, true) {
		return MutationResult{Outcome: Failed}, errors.New("invalid Feishu meeting join request")
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{Outcome: Failed}, err
	}
	args := []string{"--profile", c.config.Profile, "vc", "+meeting-join", "--as", "bot", "--meeting-number", request.MeetingNumber, "--format", "json"}
	if request.CallID != "" {
		args = append(args, "--call-id", request.CallID)
	}
	e, outcome, err := c.mutate(ctx, args)
	result := MutationResult{Outcome: outcome}
	if err != nil {
		return result, err
	}
	var joined struct {
		Meeting struct {
			ID     string `json:"id"`
			Number string `json:"meeting_no"`
		} `json:"meeting"`
	}
	if json.Unmarshal(e.Data, &joined) != nil || !meetingPattern.MatchString(joined.Meeting.ID) || joined.Meeting.Number != "" && joined.Meeting.Number != request.MeetingNumber || request.ExpectedMeetingID != "" && joined.Meeting.ID != request.ExpectedMeetingID {
		return MutationResult{Outcome: Unknown}, errors.New("Feishu join acknowledgement has an invalid meeting identity")
	}
	result.MeetingID = joined.Meeting.ID
	return result, nil
}

// Leave departs only the specified meeting. It never ends the meeting for other
// participants. An uncertain result requires a Membership check before retry.
func (c *MeetingClient) Leave(ctx context.Context, meetingID string) (MutationResult, error) {
	if !meetingPattern.MatchString(meetingID) {
		return MutationResult{Outcome: Failed}, errors.New("leave requires a long meeting ID")
	}
	_, outcome, err := c.mutate(ctx, append(c.arguments("+meeting-leave", meetingID), "--format", "json"))
	return MutationResult{Outcome: outcome, MeetingID: meetingID}, err
}

func (c *MeetingClient) mutate(ctx context.Context, args []string) (envelope, string, error) {
	if err := ctx.Err(); err != nil {
		return envelope{}, Failed, err
	}
	out, runErr := c.transport.Run(ctx, args...)
	if runErr != nil && !out.Started {
		return envelope{}, Failed, errors.New("Feishu command did not start")
	}
	e, err := decodeEnvelope(out)
	if err != nil {
		return e, Unknown, errors.New("Feishu mutation is unconfirmed; do not retry automatically")
	}
	if !*e.OK {
		switch e.Error.Type {
		case "validation", "auth", "authorization", "permission":
			return e, Failed, errors.New("Feishu rejected validation or authorization")
		}
		return e, Unknown, errors.New("Feishu mutation is unconfirmed; do not retry automatically")
	}
	var ack map[string]json.RawMessage
	if runErr != nil || json.Unmarshal(e.Data, &ack) != nil || ack == nil {
		return e, Unknown, errors.New("Feishu mutation acknowledgement is invalid or incomplete")
	}
	return e, Accepted, nil
}
