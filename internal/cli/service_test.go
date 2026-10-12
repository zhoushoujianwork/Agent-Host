package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const serviceTestKey = "synthetic-service-provider-secret"

func newServiceFixture(t *testing.T) (serviceLayout, serviceConfig) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	layout := serviceLayout{Root: root, Config: filepath.Join(root, "config"), State: filepath.Join(root, "state"), Workspace: filepath.Join(root, "workspace"), Native: filepath.Join(root, "native")}
	for _, path := range []string{layout.Config, layout.State, layout.Workspace, layout.Native} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "fixture-claude")
	// Executable permission permits LookPath; actual execution must fail.
	if err := os.WriteFile(binary, []byte("not a native executable"), 0700); err != nil {
		t.Fatal(err)
	}
	config := serviceConfig{Claude: binary, Model: "fixture-model", JevProvider: "typesafe", JevModel: "jev-latest", Timeout: "10m", NoTools: true}
	writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
	writeBotJSON(t, filepath.Join(layout.Config, "environment.json"), map[string]string{"TYPESAFE_API_KEY": serviceTestKey, "ANTHROPIC_AUTH_TOKEN": serviceTestKey})
	secret := filepath.Join(layout.Config, "dingtalk.secret")
	if err := os.WriteFile(secret, []byte(cliBotSecret), 0600); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, filepath.Join(layout.Config, "dingtalk.json"), botConfigFile{ClientID: "fixture-app", RobotCode: "fixture-robot", ClientSecretFile: secret, CorpID: "fixture-corp", OwnerUserID: "fixture-owner"})
	return layout, config
}

func TestServiceCheckUsesInstalledConfigurationWithoutNetworkOrWriters(t *testing.T) {
	layout, _ := newServiceFixture(t)
	t.Setenv("TYPESAFE_API_KEY", "inherited-key-must-not-use")
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited/native")
	t.Setenv("AGENT_HOST_MODEL", "inherited-model")
	transport := &rejectingHTTPTransport{}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	var out, errout bytes.Buffer
	if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, &out, &errout); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"status": "local_configuration_valid", "model": "fixture-model", "runtime_root": layout.Root,
		"state_directory": layout.State, "workspace": layout.Workspace, "native_config_directory": layout.Native,
		"config_directory": layout.Config, "platform_verified": false, "native_auth_verified": false,
	} {
		if report[key] != want {
			t.Fatalf("report %s = %v, want %v", key, report[key], want)
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("local managed check attempted network access")
	}
	for _, path := range []string{layout.State, layout.Workspace, layout.Native} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("local check wrote to runtime directory %s: %v", filepath.Base(path), err)
		}
	}
	for _, secret := range []string{serviceTestKey, cliBotSecret, "inherited-key-must-not-use", "fixture-app", "fixture-corp", "fixture-owner"} {
		if strings.Contains(out.String()+errout.String(), secret) {
			t.Fatal("local check exposed credentials or platform identities")
		}
	}
	if os.Getenv("TYPESAFE_API_KEY") != "inherited-key-must-not-use" || os.Getenv("CLAUDE_CONFIG_DIR") != "/inherited/native" {
		t.Fatal("managed check did not restore its caller's environment")
	}
}

func TestServiceMissingConfiguredCredentialNeverUsesInheritedProviderKey(t *testing.T) {
	layout, _ := newServiceFixture(t)
	writeBotJSON(t, filepath.Join(layout.Config, "environment.json"), map[string]string{"ANTHROPIC_API_KEY": serviceTestKey})
	t.Setenv("TYPESAFE_API_KEY", "inherited-key-must-not-use")
	err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY is required") {
		t.Fatalf("managed runtime accepted an inherited provider credential: %v", err)
	}
	if os.Getenv("TYPESAFE_API_KEY") != "inherited-key-must-not-use" {
		t.Fatal("failure did not restore inherited environment")
	}
}

func TestServiceEnvironmentRejectsArbitraryOverridesAndPrivateValuesInErrors(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown key":   `{"HOME":"` + serviceTestKey + `"}`,
		"native path":   `{"CLAUDE_CONFIG_DIR":"` + serviceTestKey + `"}`,
		"non-string":    `{"TYPESAFE_API_KEY":{"secret":"` + serviceTestKey + `"}}`,
		"null value":    `{"TYPESAFE_API_KEY":null}`,
		"empty value":   `{"TYPESAFE_API_KEY":""}`,
		"whitespace":    `{"TYPESAFE_API_KEY":" secret "}`,
		"line break":    `{"TYPESAFE_API_KEY":"secret\nvalue"}`,
		"duplicate":     `{"TYPESAFE_API_KEY":"a","TYPESAFE_API_KEY":"` + serviceTestKey + `"}`,
		"trailing data": `{"TYPESAFE_API_KEY":"` + serviceTestKey + `"}{}`,
		"oversized":     `{"TYPESAFE_API_KEY":"` + strings.Repeat("s", 32*1024) + `"}`,
		"null object":   `null`,
	} {
		t.Run(name, func(t *testing.T) {
			layout, _ := newServiceFixture(t)
			if err := os.WriteFile(filepath.Join(layout.Config, "environment.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard)
			if err == nil || strings.Contains(err.Error(), serviceTestKey) {
				t.Fatalf("invalid environment accepted or disclosed: %v", err)
			}
		})
	}
}

func TestServiceNativeStreamRecoveryRequiresValidExplicitPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, fallback, limit string
		valid                 bool
	}{
		{"default", "", "", true},
		{"bounded recovery", "0", "8192", true},
		{"smaller recovery", "0", "1024", true},
		{"native default limit", "0", "", true},
		{"native large limit", "0", "64000", true},
		{"zero limit", "0", "0", false},
		{"noncanonical limit", "0", "08192", false},
		{"invalid fallback", "2", "8192", false},
		{"private value", "0", serviceTestKey, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, _ := newServiceFixture(t)
			environment := map[string]string{"TYPESAFE_API_KEY": serviceTestKey}
			if tc.fallback != "" {
				environment["CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"] = tc.fallback
			}
			if tc.limit != "" {
				environment["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = tc.limit
			}
			writeBotJSON(t, filepath.Join(layout.Config, "environment.json"), environment)
			err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), serviceTestKey) {
				t.Fatal("native stream configuration error disclosed a private value")
			}
		})
	}
}

func TestServiceConfigurationRequiresExplicitValidExecutionPolicy(t *testing.T) {
	mutations := map[string]func(*serviceConfig){
		"relative executable": func(c *serviceConfig) { c.Claude = "claude" },
		"missing executable":  func(c *serviceConfig) { c.Claude = "/absent/native-claude" },
		"empty model":         func(c *serviceConfig) { c.Model = "" },
		"option model":        func(c *serviceConfig) { c.Model = "--dangerous" },
		"empty Jev model":     func(c *serviceConfig) { c.JevModel = "" },
		"provider":            func(c *serviceConfig) { c.JevProvider = "auto" },
		"custom no endpoint":  func(c *serviceConfig) { c.JevProvider = "custom" },
		"endpoint official":   func(c *serviceConfig) { c.JevEndpoint = "https://gateway.example.test/v1" },
		"endpoint query": func(c *serviceConfig) {
			c.JevProvider, c.JevEndpoint = "custom", "https://gateway.example.test/v1?key=x"
		},
		"missing timeout":    func(c *serviceConfig) { c.Timeout = "" },
		"negative timeout":   func(c *serviceConfig) { c.Timeout = "-1m" },
		"conflicting tools":  func(c *serviceConfig) { c.AllowedTools = []string{"Read"} },
		"empty tool":         func(c *serviceConfig) { c.NoTools = false; c.AllowedTools = []string{""} },
		"option tool":        func(c *serviceConfig) { c.NoTools = false; c.AllowedTools = []string{"--dangerous"} },
		"padded option tool": func(c *serviceConfig) { c.NoTools = false; c.AllowedTools = []string{" --dangerous"} },
		"compound tool":      func(c *serviceConfig) { c.NoTools = false; c.AllowedTools = []string{"Read,Edit"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			layout, config := newServiceFixture(t)
			mutate(&config)
			writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
			if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err == nil {
				t.Fatal("accepted invalid managed execution policy")
			}
		})
	}
	for name, raw := range map[string]string{
		"unknown field": `{"claude":"/bin/sh","secret":"` + serviceTestKey + `"}`,
		"duplicate":     `{"model":"a","MODEL":"b"}`,
		"array":         `[]`,
		"null":          `null`,
	} {
		t.Run(name, func(t *testing.T) {
			layout, _ := newServiceFixture(t)
			if err := os.WriteFile(filepath.Join(layout.Config, "service.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard)
			if err == nil || strings.Contains(err.Error(), serviceTestKey) {
				t.Fatalf("invalid managed configuration accepted or disclosed: %v", err)
			}
		})
	}
}

func TestServicePrivatePathsRejectSymlinksPublicFilesAndSpecialFiles(t *testing.T) {
	for _, target := range []string{"root", "config", "state", "workspace", "native", "config/service.json", "config/environment.json", "config/dingtalk.json", "config/dingtalk.secret"} {
		for _, mutation := range []string{"public", "symlink", "wrong kind", "missing"} {
			t.Run(target+"/"+mutation, func(t *testing.T) {
				layout, _ := newServiceFixture(t)
				path := filepath.Join(layout.Root, target)
				if target == "root" {
					path = layout.Root
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "public":
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					moved := path + ".original"
					if err := os.Rename(path, moved); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(moved) })
					if err := os.Symlink(moved, path); err != nil {
						t.Fatal(err)
					}
				case "missing", "wrong kind":
					if err := os.RemoveAll(path); err != nil {
						t.Fatal(err)
					}
					if mutation == "wrong kind" {
						if info.IsDir() {
							err = os.WriteFile(path, []byte("fixture"), 0600)
						} else {
							err = syscall.Mkfifo(path, 0600)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, _, _, err := loadService(layout.Root); err == nil {
					t.Fatal("managed runtime accepted an unprotected or redirected path")
				}
			})
		}
	}
}

func TestServiceEnvironmentIsInheritedByNativeChildAndRestoredAfterFailure(t *testing.T) {
	native := filepath.Join(t.TempDir(), "managed-native")
	t.Setenv("HOME", "/fixture/unchanged-home")
	t.Setenv("CLAUDE_CONFIG_DIR", "/fixture/development-native")
	t.Setenv("TYPESAFE_API_KEY", "development-key")
	t.Setenv("JEV_API_KEY", "another-development-key")
	t.Setenv("ANTHROPIC_API_KEY", "development-native-key")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "development-oauth")
	t.Setenv("CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK", "0")
	t.Setenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS", "8192")
	failure := errors.New("fixture managed invocation failed")
	err := withServiceEnvironment(native, map[string]string{"TYPESAFE_API_KEY": serviceTestKey, "ANTHROPIC_AUTH_TOKEN": serviceTestKey}, func() error {
		cmd := exec.Command("/bin/sh", "-c", `test "$CLAUDE_CONFIG_DIR" = "$1" && test "$HOME" = /fixture/unchanged-home && test "$TYPESAFE_API_KEY" = "$2" && test "$ANTHROPIC_AUTH_TOKEN" = "$2" && test -z "${JEV_API_KEY+x}" && test -z "${ANTHROPIC_API_KEY+x}" && test -z "${CLAUDE_CODE_USE_BEDROCK+x}" && test -z "${CLAUDE_CODE_OAUTH_TOKEN+x}" && test -z "${CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK+x}" && test -z "${CLAUDE_CODE_MAX_OUTPUT_TOKENS+x}"`, "fixture", native, serviceTestKey)
		if err := cmd.Run(); err != nil {
			t.Fatalf("native child inherited incorrect managed environment: %v", err)
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("managed invocation failure was lost: %v", err)
	}
	for key, value := range map[string]string{"HOME": "/fixture/unchanged-home", "CLAUDE_CONFIG_DIR": "/fixture/development-native", "TYPESAFE_API_KEY": "development-key", "JEV_API_KEY": "another-development-key", "ANTHROPIC_API_KEY": "development-native-key", "CLAUDE_CODE_USE_BEDROCK": "1", "CLAUDE_CODE_OAUTH_TOKEN": "development-oauth", "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "0", "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "8192"} {
		if os.Getenv(key) != value {
			t.Fatalf("managed invocation did not restore %s", key)
		}
	}
}

func TestServiceDoesNotAcceptStateWorkspaceOrNativePathOverrides(t *testing.T) {
	layout, _ := newServiceFixture(t)
	for _, suffix := range [][]string{{"--home", t.TempDir()}, {"--workspace", t.TempDir()}, {"extra"}, {"--format", "text"}} {
		args := append([]string{"service", "check", "--root", layout.Root}, suffix...)
		if err := Run(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("managed command accepted path/format override: %v", suffix)
		}
	}
	for _, root := range []string{"", "relative-root", filepath.Join(layout.Root, "missing"), layout.Root + "/.."} {
		if err := Run(context.Background(), []string{"service", "check", "--root", root}, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("managed command accepted an invalid root")
		}
	}
}

func TestServiceCustomJevGatewayArguments(t *testing.T) {
	config := serviceConfig{JevProvider: "custom", JevEndpoint: "https://gateway.example.test/v1/systemone", JevModel: "jev-latest"}
	args := strings.Join(config.arguments(serviceLayout{Config: "/c", State: "/s", Workspace: "/w", Native: "/n"}, "serve"), " ")
	if !strings.Contains(args, "--jev-provider custom") || !strings.Contains(args, "--jev-endpoint https://gateway.example.test/v1/systemone") {
		t.Fatal(args)
	}
	config.JevProvider, config.JevEndpoint = "typesafe", ""
	if args := strings.Join(config.arguments(serviceLayout{}, "serve"), " "); strings.Contains(args, "--jev-endpoint") {
		t.Fatal(args)
	}
}
