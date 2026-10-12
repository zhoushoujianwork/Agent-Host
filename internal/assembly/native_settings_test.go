package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/state"
)

func nativeSettingsTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeNativeSettingsTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadedProjectSettingsRotatePrimaryAndNeutralRuntime(t *testing.T) {
	for _, primary := range []string{"a", ""} {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			t.Run(primary+"/"+name, func(t *testing.T) {
				neutral, a, b := nativeSettingsTestDir(t), nativeSettingsTestDir(t), nativeSettingsTestDir(t)
				projects := []state.ProjectRef{{ID: "a", Scope: "a", Workspace: a}, {ID: "b", Scope: "b", Workspace: b}}
				c := Configuration{NativeProfile: filepath.Join(neutral, "profile")}
				assemble := func() Runtime {
					t.Helper()
					r, err := AssembleProjects(projects, primary, "default", neutral, c)
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
				absent := assemble()
				settings := filepath.Join(absent.Workspace, ".claude", name)
				writeNativeSettingsTestFile(t, settings, `{"permissions":{"allow":["Read"]}}`)
				present := assemble()
				if present.Fingerprint == absent.Fingerprint {
					t.Fatal("creating loaded project settings did not rotate runtime")
				}
				writeNativeSettingsTestFile(t, settings, `{"permissions":{"allow":["Read","Edit"]}}`)
				changed := assemble()
				if changed.Fingerprint == present.Fingerprint {
					t.Fatal("loaded project permission change did not rotate runtime")
				}
				for _, name := range []string{"source.go", "README.md", "CLAUDE.md", "AGENTS.md", ".claude/history.jsonl"} {
					writeNativeSettingsTestFile(t, filepath.Join(changed.Workspace, name), "unrelated text")
				}
				// --add-dir loads instructions, not another project's permission settings.
				writeNativeSettingsTestFile(t, filepath.Join(b, ".claude", "settings.json"), `{"permissions":{"deny":["Edit"]}}`)
				writeNativeSettingsTestFile(t, filepath.Join(b, ".claude", "settings.local.json"), `{"permissions":{"deny":["Bash"]}}`)
				projects[0], projects[1] = projects[1], projects[0]
				if unrelated := assemble(); unrelated.Fingerprint != changed.Fingerprint {
					t.Fatal("unloaded settings, source text, or project order rotated runtime")
				}
				if err := os.Remove(settings); err != nil {
					t.Fatal(err)
				}
				if removed := assemble(); removed.Fingerprint != absent.Fingerprint {
					t.Fatal("removing loaded settings did not restore absent configuration")
				}
			})
		}
	}
}

func TestNativeGitRootLocalSettingsParticipate(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "nested cwd"
		if linked {
			name = "linked worktree"
		}
		t.Run(name, func(t *testing.T) {
			root := nativeSettingsTestDir(t)
			workspace := filepath.Join(root, "nested", "app")
			if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			if linked {
				worktree := nativeSettingsTestDir(t)
				workspace = filepath.Join(worktree, "nested")
				gitdir := filepath.Join(root, ".git", "worktrees", "feature")
				writeNativeSettingsTestFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitdir+"\n")
				writeNativeSettingsTestFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
				writeNativeSettingsTestFile(t, filepath.Join(gitdir, "gitdir"), filepath.Join(worktree, ".git")+"\n")
			}
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			c := Configuration{NativeProfile: filepath.Join(root, "profile")}
			assemble := func() Runtime {
				t.Helper()
				r, err := Assemble("a", "A", "scope:a", workspace, c)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			absent := assemble()
			rootLocal := filepath.Join(root, ".claude", "settings.local.json")
			writeNativeSettingsTestFile(t, rootLocal, `{"permissions":{"allow":["Read"]}}`)
			present := assemble()
			if present.Fingerprint == absent.Fingerprint {
				t.Fatal("creating canonical Git-root local settings did not rotate runtime")
			}
			writeNativeSettingsTestFile(t, rootLocal, `{"permissions":{"deny":["Read"]}}`)
			changed := assemble()
			if changed.Fingerprint == present.Fingerprint {
				t.Fatal("canonical Git-root permission change did not rotate runtime")
			}
			writeNativeSettingsTestFile(t, filepath.Join(root, ".claude", "settings.json"), `{"permissions":{"allow":["Bash"]}}`)
			if ignored := assemble(); ignored.Fingerprint != changed.Fingerprint {
				t.Fatal("unloaded ancestor project settings rotated runtime")
			}
			cwdLocal := filepath.Join(workspace, ".claude", "settings.local.json")
			writeNativeSettingsTestFile(t, cwdLocal, `{"permissions":{"deny":["Edit"]}}`)
			if legacy := assemble(); legacy.Fingerprint == changed.Fingerprint {
				t.Fatal("cwd local settings merged by native were omitted")
			}
			for _, path := range []string{cwdLocal, rootLocal} {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if removed := assemble(); removed.Fingerprint != absent.Fingerprint {
				t.Fatal("removing local settings did not restore absent configuration")
			}
		})
	}
}

func TestNativeGitRootSettingsRespectHomeAndSubmoduleBoundaries(t *testing.T) {
	root := nativeSettingsTestDir(t)
	workspace := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	got, err := nativeLocalSettingsRoot(workspace)
	if err != nil || got != workspace {
		t.Fatalf("home Git root must fall back to cwd: %s %v", got, err)
	}
	t.Setenv("HOME", nativeSettingsTestDir(t))
	gitdir := filepath.Join(root, ".git", "modules", "app")
	if err := os.MkdirAll(gitdir, 0700); err != nil {
		t.Fatal(err)
	}
	writeNativeSettingsTestFile(t, filepath.Join(workspace, ".git"), "gitdir: "+gitdir)
	got, err = nativeLocalSettingsRoot(workspace)
	if err != nil || got != workspace {
		t.Fatalf("submodule must keep its own root: %s %v", got, err)
	}
}

func TestLoadedSettingsFailuresAreExplicitAcrossProjectCombinations(t *testing.T) {
	for _, primary := range []string{"a", ""} {
		for _, source := range []string{"project", "cwd local", "Git-root local"} {
			for _, kind := range []string{"directory", "broken symlink", "broken parent", "unreadable"} {
				t.Run(primary+"/"+source+"/"+kind, func(t *testing.T) {
					if kind == "unreadable" && os.Geteuid() == 0 {
						t.Skip("root can read mode-000 files")
					}
					root, b := nativeSettingsTestDir(t), nativeSettingsTestDir(t)
					workspace := filepath.Join(root, "cwd")
					for _, path := range []string{workspace, filepath.Join(root, ".git")} {
						if err := os.MkdirAll(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					settings := filepath.Join(workspace, ".claude", "settings.json")
					if source == "cwd local" {
						settings = filepath.Join(workspace, ".claude", "settings.local.json")
					} else if source == "Git-root local" {
						settings = filepath.Join(root, ".claude", "settings.local.json")
					}
					if kind == "broken parent" {
						if err := os.Symlink(filepath.Join(root, "missing"), filepath.Dir(settings)); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
							t.Fatal(err)
						}
						switch kind {
						case "directory":
							if err := os.Mkdir(settings, 0700); err != nil {
								t.Fatal(err)
							}
						case "broken symlink":
							if err := os.Symlink(filepath.Join(root, "missing"), settings); err != nil {
								t.Fatal(err)
							}
						case "unreadable":
							if err := os.WriteFile(settings, []byte(`{}`), 0000); err != nil {
								t.Fatal(err)
							}
						}
					}
					projects := []state.ProjectRef{{ID: "a", Scope: "a", Workspace: workspace}, {ID: "b", Scope: "b", Workspace: b}}
					_, err := AssembleProjects(projects, primary, "default", workspace, Configuration{NativeProfile: filepath.Join(root, "profile")})
					if err == nil || !strings.Contains(err.Error(), "native settings") {
						t.Fatalf("invalid loaded settings accepted: %v", err)
					}
				})
			}
		}
	}
}

func TestNativeGitMetadataErrorsDoNotBecomeAbsentSettings(t *testing.T) {
	for _, kind := range []string{"broken link", "unreadable", "network", "network normalized", "network link", "invalid commondir"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read mode-000 files")
			}
			root := nativeSettingsTestDir(t)
			entry := filepath.Join(root, ".git")
			switch kind {
			case "broken link":
				if err := os.Symlink(filepath.Join(root, "missing"), entry); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.WriteFile(entry, []byte("gitdir: missing"), 0000); err != nil {
					t.Fatal(err)
				}
			case "network":
				writeNativeSettingsTestFile(t, entry, "gitdir: //unavailable.invalid/repository")
			case "network normalized":
				writeNativeSettingsTestFile(t, entry, "gitdir: /unused/../NET/unavailable.invalid/repository")
			case "network link":
				if err := os.Symlink("/net/unavailable.invalid/repository", entry); err != nil {
					t.Fatal(err)
				}
			case "invalid commondir":
				gitdir := filepath.Join(root, "metadata")
				if err := os.MkdirAll(filepath.Join(gitdir, "commondir"), 0700); err != nil {
					t.Fatal(err)
				}
				writeNativeSettingsTestFile(t, entry, "gitdir: "+gitdir)
			}
			if _, err := Assemble("a", "A", "a", root, Configuration{NativeProfile: filepath.Join(root, "profile")}); err == nil {
				t.Fatal("invalid Git metadata silently became absent settings")
			}
		})
	}
}

func TestNativeMetadataResolutionCleansInputBeforeFollowingLinkTargets(t *testing.T) {
	root := nativeSettingsTestDir(t)
	for _, path := range []string{filepath.Join(root, "local"), filepath.Join(root, "other", "nested")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeNativeSettingsTestFile(t, filepath.Join(root, "local", "entry"), "lexical entry")
	writeNativeSettingsTestFile(t, filepath.Join(root, "other", "entry"), "realpath entry")
	if err := os.Symlink(filepath.Join(root, "other", "nested"), filepath.Join(root, "local", "inner")); err != nil {
		t.Fatal(err)
	}
	// Explicit metadata is lexically resolved before realpath, matching native's
	// path.resolve(base, ref), even when that removes a link/.. pair.
	input := filepath.Join(root, "local") + "/inner/../entry"
	got, err := resolveNativeLocalPath(input)
	if err != nil || got != filepath.Join(root, "local", "entry") {
		t.Fatalf("metadata input was not cleaned before realpath: %s %v", got, err)
	}
	// Dot segments hidden inside a symlink target have filesystem semantics:
	// follow inner first, then its parent, rather than cleaning away inner.
	link := filepath.Join(root, "outer")
	if err := os.Symlink("local/inner/../entry", link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	got, err = resolveNativeLocalPath(link)
	if err != nil || got != want || got != filepath.Join(root, "other", "entry") {
		t.Fatalf("link target did not preserve realpath semantics: %s want %s: %v", got, want, err)
	}
}
