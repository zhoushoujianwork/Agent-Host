// Package buildinfo exposes the identity embedded in the running binary.
package buildinfo

import (
	"regexp"
	"runtime"
	"runtime/debug"
)

// Version can be set with -ldflags '-X agent-host/internal/buildinfo.Version=...'.
var Version = "0.2.0-dev"

// SourceRevision and SourceModified carry the source checkout identity when the Go
// toolchain embeds no VCS settings. `make build` sets them with -ldflags; they are
// ignored when the binary has native vcs.revision, and unless both are well formed.
var (
	SourceRevision string
	SourceModified string
)

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Modified  bool   `json:"modified"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// Current reads the executable's build metadata, never the current checkout.
// Without native VCS settings it uses the explicit SourceRevision/SourceModified
// fallback, and only when that is a full commit plus "true" or "false". Otherwise
// Revision stays empty (unknown).
func Current() Info {
	b, _ := debug.ReadBuildInfo()
	return fromBuildInfo(b)
}

func fromBuildInfo(b *debug.BuildInfo) Info {
	info := Info{Version: Version, GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	native := false
	if b != nil {
		if b.GoVersion != "" {
			info.GoVersion = b.GoVersion
		}
		for _, setting := range b.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
				native = setting.Value != ""
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	if !native {
		info.Revision, info.Modified = "", false
		if fullRevision.MatchString(SourceRevision) && (SourceModified == "true" || SourceModified == "false") {
			info.Revision, info.Modified = SourceRevision, SourceModified == "true"
		}
	}
	return info
}
