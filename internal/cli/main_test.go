package cli

import (
	"os"
	"testing"

	"agent-host/internal/channel/feishumeeting"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == feishumeeting.EntryWorkerFlag {
		os.Exit(feishumeeting.EntryWorker(os.Args[2:]))
	}
	os.Exit(m.Run())
}
