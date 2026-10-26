package channel

import (
	"context"
	"testing"

	"agent-host/internal/state"
)

func TestOwnerPrivateCommandAuthenticationAndNormalDispatch(t *testing.T) {
	for _, scenario := range []string{"owner", "member", "group", "wrong-audience", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServiceFixture(t)
			calls := 0
			f.service.OwnerID = f.config.OwnerUserID
			f.service.OwnerPrivateCommand = func(_ context.Context, m state.ChannelMessage) (bool, string, error) {
				calls++
				return scenario != "ordinary", "Owner-only clarification result", nil
			}
			m := f.message(t, "clarification", "/answer question.1 details", "text", "owner-private")
			f.receive(t, m)
			receipt := f.next(t)
			switch scenario {
			case "member":
				receipt.SenderID = "peer"
			case "group":
				receipt.ConversationType = "2"
			case "wrong-audience":
				receipt.AudienceID = "peer"
			}
			// Direct processing here isolates admission guards; queued receipt remains
			// unchanged for ordinary execution/partition validation.
			if err := f.service.processMessage(t.Context(), receipt, nil); err != nil {
				t.Fatal(err)
			}
			if scenario == "owner" || scenario == "ordinary" {
				if calls != 1 {
					t.Fatal("owner callback missing")
				}
			} else if calls != 0 {
				t.Fatal("non-owner/private callback admitted")
			}
			if scenario == "owner" && len(f.runner.snapshot()) != 0 {
				t.Fatal("handled clarification ran ordinary native task")
			}
		})
	}
}
