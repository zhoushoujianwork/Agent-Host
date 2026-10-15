package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agent-host/internal/harness"
)

var hostSkillName = regexp.MustCompile(`^procedure-[a-f0-9]{24}$`)
var skillDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (a *Adapter) supportsSkillPlugin(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, a.binary, "--help")
	cmd.WaitDelay = 2 * time.Second
	var output boundedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := bindProcessGroup(cmd); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		stopProcessGroup(cmd)
		return fmt.Errorf("Claude native skill capability probe: %w", err)
	}
	if !strings.Contains(output.String(), "--plugin-dir") {
		return errors.New("installed Claude does not advertise session-local --plugin-dir; native skill mounting is unavailable")
	}
	return nil
}

func validateSkillPlugin(root, expected string) (harness.SkillManifest, error) {
	var manifest harness.SkillManifest
	if !skillDigest.MatchString(expected) {
		return manifest, errors.New("native skill snapshot digest is invalid")
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != root {
		return manifest, errors.New("native skill package must have a canonical path without symlinks")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return manifest, errors.New("native skill package must be a private directory")
	}
	raw, err := readPrivateSkillFile(filepath.Join(root, "manifest.json"), 64<<10)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("native skill manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return manifest, errors.New("native skill manifest has trailing content")
	}
	digest, err := manifest.ContentDigest()
	if err != nil {
		return manifest, err
	}
	if manifest.Version != harness.SkillManifestVersion || manifest.Digest != expected || digest != expected || manifest.AgentID == "" || manifest.AudienceID == "" || manifest.SourceIdentity == "" || len(manifest.Scopes) == 0 || len(manifest.Sources) == 0 || len(manifest.Sources) > 8 {
		return manifest, errors.New("native skill manifest does not match the selected snapshot")
	}
	files := map[string]string{}
	allowed := map[string]bool{".claude-plugin/plugin.json": true}
	for _, source := range manifest.Sources {
		if !hostSkillName.MatchString(source.Name) || source.MemoryID == "" || source.Revision < 1 || (source.SourceRunID == "" && source.ExternalReceiptID == "") || (source.SourceRunID != "" && source.ExternalReceiptID != "") || source.Scope == "" || len(source.EvidenceIDs) == 0 {
			return manifest, errors.New("native skill source is incomplete")
		}
		path := "skills/" + source.Name + "/SKILL.md"
		if allowed[path] {
			return manifest, errors.New("native skill source is duplicated")
		}
		allowed[path] = true
	}
	for _, file := range manifest.Files {
		if !allowed[file.Path] || !skillDigest.MatchString(file.SHA256) || files[file.Path] != "" {
			return manifest, errors.New("native skill manifest contains an unexpected or duplicate file")
		}
		files[file.Path] = file.SHA256
	}
	if len(files) != len(allowed) {
		return manifest, errors.New("native skill manifest file set is incomplete")
	}
	seen, total := map[string]bool{}, 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("native skill package contains a symlink")
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0077 != 0 {
				return errors.New("native skill package directories must be private")
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "manifest.json" {
			return nil
		}
		expected, ok := files[relative]
		if !ok {
			return fmt.Errorf("native skill package contains unexpected file %s", relative)
		}
		body, err := readPrivateSkillFile(path, 16<<10)
		if err != nil {
			return err
		}
		total += len(body)
		if total > 65<<10 {
			return errors.New("native skill package exceeds its content budget")
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != expected {
			return fmt.Errorf("native skill package file %s changed", relative)
		}
		if relative == ".claude-plugin/plugin.json" {
			if !bytes.Equal(body, []byte("{\"name\":\"agent-host\",\"version\":\"1.0.0\",\"description\":\"Host-selected observed procedures\"}\n")) {
				return errors.New("native skill plugin must contain only the Host skill declaration")
			}
		} else if strings.Contains(string(body), "!`") {
			return errors.New("native skill contains executable dynamic context")
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return manifest, err
	}
	if len(seen) != len(files) {
		return manifest, errors.New("native skill package is incomplete")
	}
	return manifest, nil
}

func readPrivateSkillFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("native skill file %s must be private and regular", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("native skill file %s exceeds its size limit", path)
	}
	return body, nil
}
