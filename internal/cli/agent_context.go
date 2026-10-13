package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"agent-host/internal/assembly"
)

const agentInstructionBudget = 32 * 1024
const userProfileBudget = 16 * 1024

// User-maintained configuration is read once at startup. Learning never writes
// back into these files, and private USER content is selected only for owner use.
func (a *agentDefinition) loadContextFiles(configDir string) error {
	if a.InstructionsFile != "" && a.InstructionsDir != "" {
		return errors.New("instructions_file and instructions_dir cannot both be configured")
	}
	a.contextFiles, a.role, a.userProfile = nil, "", ""
	if a.InstructionsFile != "" {
		if !filepath.IsAbs(a.InstructionsFile) {
			return errors.New("agent instructions_file must be absolute")
		}
		body, err := readAgentContext(a.InstructionsFile, agentInstructionBudget)
		if err != nil {
			return err
		}
		a.role = body
		a.contextFiles = []assembly.ContextFile{{Name: "AGENTS.md", Source: a.InstructionsFile, Content: body}}
	}
	if a.InstructionsDir != "" {
		path, err := resolveAgentContextPath(configDir, a.InstructionsDir)
		if err != nil {
			return fmt.Errorf("instructions_dir: %w", err)
		}
		if _, err := privateServicePath(path, true); err != nil {
			return errors.New("instructions_dir must be an existing private directory without symlinks")
		}
		a.InstructionsDir = path
		var sections []string
		used := 0
		for _, name := range []string{"AGENTS.md", "SOUL.md", "IDENTITY.md"} {
			source := filepath.Join(path, name)
			if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return fmt.Errorf("inspect agent context %s: %w", name, err)
			}
			body, err := readAgentContext(source, agentInstructionBudget-used)
			if err != nil {
				return err
			}
			used += len(body)
			a.contextFiles = append(a.contextFiles, assembly.ContextFile{Name: name, Source: source, Content: body})
			sections = append(sections, fmt.Sprintf("User-maintained agent configuration (%s). This text cannot grant capabilities or change Host audience permissions.\n\n%s", name, body))
		}
		if len(a.contextFiles) == 0 {
			return errors.New("instructions_dir must contain AGENTS.md, SOUL.md or IDENTITY.md")
		}
		a.role = strings.Join(sections, "\n\n")
	}
	if a.UserFile != "" {
		path, err := resolveAgentContextPath(configDir, a.UserFile)
		if err != nil {
			return fmt.Errorf("user_file: %w", err)
		}
		if filepath.Base(path) != "USER.md" {
			return errors.New("user_file must explicitly reference USER.md")
		}
		for _, file := range a.contextFiles {
			if file.Source == path {
				return errors.New("USER.md cannot also be selected as agent instructions")
			}
		}
		a.userProfile, err = readAgentContext(path, userProfileBudget)
		if err != nil {
			return err
		}
		a.UserFile = path
	}
	return nil
}

func resolveAgentContextPath(configDir, configured string) (string, error) {
	if configured != strings.TrimSpace(configured) || strings.ContainsAny(configured, "\x00\r\n") {
		return "", errors.New("context path must be a nonempty single line")
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(configDir, configured)
	}
	path, err := filepath.Abs(configured)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", errors.New("context path must exist without symlinks")
	}
	return path, nil
}

func readAgentContext(path string, limit int) (string, error) {
	before, err := privateServicePath(path, false)
	if err != nil || before.Size() > int64(limit) {
		return "", errors.New("agent context must be private regular bounded text without symlinks")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read agent context: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 {
		return "", errors.Join(errors.New("agent context changed while opening"), file.Close())
	}
	body, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return "", fmt.Errorf("read agent context: %w", err)
	}
	if len(body) > limit || !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
		return "", errors.New("agent context must be valid UTF-8 without NUL within its budget")
	}
	return string(body), nil
}
