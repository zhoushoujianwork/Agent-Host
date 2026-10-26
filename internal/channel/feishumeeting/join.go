package feishumeeting

import (
	"context"
	feishulib "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"
)

// The manager persists intent before the shared library makes one join attempt.
func joinMeeting(ctx context.Context, c Config, transport Client, entry EntryMeeting) (string, string) {
	client, err := meetingClient(c, transport)
	if err != nil {
		return "", Failed
	}
	result, _ := client.Join(ctx, feishulib.JoinRequest{MeetingNumber: entry.MeetingNumber, ExpectedMeetingID: entry.MeetingID, CallID: entry.CallID})
	return result.MeetingID, result.Outcome
}

type meetingMembershipDetails = feishulib.Membership

func meetingMembership(ctx context.Context, c Config, transport Client, id string, expectedNumber ...string) (string, error) {
	result, err := readMeetingMembership(ctx, c, transport, id, expectedNumber...)
	return result.Status, err
}

func readMeetingMembership(ctx context.Context, c Config, transport Client, id string, expectedNumber ...string) (meetingMembershipDetails, error) {
	client, err := meetingClient(c, transport)
	if err != nil {
		return meetingMembershipDetails{Status: Unknown}, err
	}
	return client.Membership(ctx, id, expectedNumber...)
}
