//go:build unix

package nativepolicy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/offloadexec"
	"github.com/google/uuid"
)

func TestCodexTaskSkillLiveResumeAcrossRunPackages(t *testing.T) {
	if os.Getenv("AGH_TASK_SKILLS_LIVE") != "1" {
		t.Skip("explicit native acceptance only")
	}
	library, err := offloadexec.DefaultLibrary("", 2*time.Minute)
	if err != nil || library.Backend() != "codex" {
		t.Fatalf("acceptance requires the configured Codex default: %v", err)
	}
	firstPackage, nextPackage := nativeSkillFixture(t), nativeSkillFixture(t)
	if firstPackage.Path == nextPackage.Path || firstPackage.Digest != nextPackage.Digest {
		t.Fatal("fixture must provide equal publications from different run paths")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "native")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	var mounts []string
	toolCalls := 0
	observe := func(o harness.Observation) error {
		if o.SkillMount != nil {
			mounts = append(mounts, o.SkillMount.Directory)
		}
		if o.Tool != nil && o.Tool.Stage == "call" {
			toolCalls++
		}
		return nil
	}
	request := harness.Request{HostSessionID: "live-task-" + uuid.NewString(), NativeProfile: profile, Workspace: root, PermissionMode: "bypassPermissions", SkillPluginDir: firstPackage.Path, SkillPluginDigest: firstPackage.Digest, Prompt: "Reply only AGH_TASK_SKILL_FIRST_OK. Do not use tools, read files, or change anything."}
	adapter := NewWithLibrary("", library)
	first, err := adapter.Run(context.Background(), request, observe)
	if err != nil || first.Status != "completed" || strings.TrimSpace(first.Text) != "AGH_TASK_SKILL_FIRST_OK" || first.NativeSessionID == "" {
		t.Fatalf("first status=%s text=%q err=%v", first.Status, first.Text, err)
	}
	request.Resume, request.NativeSessionID = true, first.NativeSessionID
	request.SkillPluginDir, request.SkillPluginDigest = nextPackage.Path, nextPackage.Digest
	request.Prompt = "Reply only AGH_TASK_SKILL_RESUME_OK. Do not use tools, read files, or change anything."
	next, err := adapter.Run(context.Background(), request, observe)
	if err != nil || next.Status != "completed" || next.NativeSessionID != first.NativeSessionID || strings.TrimSpace(next.Text) != "AGH_TASK_SKILL_RESUME_OK" {
		t.Fatalf("resume status=%s text=%q err=%v", next.Status, next.Text, err)
	}
	if len(mounts) != 2 || mounts[0] != mounts[1] || toolCalls != 0 {
		t.Fatalf("stable mounts=%v tool_calls=%d", mounts, toolCalls)
	}
	if _, err := os.Lstat(filepath.Join(profile, "environments", request.HostSessionID, "codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("native config was not cleaned: %v", err)
	}
	t.Logf("model=%s profile=%s native_session=%s same_manifest_different_runs=true stable_mount=true resumed=true", next.Model, next.Evidence.Profile, next.NativeSessionID)
}
