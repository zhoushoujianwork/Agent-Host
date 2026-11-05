package cli

import (
	"errors"
	"fmt"

	"agent-host/internal/memory"
	"agent-host/internal/offloadchannel"
	"agent-host/internal/offloadexec"
)

func loadExecutionOptions(o *options) error {
	if o.dreamConfig != "" && fullHostOptions(*o) {
		return errors.New("dream runtime currently requires direct Offload Chat or channel execution; Full Host catalog/project execution is not supported")
	}
	if err := loadMemoryExecutionOptions(o); err != nil {
		return err
	}
	o.executionFingerprint = ""
	o.offloadProfile = nil
	o.offloadClient = nil
	o.offloadMemory = nil
	if o.offloadConfig != "" {
		profile, fingerprint, err := offloadchannel.Load(o.offloadConfig)
		if err != nil {
			return err
		}
		client, err := profile.NewClient()
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
		if o.fullHost {
			o.offloadProfile = nil
			if o.model == "" {
				o.model = client.(*offloadexec.Library).Model()
			}
		}
		return nil
	}
	if !o.nativeExecution {
		return nil
	}
	library, err := offloadexec.DefaultLibrary(o.model, o.timeout)
	if err != nil {
		return err
	}
	o.offloadClient = library
	o.executionFingerprint = offloadchannel.FingerprintPrefix + library.Fingerprint()
	if o.model == "" {
		o.model = library.Model()
	}
	if !fullHostOptions(*o) {
		profile := offloadchannel.FromLibrary(library, o.timeout)
		o.offloadProfile = &profile
	}
	return nil
}

func loadMemoryExecutionOptions(o *options) error {
	o.memoryOffloadClient = nil
	o.memoryExecutionFingerprint = ""
	o.memoryModel = ""
	if o.memoryOffloadConfig == "" {
		return nil
	}
	profile, fingerprint, err := offloadchannel.Load(o.memoryOffloadConfig)
	if err != nil {
		return fmt.Errorf("memory offload configuration: %w", err)
	}
	client, err := profile.NewClient()
	if err != nil {
		return fmt.Errorf("memory offload configuration: %w", err)
	}
	library, ok := client.(*offloadexec.Library)
	if !ok || library.Model() == "" {
		return errors.New("memory offload configuration must resolve an explicit model")
	}
	if backend := library.Backend(); backend != "claude" {
		return fmt.Errorf("memory offload configuration profile %q uses unsupported extraction backend %q", library.ProfileName(), backend)
	}
	o.memoryOffloadClient = client
	o.memoryExecutionFingerprint = fingerprint
	o.memoryModel = library.Model()
	return nil
}

func fullHostOptions(o options) bool {
	return o.fullHost || o.agentsConfig != "" || o.projectID != "" || len(o.projectIDs) > 0 || o.primaryProject != "" ||
		o.noTools || o.debugNonOwner || len(o.allowedTools) > 0 || o.nativeProfile != "" || o.permissionMode != "" ||
		o.emailConfig != "" || o.knowledgeConfig != "" || len(o.knowledgeConfigs) > 0 || o.dingtalkWorkspaceConfig != "" ||
		o.discoveryConfig != "" || o.skillsRoot != "" || o.workspaceCache != "" || len(o.mcpServers) > 0 || len(o.pluginNames) > 0 ||
		o.takeoverSession != "" || o.renewRuntime || o.requestFor != "" || o.notifyTo != "" || o.feedbackConfigDir != "" || o.knowledgeChannel != "" || o.memoryOffloadConfig != ""
}
