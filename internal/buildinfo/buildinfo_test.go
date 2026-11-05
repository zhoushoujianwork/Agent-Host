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

const fixtureRevision = "0123456789abcdef0123456789abcdef01234567"

func withSource(t *testing.T, revision, modified string) {
	t.Helper()
	oldRevision, oldModified := SourceRevision, SourceModified
	SourceRevision, SourceModified = revision, modified
	t.Cleanup(func() { SourceRevision, SourceModified = oldRevision, oldModified })
}

func TestSourceFallbackUsedWithoutNativeVCSSettings(t *testing.T) {
	for _, tc := range []struct {
		modified string
		want     bool
	}{{"false", false}, {"true", true}} {
		withSource(t, fixtureRevision, tc.modified)
		for _, b := range []*debug.BuildInfo{nil, {GoVersion: "go1.fixture", Settings: []debug.BuildSetting{{Key: "-buildmode", Value: "exe"}}}} {
			info := fromBuildInfo(b)
			if info.Revision != fixtureRevision || info.Modified != tc.want {
				t.Fatalf("fallback not used for modified=%s: %+v", tc.modified, info)
			}
		}
	}
}

func TestNativeVCSSettingsOverrideSourceFallback(t *testing.T) {
	withSource(t, fixtureRevision, "true")
	info := fromBuildInfo(&debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "native"}, {Key: "vcs.modified", Value: "false"},
	}})
	if info.Revision != "native" || info.Modified {
		t.Fatalf("fallback overrode native metadata: %+v", info)
	}
	// Native revision without a modified flag is clean, never taken from the fallback.
	info = fromBuildInfo(&debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "native"}}})
	if info.Revision != "native" || info.Modified {
		t.Fatalf("fallback leaked into native metadata: %+v", info)
	}
}

func TestMalformedOrMissingSourceFallbackStaysUnknown(t *testing.T) {
	for _, tc := range [][2]string{{"", ""}, {fixtureRevision, ""}, {"", "false"}, {"abc", "false"}, {strings.ToUpper(fixtureRevision), "false"},
		{fixtureRevision + "0", "false"}, {fixtureRevision, "yes"}, {fixtureRevision, "True"}, {"HEAD", "true"}} {
		withSource(t, tc[0], tc[1])
		info := fromBuildInfo(nil)
		if info.Revision != "" || info.Modified {
			t.Fatalf("malformed fallback %q accepted: %+v", tc, info)
		}
	}
}
