// Package buildinfo exposes the identity embedded in the running binary.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Version can be set with -ldflags '-X agent-host/internal/buildinfo.Version=...'.
var Version = "0.2.0-dev"

type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Modified  bool   `json:"modified"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// Current reads the executable's build metadata, never the current checkout.
// A build without VCS metadata retains an empty Revision.
func Current() Info {
	b, _ := debug.ReadBuildInfo()
	return fromBuildInfo(b)
}

func fromBuildInfo(b *debug.BuildInfo) Info {
	info := Info{Version: Version, GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if b == nil {
		return info
	}
	if b.GoVersion != "" {
		info.GoVersion = b.GoVersion
	}
	for _, setting := range b.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Revision = setting.Value
		case "vcs.modified":
			info.Modified = setting.Value == "true"
		}
	}
	return info
}
