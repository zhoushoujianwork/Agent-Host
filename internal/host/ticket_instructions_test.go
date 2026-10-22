package host

import (
	"strings"
	"testing"
)

func TestTicketPolicySeparatesInterpretationChosenActionAndClosure(t *testing.T) {
	for _, policy := range []string{ChannelInstructions("", false), ChannelInstructions("", true), PrivateUserChannelInstructions(""), MeetingChannelInstructions("")} {
		for _, expected := range []string{"dingtalk.read_ticket", "Reuse an existing instance ID directly", "never guess a different ticket", "first explain it", "Then ask one concrete question", "Forwarding a card does not authorize", "An explicit follow-up choice is authorization", "oa approval oa-comments", "oa approval approve", "A running instance with processInstanceResult=agree is still open", "do not repeat the successful write", "A separate POP work-order lifecycle"} {
			if !strings.Contains(policy, expected) {
				t.Fatalf("ticket policy omitted %q", expected)
			}
		}
	}
}
