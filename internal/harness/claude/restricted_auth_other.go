//go:build !unix

package claude

import "os"

func trustedRestrictedAuthEntry(os.FileInfo) bool { return false }
