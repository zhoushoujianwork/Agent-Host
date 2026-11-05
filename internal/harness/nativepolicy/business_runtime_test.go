package nativepolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-host/internal/businessruntime"
	"agent-host/internal/harness"
)

func TestBusinessRuntimeEnvironmentFreshResumeAndExtraction(t *testing.T) {
	profile, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profile, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(profile, "synthetic-source")
	if err := os.WriteFile(source, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	p := businessruntime.Policy{HomeFiles: []businessruntime.HomeFile{{Source: source, Target: "business/credential"}}}
	if err := p.Prepare(profile); err != nil {
		t.Fatal(err)
	}
	req := harness.Request{NativeProfile: profile, HostSessionID: "business-test"}
	env, _, err := executionEnvironment(req)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(env.Root, "home", "business", "credential")
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "first" {
		t.Fatal("fresh binding unavailable", err)
	}
	if err := os.WriteFile(source, []byte("rotated"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Resume = true
	if _, _, err := executionEnvironment(req); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "rotated" {
		t.Fatal("resumed binding not refreshed", err)
	}
	freshReq := req
	freshReq.HostSessionID, freshReq.Resume = "fresh-after-rotation", false
	fresh, _, err := executionEnvironment(freshReq)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(fresh.Root, "home", "business", "credential")); err != nil || string(raw) != "rotated" {
		t.Fatal("fresh binding not rotated", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executionEnvironment(req); err == nil {
		t.Fatal("missing source did not stop dispatch preparation")
	}
	// Even an invalid/missing business source is never read by extraction.
	req.Extraction, req.Resume = true, false
	extracted, cleanup, err := executionEnvironment(req)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(extracted.Root, "home", "business")); !os.IsNotExist(err) {
		t.Fatal("business credential entered extraction")
	}
	if err := os.Remove(filepath.Join(profile, businessruntime.Manifest)); err != nil {
		t.Fatal(err)
	}
	req.Extraction, req.Resume = false, true
	if _, _, err := executionEnvironment(req); err == nil {
		t.Fatal("removed policy adopted existing bound environment")
	}
}
