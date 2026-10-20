package acp

import (
	"os"
	"path/filepath"
	"sync"
)

// Adapter diagnostics may contain account/configuration details. Keep bounded
// source evidence in the private native profile, never the public event stream.
type diagnostics struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (d *diagnostics) Write(raw []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(raw)
	remaining := (64 << 10) - len(d.data)
	if len(raw) > remaining {
		raw = raw[:remaining]
		d.truncated = true
	}
	d.data = append(d.data, raw...)
	return n, nil
}
func (d *diagnostics) save(profile string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.data) == 0 {
		return nil
	}
	dir := filepath.Join(profile, "agent-host-acp", "diagnostics")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return os.ErrPermission
	}
	file, err := os.CreateTemp(dir, "run-*.log")
	if err != nil {
		return err
	}
	_, err = file.Write(d.data)
	if d.truncated && err == nil {
		_, err = file.WriteString("\n[diagnostics truncated at 64 KiB]\n")
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
