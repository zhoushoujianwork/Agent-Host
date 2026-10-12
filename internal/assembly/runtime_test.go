package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeKeepsScopeIndependentOfDirectory(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	a, _ = filepath.EvalSymlinks(a)
	b, _ = filepath.EvalSymlinks(b)
	c := Configuration{Binary: "/native/claude", Model: "sonnet", NativeProfile: "/native/profile", AllowedTools: []string{"Read"}}
	r, err := Assemble("api", "API", "project:api", a, c)
	if err != nil || r.Scope != "project:api" || r.Workspace != a || r.Fingerprint == "" {
		t.Fatalf("%+v %v", r, err)
	}
	moved, err := Assemble("api", "API", "project:api", b, c)
	if err != nil || moved.Scope != r.Scope || moved.Workspace == r.Workspace || moved.Fingerprint == r.Fingerprint {
		t.Fatalf("%+v %v", moved, err)
	}
	c.AllowedTools = []string{"Read", "Edit"}
	changed, err := Assemble("api", "API", "project:api", a, c)
	if err != nil || changed.Fingerprint == r.Fingerprint {
		t.Fatalf("configuration did not rotate: %+v %v", changed, err)
	}
	if _, err = Assemble("api", "API", "project:api", filepath.Join(a, "missing"), c); err == nil {
		t.Fatal("accepted missing cwd")
	}
	file := filepath.Join(a, "file")
	if err = os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Assemble("api", "API", "project:api", file, c); err == nil {
		t.Fatal("accepted file cwd")
	}
}

func TestNativeSettingsRotateRuntimeAndAuthenticationDoesNot(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), "native")
	c := Configuration{Binary: "claude", Model: "sonnet", NativeProfile: profile}
	assemble := func() Runtime {
		t.Helper()
		r, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	absent := assemble()
	if repeated := assemble(); repeated.Fingerprint != absent.Fingerprint {
		t.Fatal("missing settings changed the runtime fingerprint")
	}
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	// Native startup may create its profile, credentials and sessions. They do
	// not change shared execution policy and must not cause a second fresh run.
	for _, name := range []string{".credentials.json", ".claude.json", "session.jsonl"} {
		if err := os.WriteFile(filepath.Join(profile, name), []byte("authentication/session data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if created := assemble(); created.Fingerprint != absent.Fingerprint {
		t.Fatal("profile/authentication creation rotated the runtime")
	}
	settings := filepath.Join(profile, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["Read"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	configured := assemble()
	if configured.Fingerprint == absent.Fingerprint {
		t.Fatal("new native settings did not rotate the runtime")
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["Read","Edit"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	changed := assemble()
	if changed.Fingerprint == configured.Fingerprint {
		t.Fatal("native permission change did not rotate the runtime")
	}
	for _, name := range []string{".credentials.json", ".claude.json", "session.jsonl"} {
		if err := os.WriteFile(filepath.Join(profile, name), []byte("updated authentication/session data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if authenticated := assemble(); authenticated.Fingerprint != changed.Fingerprint {
		t.Fatal("authentication/session change rotated the runtime")
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if removed := assemble(); removed.Fingerprint != absent.Fingerprint {
		t.Fatal("removing native settings did not restore the absent configuration")
	}
	if err := os.WriteFile(settings, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if empty := assemble(); empty.Fingerprint == absent.Fingerprint {
		t.Fatal("empty native settings were confused with an absent file")
	}
}

func TestNativeProfileResolutionMatchesEffectiveDirectory(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := Configuration{Binary: "claude", Model: "sonnet"}
	defaultRuntime, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil {
		t.Fatal(err)
	}
	c.NativeProfile = filepath.Join(home, ".claude")
	explicit, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil || explicit.Fingerprint != defaultRuntime.Fingerprint {
		t.Fatalf("default profile did not resolve consistently: %+v %v", explicit, err)
	}
	profile := filepath.Join(workspace, "native")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"disableAllHooks":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.NativeProfile = "native"
	relative, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil {
		t.Fatal(err)
	}
	c.NativeProfile = profile
	absolute, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil || absolute.Fingerprint != relative.Fingerprint {
		t.Fatalf("relative profile did not follow native cwd: %+v %v", absolute, err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(profile, alias); err != nil {
		t.Fatal(err)
	}
	c.NativeProfile = alias
	symlink, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil || symlink.Fingerprint != absolute.Fingerprint {
		t.Fatalf("profile alias changed identity: %+v %v", symlink, err)
	}
	c.NativeProfile = filepath.Join(workspace, "different-native")
	other, err := Assemble("alpha", "Alpha", "project:alpha", workspace, c)
	if err != nil || other.Fingerprint == absolute.Fingerprint {
		t.Fatalf("different profile did not rotate runtime: %+v %v", other, err)
	}
}

func TestNativeSettingsFailuresAreExplicit(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory", "broken symlink", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			profile := t.TempDir()
			settings := filepath.Join(profile, "settings.json")
			switch kind {
			case "directory":
				if err := os.Mkdir(settings, 0700); err != nil {
					t.Fatal(err)
				}
			case "broken symlink":
				if err := os.Symlink(filepath.Join(profile, "missing"), settings); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root can read mode-000 files")
				}
				if err := os.WriteFile(settings, []byte(`{}`), 0000); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Assemble("alpha", "Alpha", "project:alpha", workspace, Configuration{NativeProfile: profile})
			if err == nil || !strings.Contains(err.Error(), "native settings") {
				t.Fatalf("invalid settings %q accepted: %v", kind, err)
			}
		})
	}
	profileFile := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(profileFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Assemble("alpha", "Alpha", "project:alpha", workspace, Configuration{NativeProfile: profileFile}); err == nil {
		t.Fatal("regular file accepted as native profile")
	}
}
