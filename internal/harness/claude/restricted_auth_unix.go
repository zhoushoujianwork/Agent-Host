//go:build unix

package claude

import (
	"os"
	"syscall"
)

func trustedRestrictedAuthEntry(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0022 == 0
}
