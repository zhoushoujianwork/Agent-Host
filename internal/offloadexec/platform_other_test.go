//go:build !unix

package offloadexec

import (
	"testing"
	"time"
)

func TestUnsupportedPlatformRejected(t *testing.T) {
	if _, err := NewLibrary(`C:\offload.json`, "p", "", time.Second); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}
