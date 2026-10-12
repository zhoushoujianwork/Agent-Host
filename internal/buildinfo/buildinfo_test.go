package buildinfo

import (
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildIdentityKeepsUnknownRevisionAndRuntime(t *testing.T) {
	info := fromBuildInfo(nil)
	if info.Revision != "" || info.Modified || info.Version != Version || info.GoVersion != runtime.Version() || info.GOOS != runtime.GOOS || info.GOARCH != runtime.GOARCH {
		t.Fatalf("missing metadata invented an identity or lost runtime: %+v", info)
	}
}

func TestBuildIdentityUsesEmbeddedRevisionWithoutPrivateSettings(t *testing.T) {
	info := fromBuildInfo(&debug.BuildInfo{GoVersion: "go1.26.fixture", Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
		{Key: "CGO_CFLAGS", Value: "/private/path token=private-value"},
	}})
	if info.Revision != "0123456789abcdef" || !info.Modified || info.GoVersion != "go1.26.fixture" {
		t.Fatalf("embedded identity lost: %+v", info)
	}
	raw, err := json.Marshal(info)
	if err != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "CGO_CFLAGS") {
		t.Fatalf("build metadata disclosed unrelated settings: %s %v", raw, err)
	}
}
