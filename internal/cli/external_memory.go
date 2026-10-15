package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"agent-host/internal/state"
)

const externalMemoryUsage = `External agent memory (local running Host, file-memory backend):
  agent-host memory connection add --connection NAME --home STATE_HOME [--agent ID] [--project ID] [--checkpoint-save]
  agent-host memory connection show|revoke --connection NAME
  agent-host memory submit --connection NAME --input FILE_OR_DASH
  agent-host memory receipt --connection NAME --receipt ID
  agent-host memory receipt --connection NAME --key IDEMPOTENCY_KEY
  agent-host memory recall --connection NAME --query TEXT
  agent-host memory read --connection NAME --memory ID [--revision N]
  agent-host memory associate --connection NAME --memory ID --revision N --input FILE_OR_DASH

All output is JSON. --json is accepted for explicitness. --connections FILE
overrides ~/.config/agent-host/memory-connections.json. --connection defaults to
AGENT_HOST_MEMORY_CONNECTION or "default". Submit acknowledges durable intake;
query its receipt until processing_status is completed or failed. Installation
does not enable checkpoint collection; --checkpoint-save is an explicit opt-in.
`

type externalProfile struct {
	Home           string `json:"home"`
	Binding        string `json:"binding"`
	CheckpointSave bool   `json:"checkpoint_save"`
}

func isExternalMemoryCommand(s string) bool {
	return s == "connection" || s == "submit" || s == "receipt" || s == "recall" || s == "read" || s == "associate"
}

func loadExternalProfiles(path string) (map[string]externalProfile, error) {
	profiles := map[string]externalProfile{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 64<<10 {
		return nil, errors.New("memory connection profile exceeds 64 KiB")
	}
	err = json.Unmarshal(b, &profiles)
	if profiles == nil {
		return nil, errors.New("memory connection profiles must be an object")
	}
	return profiles, err
}

func saveExternalProfiles(path string, p map[string]externalProfile) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".memory-connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func externalMemoryCall(ctx context.Context, home string, a externalMemoryRequest) (json.RawMessage, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(home, "memory-control.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent-host/external-memory", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memory Host unavailable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) != nil || failure.Error == "" {
			return nil, fmt.Errorf("memory Host returned HTTP %d", resp.StatusCode)
		}
		return nil, errors.New(failure.Error)
	}
	if !json.Valid(data) {
		return nil, errors.New("memory Host returned invalid JSON")
	}
	return data, nil
}

func runExternalMemory(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) == 0 {
		return errors.New("external memory operation is required")
	}
	command := args[0]
	args = args[1:]
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, externalMemoryUsage)
		return err
	}
	if command == "connection" {
		if len(args) == 0 {
			_, err := io.WriteString(out, externalMemoryUsage)
			return err
		}
		command = "connection-" + args[0]
		args = args[1:]
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	name := os.Getenv("AGENT_HOST_MEMORY_CONNECTION")
	if name == "" {
		name = "default"
	}
	var profilesPath, home, input string
	var checkpoint, jsonOutput bool
	a := externalMemoryRequest{Action: command}
	fs := flag.NewFlagSet("memory "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	fs.StringVar(&name, "connection", name, "Local connection alias")
	fs.StringVar(&profilesPath, "connections", filepath.Join(userHome, ".config", "agent-host", "memory-connections.json"), "Local connection profiles")
	fs.StringVar(&home, "home", "", "Running Host state home (connection add only)")
	fs.StringVar(&a.Agent, "agent", "", "Configured Agent (connection add only)")
	fs.StringVar(&a.Project, "project", "", "Registered project (connection add only)")
	fs.BoolVar(&checkpoint, "checkpoint-save", false, "Enable checkpoint submissions for this connection")
	fs.StringVar(&input, "input", "", "Submission JSON file or - for stdin")
	fs.StringVar(&a.Receipt, "receipt", "", "Receipt ID")
	fs.StringVar(&a.Key, "key", "", "Original submission key")
	fs.StringVar(&a.Query, "query", "", "Recall query")
	fs.StringVar(&a.Memory, "memory", "", "Memory ID")
	fs.IntVar(&a.Revision, "revision", 0, "Expected memory revision")
	fs.BoolVar(&jsonOutput, "json", true, "JSON output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, externalMemoryUsage)
			return err
		}
		return err
	}
	if fs.NArg() != 0 || !agentIdentifier.MatchString(name) {
		return errors.New("use named options and a bounded connection alias")
	}
	profiles, err := loadExternalProfiles(profilesPath)
	if err != nil {
		return err
	}
	p, exists := profiles[name]
	if command == "connection-add" {
		if home == "" {
			return errors.New("connection add requires --home identifying one running Host")
		}
		home, err = filepath.Abs(home)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("connection alias already exists; choose a new alias")
		}
		p = externalProfile{Home: home, Binding: name, CheckpointSave: checkpoint}
	} else if !exists {
		return fmt.Errorf("memory connection %q is not configured; use memory connection add", name)
	}
	a.Connection = p.Binding
	if command != "connection-add" && (home != "" || a.Agent != "" || a.Project != "" || checkpoint) {
		return errors.New("scope and checkpoint options are for connection add only")
	}
	if command == "submit" || command == "associate" {
		if input == "" {
			return fmt.Errorf("%s requires --input FILE or -", command)
		}
		reader := in
		if input != "-" {
			f, err := os.Open(input)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		b, err := io.ReadAll(io.LimitReader(reader, state.MaxExternalBytes+1))
		if err != nil {
			return err
		}
		if len(b) > state.MaxExternalBytes {
			return errors.New("submission exceeds 64 KiB")
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if command == "associate" {
			if a.Memory == "" || a.Revision < 1 {
				return errors.New("associate requires --memory and a positive expected --revision")
			}
			if err := d.Decode(&a.Objects); err != nil {
				return err
			}
			if d.Decode(new(any)) != io.EOF {
				return errors.New("association requires one JSON array")
			}
		} else {
			var submission state.ExternalSubmission
			if err := d.Decode(&submission); err != nil {
				return err
			}
			if d.Decode(new(any)) != io.EOF {
				return errors.New("submission must contain one JSON object")
			}
			if err := submission.Validate(); err != nil {
				return err
			}
			a.Submission = &submission
		}
	}
	if command == "receipt" && ((a.Receipt == "") == (a.Key == "")) {
		return errors.New("receipt requires exactly one of --receipt or --key")
	}
	if command == "read" && (a.Memory == "" || a.Revision < 0) {
		return errors.New("read requires --memory and an optional positive revision")
	}
	data, err := externalMemoryCall(ctx, p.Home, a)
	if err != nil {
		return err
	}
	if command == "connection-add" {
		profiles[name] = p
		if err := saveExternalProfiles(profilesPath, profiles); err != nil {
			return fmt.Errorf("Host connection admitted but saving local alias failed: %w", err)
		}
	}
	if command == "connection-show" {
		data, err = json.Marshal(map[string]any{"binding": json.RawMessage(data), "checkpoint_save": p.CheckpointSave})
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}
