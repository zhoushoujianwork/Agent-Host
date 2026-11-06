package dream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanPageRotatesBeyondManualHistoryBudget(t *testing.T) {
	root := privateRoot(t)
	for i := 0; i < maxDirectories+3; i++ {
		if e := os.Mkdir(filepath.Join(root, fmt.Sprintf("handoff-%04d", i)), 0700); e != nil {
			t.Fatal(e)
		}
	}
	dest := Destination{PersonID: "owner", Agent: "agent", Audience: "owner", MemoryScope: "scope"}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		report, next, e := ScanPage(context.Background(), root, dest, cursor, 64)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range report.Entries {
			if seen[entry.Directory] {
				t.Fatal("page repeated before wrap", entry.Directory)
			}
			seen[entry.Directory] = true
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != maxDirectories+3 {
		t.Fatalf("incomplete cursor traversal: %d %q", len(seen), cursor)
	}
	report, _, e := ScanPage(context.Background(), root, dest, cursor, 64)
	if e != nil || len(report.Entries) != 64 || report.Entries[0].Directory != "handoff-0000" {
		t.Fatal("cursor did not rotate", e)
	}
}
