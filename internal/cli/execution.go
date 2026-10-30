package cli

import (
	"errors"

	"agent-host/internal/harness/acp"
	"agent-host/internal/memory"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
)

func loadExecutionOptions(o *options) error {
	o.executionProfile = nil
	o.executionFingerprint = ""
	o.offloadProfile = nil
	o.offloadClient = nil
	o.offloadMemory = nil
	if o.offloadConfig != "" && o.executionConfig != "" {
		return errors.New("--offload-config and --execution-config are mutually exclusive")
	}
	if o.offloadConfig != "" {
		profile, fingerprint, err := offloadchannel.Load(o.offloadConfig)
		if err != nil {
			return err
		}
		client, err := offloadexec.New(profile.ClientConfig())
		if err != nil {
			return err
		}
		var plugin *offloadchannel.MemoryPlugin
		if profile.MemoryEnabled {
			plugin = &offloadchannel.MemoryPlugin{Timeout: profile.Memory.Timeout}
			if profile.Memory.Processing {
				decisionClient, err := newJevClient(profile.Memory.Provider, profile.Memory.Endpoint, profile.Memory.Model)
				if err != nil {
					return err
				}
				plugin.Decider = memory.New(decisionClient)
			}
		}
		o.offloadProfile, o.offloadClient, o.executionFingerprint, o.offloadMemory = &profile, client, fingerprint, plugin
		return nil
	}
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
