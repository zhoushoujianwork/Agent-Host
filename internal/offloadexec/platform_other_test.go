//go:build !unix

package offloadexec

import (
	"testing"
	"time"
)

func TestUnsupportedPlatformRejected(t *testing.T) {
	cfg := Config{Command: `C:\offload.exe`, Profile: "p", Timeout: time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an unsupported platform")
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted an unsupported platform")
	}
}
