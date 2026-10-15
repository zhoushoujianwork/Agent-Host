package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"agent-host/internal/backup"
)

func runBackup(ctx context.Context, args []string, out, errout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agent-host backup init|create|verify|restore|list|status")
	}
	command := args[0]
	fs := flag.NewFlagSet("backup "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var root, destination, identity, timezone, archive, target string
	var due bool
	fs.StringVar(&root, "root", ".agent-host", "Host workspace root")
	fs.StringVar(&destination, "destination", "", "Archive directory; default ROOT/backups (init)")
	fs.StringVar(&identity, "identity", "", "Separate private age recovery key")
	fs.StringVar(&timezone, "timezone", "Local", "Calendar timezone for retention (init)")
	fs.StringVar(&archive, "archive", "", "Encrypted .tar.gz.age archive (verify/restore)")
	fs.StringVar(&target, "target", "", "New, nonexistent Host root (restore)")
	fs.BoolVar(&due, "due", false, "Only create an overdue automatic recovery point")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected backup arguments")
	}
	var e error
	root, e = filepath.Abs(root)
	if e != nil {
		return e
	}
	print := func(v any) error { enc := json.NewEncoder(out); enc.SetIndent("", "  "); return enc.Encode(v) }
	switch command {
	case "init":
		home, e := os.UserHomeDir()
		if e != nil {
			return e
		}
		if destination == "" {
			destination = filepath.Join(root, "backups")
		}
		if identity == "" {
			hash := sha256.Sum256([]byte(root))
			identity = filepath.Join(home, ".config", "agent-host", "backup-keys", fmt.Sprintf("%x.key", hash[:8]))
		}
		if e = backup.Init(root, destination, identity, timezone); e != nil {
			return e
		}
		return print(map[string]any{"enabled": true, "destination": destination, "identity_file": identity, "format": "tar.gz.age", "notice": "Keep a recovery-key copy separately. This local destination may share the source disk."})
	case "create":
		point, e := backup.Create(ctx, root, due)
		if e != nil {
			return e
		}
		return print(point)
	case "list":
		points, e := backup.List(root)
		if e != nil {
			return e
		}
		return print(points)
	case "status":
		s, e := backup.ReadStatus(root)
		if e != nil {
			return e
		}
		return print(s)
	case "verify", "restore":
		if archive == "" {
			return errors.New("--archive is required")
		}
		if identity == "" {
			p, e := backup.LoadPolicy(root)
			if e != nil {
				return errors.New("provide --identity or an existing --root backup policy")
			}
			identity = p.IdentityFile
		}
		if command == "verify" {
			m, e := backup.Verify(ctx, archive, identity)
			if e != nil {
				return e
			}
			return print(map[string]any{"verified": true, "manifest": m})
		}
		if target == "" {
			return errors.New("--target is required")
		}
		target, e = filepath.Abs(target)
		if e != nil {
			return e
		}
		m, e := backup.Restore(ctx, archive, identity, target)
		if e != nil {
			return e
		}
		return print(map[string]any{"restored": true, "target": target, "external_dependencies": m.Dependencies, "channels_started": false})
	default:
		return errors.New("unknown backup command")
	}
}
