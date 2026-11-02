package offloadexec

import (
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxCommandLen = 4096
	maxNameLen    = 256
	maxPathLen    = 4096
	maxTimeout    = 24 * time.Hour
	// MaxInputBytes bounds the combined task and internal context budget.
	MaxInputBytes = 1 << 20
	// MaxInstructionsBytes matches Offload's private instruction-file contract.
	MaxInstructionsBytes = 256 << 10
	// MaxOutputBytes bounds stdout and stderr each.
	MaxOutputBytes = 4 << 20
)

var correlationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// checkText rejects empty, oversized, non-UTF-8 and control-character values.
// multiline permits tab, newline and carriage return.
func checkText(field, s string, max int, multiline bool) error {
	if s == "" {
		return fmt.Errorf("offloadexec: %s is required", field)
	}
	if len(s) > max {
		return fmt.Errorf("offloadexec: %s exceeds %d bytes", field, max)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("offloadexec: %s is not valid UTF-8", field)
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("offloadexec: %s contains a control character", field)
		}
	}
	return nil
}
