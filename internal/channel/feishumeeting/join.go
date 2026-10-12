package feishumeeting

import (
	"context"
	"encoding/json"
	"errors"
)

// joinMeeting makes exactly one mutation. The manager must persist joining first.
func joinMeeting(ctx context.Context, c Config, client Client, entry EntryMeeting) (string, string) {
	args := []string{"--profile", c.Profile, "vc", "+meeting-join", "--as", "bot", "--meeting-number", entry.MeetingNumber, "--format", "json"}
	if entry.CallID != "" {
		args = append(args, "--call-id", entry.CallID)
	}
	out, runErr := selectedClient(client).Run(ctx, args...)
	if runErr != nil && !out.Started {
		return "", Failed
	}
	e, err := decodeEnvelope(out)
	if err != nil {
		return "", Unknown
	}
	if !*e.OK {
		if e.Error.Type == "validation" || e.Error.Type == "auth" || e.Error.Type == "authorization" || e.Error.Type == "permission" {
			return "", Failed
		}
		return "", Unknown
	}
	var result struct {
		Meeting struct {
			ID     string `json:"id"`
			Number string `json:"meeting_no"`
		} `json:"meeting"`
	}
	if runErr != nil || json.Unmarshal(e.Data, &result) != nil || !meetingID.MatchString(result.Meeting.ID) || result.Meeting.Number != "" && result.Meeting.Number != entry.MeetingNumber || entry.MeetingID != "" && result.Meeting.ID != entry.MeetingID {
		return "", Unknown
	}
	// The official meeting SDK identifies a successful join through meeting.id.
	// Join-user fields do not prove bot presence; the manager independently
	// checks the configured bot Open ID in this meeting before polling starts.
	return result.Meeting.ID, Accepted
}

// Membership is deliberately tri-state: incomplete/auth/error responses never
// establish absence and must not authorize another join mutation.
func meetingMembership(ctx context.Context, c Config, client Client, id string, expectedNumber ...string) (string, error) {
	result, err := readMeetingMembership(ctx, c, client, id, expectedNumber...)
	return result.Status, err
}

type meetingMembershipDetails struct {
	Status        string
	MeetingNumber string
}

// A verified long-ID response may also identify an initially numberless seed.
// Its number is useful for later entry lookup even when presence is unsettled.
func readMeetingMembership(ctx context.Context, c Config, client Client, id string, expectedNumber ...string) (meetingMembershipDetails, error) {
	details := meetingMembershipDetails{Status: Unknown}
	if !meetingID.MatchString(id) {
		return details, errors.New("membership requires a known long meeting ID")
	}
	args := []string{"--profile", c.Profile, "vc", "meeting", "get", "--as", "bot", "--meeting-id", id, "--with-participants", "--user-id-type", "open_id", "--format", "json"}
	out, runErr := selectedClient(client).Run(ctx, args...)
	e, err := decodeEnvelope(out)
	if runErr != nil || err != nil || !*e.OK {
		return details, errors.New("meeting membership could not be verified")
	}
	var result struct {
		Meeting struct {
			ID           string          `json:"id"`
			Number       string          `json:"meeting_no"`
			Status       json.RawMessage `json:"status"`
			Participants json.RawMessage `json:"participants"`
		} `json:"meeting"`
	}
	if json.Unmarshal(e.Data, &result) != nil || result.Meeting.ID != id || result.Meeting.Number != "" && !entryMeetingNumberPattern.MatchString(result.Meeting.Number) || len(expectedNumber) > 0 && expectedNumber[0] != "" && result.Meeting.Number != "" && result.Meeting.Number != expectedNumber[0] {
		return details, errors.New("meeting membership response has an invalid identity")
	}
	details.MeetingNumber = result.Meeting.Number
	if scalar(result.Meeting.Status, "3") {
		details.Status = "ended"
		return details, nil
	}
	if !scalar(result.Meeting.Status, "2") || len(result.Meeting.Participants) == 0 || string(result.Meeting.Participants) == "null" {
		return details, errors.New("meeting membership response is incomplete")
	}
	var participants []struct {
		ID     string          `json:"id"`
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal(result.Meeting.Participants, &participants) != nil {
		return details, errors.New("meeting participants are malformed")
	}
	unsettled := false
	for _, participant := range participants {
		if participant.ID != c.BotOpenID {
			continue
		}
		if scalar(participant.Status, "2") {
			details.Status = "present"
			return details, nil
		}
		if !scalar(participant.Status, "4") {
			unsettled = true
		}
	}
	if unsettled {
		return details, errors.New("bot membership is not settled")
	}
	details.Status = "absent"
	return details, nil
}
