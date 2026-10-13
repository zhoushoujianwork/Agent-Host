package cli

import (
	"io"
	"path/filepath"
	"testing"
)

func TestCLIDefaultStateStaysInInvocationDirectory(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	o, _, err := parse("chat", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.home != filepath.Join(cwd, ".agent-host", "cli-state") {
		t.Fatal("CLI default escaped the project directory", o.home)
	}
	explicit := filepath.Join(cwd, "other-state")
	o, _, err = parse("chat", []string{"--home", explicit}, io.Discard)
	if err != nil || o.home != explicit {
		t.Fatal("explicit state selection changed", err)
	}
}
