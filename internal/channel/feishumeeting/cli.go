package feishumeeting

import feishulib "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"

// Host and external consumers use the same bounded CLI transport.
type Output = feishulib.Output
type Client = feishulib.Transport
type CLIClient = feishulib.CLITransport

func meetingClient(c Config, transport Client) (*feishulib.MeetingClient, error) {
	return feishulib.NewMeetingClient(feishulib.Config{Profile: c.Profile, AppID: c.AppID, BotOpenID: c.BotOpenID}, transport)
}
