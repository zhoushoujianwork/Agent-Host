package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func proposal() Proposal {
	return Proposal{"delete project", "/example/project", "Removes local files and uncommitted work", true}
}

func TestLifecycle(t *testing.T) {
	dir := privateDirectory(t)
	if _, err := Read(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	p := proposal()
	if err := queue(dir, p); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil || got != p {
		t.Fatalf("read: %#v %v", got, err)
	}
	if err := queue(dir, p); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	p.Target = "/example/different"
	if err := queue(dir, p); err == nil {
		t.Fatal("accepted a second distinct proposal")
	}
	got, err = Read(dir)
	if err != nil || got != proposal() {
		t.Fatal("retry changed original proposal")
	}
	s := Server("/bin/agent-host", dir)
	if s.Name != "approval" || s.Command != "/bin/agent-host" || strings.Join(s.Args, " ") != "approval-mcp --directory "+dir {
		t.Fatalf("server: %#v", s)
	}
}

func TestInvalidProposals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Proposal)
	}{
		{"private", func(p *Proposal) { p.Shareable = false }},
		{"empty", func(p *Proposal) { p.Operation = "   " }},
		{"control", func(p *Proposal) { p.Impact = "first\nsecond" }},
		{"format", func(p *Proposal) { p.Target = "target\u202e" }},
		{"utf8", func(p *Proposal) { p.Target = string([]byte{255}) }},
		{"operation limit", func(p *Proposal) { p.Operation = strings.Repeat("a", 257) }},
		{"target limit", func(p *Proposal) { p.Target = strings.Repeat("a", 513) }},
		{"impact limit", func(p *Proposal) { p.Impact = strings.Repeat("a", 1501) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proposal()
			tc.change(&p)
			dir := privateDirectory(t)
			if err := queue(dir, p); err == nil {
				t.Fatal("invalid proposal accepted")
			}
			if _, err := Read(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected proposal: %v", err)
			}
		})
	}
}

func TestDirectoryAndFileRestrictions(t *testing.T) {
	dir := privateDirectory(t)
	link := filepath.Join(privateDirectory(t), "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"relative", link, filepath.Join(dir, "missing")} {
		if err := Serve(context.Background(), invalid, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted directory %q", invalid)
		}
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := queue(dir, proposal()); err == nil {
		t.Fatal("accepted public directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proposal.json")
	outside := filepath.Join(privateDirectory(t), "outside")
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("accepted symlink proposal")
	}
	if err := queue(dir, proposal()); err == nil {
		t.Fatal("overwrote symlink proposal")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Repeat("x", 4097), `{"operation":"delete","operation":"other"}`, `{"command":"rm"}`, `null`, `{}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir); err == nil {
			t.Fatalf("accepted file: %.50s", raw)
		}
	}
}

func TestMCPContract(t *testing.T) {
	for _, extra := range []string{``, `,"owner_id":"forged"`, `,"command":"rm -rf /"`, `,"directory":"/tmp"`, `,"operation":"forged"`, `,"impact":null`} {
		t.Run(extra, func(t *testing.T) {
			dir := privateDirectory(t)
			p, _ := json.Marshal(proposal())
			args := strings.TrimSuffix(string(p), "}") + extra + "}"
			input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{}}}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
				`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_approval","arguments":` + args + `}}` + "\n" +
				`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"execute","arguments":{}}}` + "\n"
			var out bytes.Buffer
			if err := Serve(context.Background(), dir, strings.NewReader(input), &out); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 4 {
				t.Fatalf("output: %s", out.String())
			}
			if !strings.Contains(lines[1], `"name":"request_approval"`) || strings.Contains(lines[1], `"name":"execute"`) {
				t.Fatalf("catalog: %s", lines[1])
			}
			if !strings.Contains(lines[3], `"error"`) {
				t.Fatal("unknown tool accepted")
			}
			_, err := Read(dir)
			if extra == "" {
				if err != nil || !strings.Contains(lines[2], `"owner_notified":false`) {
					t.Fatalf("proposal not queued: %s %v", lines[2], err)
				}
			} else if !errors.Is(err, os.ErrNotExist) || (!strings.Contains(lines[2], `"isError":true`) && !strings.Contains(lines[2], `"error"`)) {
				t.Fatalf("forged contract accepted: %s %v", lines[2], err)
			}
		})
	}
}
