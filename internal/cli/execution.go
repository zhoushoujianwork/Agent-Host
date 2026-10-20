package cli

import "agent-host/internal/harness/acp"

func loadExecutionOptions(o *options) error {
	o.executionProfile = nil
	o.executionFingerprint = ""
	if o.executionConfig == "" {
		return nil
	}
	profile, fingerprint, err := acp.Load(o.executionConfig)
	if err != nil {
		return err
	}
	o.executionProfile = &profile
	o.executionFingerprint = fingerprint
	return nil
}
