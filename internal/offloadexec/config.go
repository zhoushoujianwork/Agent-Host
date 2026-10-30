package offloadexec

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxCommandLen = 4096
	maxNameLen    = 256
	maxPathLen    = 4096
	maxTimeout    = 24 * time.Hour
	// MaxInputBytes bounds the stdin payload: Instructions, the two-byte
	// separator that follows them when present, and Prompt.
	MaxInputBytes = 1 << 20
	// MaxOutputBytes bounds stdout and stderr each.
	MaxOutputBytes = 4 << 20
)

// Config selects the Offload executable and the default profile.
type Config struct {
	// Command is a clean absolute path to the offload executable.
	Command string
	// Profile is the profile passed to fresh runs (and to resumes that carry
	// no ResumeProfile). Offload may resolve it to a canonical name.
	Profile string
	// Model optionally overrides the profile's model (-m).
	Model string
	// Timeout is passed to Offload as -t and also bounds the client wait.
	Timeout time.Duration
}

// Validate reports the first invalid field, or an unsupported platform. The
// client supports Unix only because it promises to terminate and reap the whole
// process group it created. It does not touch the filesystem.
func (c Config) Validate() error {
	if !processGroupsSupported {
		return errors.New("offloadexec: unsupported platform: owned process-group cleanup requires Unix")
	}
	if c.Command == "" || !filepath.IsAbs(c.Command) || filepath.Clean(c.Command) != c.Command {
		return errors.New("offloadexec: Command must be a clean absolute path")
	}
	if err := checkText("Command", c.Command, maxCommandLen, false); err != nil {
		return err
	}
	if err := checkText("Profile", c.Profile, maxNameLen, false); err != nil {
		return err
	}
	if c.Model != "" {
		if err := checkText("Model", c.Model, maxNameLen, false); err != nil {
			return err
		}
	}
	if c.Timeout <= 0 || c.Timeout > maxTimeout {
		return fmt.Errorf("offloadexec: Timeout must be in (0, %s]", maxTimeout)
	}
	return nil
}

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
