package dream

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ScanPage streams directory names but reads artifact contents for only one
// bounded page. The persisted lexical cursor rotates, revisiting changed old
// handoffs without imposing the manual scanner's total-history size limit.
func ScanPage(ctx context.Context, source string, destination Destination, after string, limit int) (Report, string, error) {
	r := Report{Version: PolicyVersion, PolicyVersion: PolicyVersion, Source: source, Destination: destination, Status: "scan_only", Entries: []Entry{}}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || limit < 1 || limit > maxDirectories {
		return r, after, errors.New("invalid bounded handoff scan")
	}
	if err := validateIdentity(destination); err != nil {
		return r, after, err
	}
	if err := privateDirectory(source); err != nil {
		return r, after, err
	}
	dir, err := os.Open(source)
	if err != nil {
		return r, after, err
	}
	defer dir.Close()
	names := make([]string, 0, limit)
	for {
		if err = ctx.Err(); err != nil {
			return r, after, err
		}
		entries, e := dir.ReadDir(256)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "handoff-") || name <= after {
				continue
			}
			index := sort.SearchStrings(names, name)
			if index >= limit {
				continue
			}
			names = append(names, "")
			copy(names[index+1:], names[index:])
			names[index] = name
			if len(names) > limit {
				names = names[:limit]
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return r, after, e
		}
	}
	next := ""
	if len(names) == 0 && after != "" {
		return ScanPage(ctx, source, destination, "", limit)
	}
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return r, after, err
		}
		r.Entries = append(r.Entries, (Scanner{}).scanDirectory(source, name, destination))
		next = name
	}
	return r, next, nil
}
