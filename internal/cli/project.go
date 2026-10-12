package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-host/internal/state"
)

const projectUsage = `Local project registry (stop the service before add/update).
  agent-host project add --id ID --workspace PATH [--name NAME] [--home PATH]
  agent-host project update --id ID [--workspace PATH] [--name NAME] [--home PATH]
  agent-host project list [--home PATH]
  agent-host project show --id ID [--home PATH]
IDs and knowledge scopes are stable; update changes the working directory.
add/update accept --description TEXT and repeated --alias TEXT for routing.
Metadata-only updates retain the directory and memory scope.
`

func runProject(ctx context.Context, args []string, out, errout io.Writer) (resultErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, projectUsage)
		return err
	}
	command := args[0]
	if command != "add" && command != "update" && command != "list" && command != "show" {
		return errors.New("project requires add, update, list or show")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("project "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var home, id, name, workspace, description string
	var aliases []string
	fs.StringVar(&home, "home", filepath.Join(userHome, ".agent-host"), "Host state directory")
	fs.StringVar(&id, "id", "", "Stable project ID")
	fs.StringVar(&name, "name", "", "Display name")
	fs.StringVar(&workspace, "workspace", "", "Existing project directory")
	fs.StringVar(&description, "description", "", "Short project description for routing")
	fs.Func("alias", "Project routing alias; repeat to replace aliases", func(value string) error { aliases = append(aliases, strings.TrimSpace(value)); return nil })
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("project commands accept options only")
	}
	if command != "list" && id == "" {
		return errors.New("provide --id")
	}
	write := command == "add" || command == "update"
	if command == "add" && workspace == "" {
		return errors.New("provide --workspace")
	}
	metadata := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { metadata[f.Name] = true })
	if !write && (workspace != "" || name != "" || metadata["alias"] || metadata["description"] || command == "list" && id != "") {
		return errors.New("unsupported project inspection options")
	}
	if len(name) > 256 || len(description) > 4096 || len(aliases) > 32 {
		return errors.New("project metadata exceeds its limits")
	}
	seen := map[string]bool{}
	for _, alias := range aliases {
		if alias == "" || len(alias) > 256 || seen[alias] {
			return errors.New("project aliases must be nonempty, unique and at most 256 bytes")
		}
		seen[alias] = true
	}
	if write {
		release, err := state.Acquire(home)
		if err != nil {
			return fmt.Errorf("stop the service before changing projects: %w", err)
		}
		defer func() { resultErr = errors.Join(resultErr, release()) }()
	}
	store, err := state.Open(home, !write)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if command == "list" {
		projects, err := store.Projects(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(projects)
	}
	var project state.Project
	switch command {
	case "add":
		project, err = store.RegisterProject(ctx, id, name, workspace)
	case "update":
		project, err = store.Project(ctx, id)
		if err == nil && workspace != "" {
			project, err = store.UpdateProject(ctx, id, name, workspace)
		}
	case "show":
		project, err = store.Project(ctx, id)
	}
	if err != nil {
		return err
	}
	if write && (metadata["description"] || metadata["alias"] || name != "") {
		if !metadata["description"] {
			description = project.Description
		}
		if !metadata["alias"] {
			aliases = project.Aliases
		}
		project, err = store.UpdateProjectMetadata(ctx, id, name, description, aliases)
		if err != nil {
			return err
		}
	}
	return encoder.Encode(project)
}
