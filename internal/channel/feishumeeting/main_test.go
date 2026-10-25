package feishumeeting

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == EntryWorkerFlag {
		os.Exit(EntryWorker(os.Args[2:]))
	}
	os.Exit(m.Run())
}
