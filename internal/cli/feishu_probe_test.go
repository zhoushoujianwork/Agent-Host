package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeishuProbeChecksAppWithoutNativeRuntimeOrHostState(t *testing.T) {
	for _, app := range []string{"cli_fixture", "cli_wrong"} {
		t.Run(app, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "meeting.json")
			writeBotJSON(t, config, meetingConfigFixture())
			bin := filepath.Join(root, "lark-cli")
			body := "#!/bin/sh\n[ \"$1\" = --profile ] && [ \"$2\" = fixture-meeting ] && [ \"$3\" = whoami ] && [ \"$4\" = --as ] && [ \"$5\" = bot ] || exit 91\nprintf '%s\\n' '{\"profile\":\"fixture-meeting\",\"appId\":\"" + app + "\",\"identity\":\"bot\",\"available\":true}'\n"
			if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			home := filepath.Join(root, "uncreated-home")
			var out, errout bytes.Buffer
			err := Run(context.Background(), []string{"feishu", "probe", "--config", config, "--home", home, "--workspace", root, "--claude", "missing-native", "--agents-config", filepath.Join(root, "missing-agents.json")}, nil, &out, &errout)
			if app == "cli_wrong" {
				if err == nil || out.Len() != 0 {
					t.Fatal("wrong application passed", err, out.String())
				}
			} else if err != nil || !strings.Contains(out.String(), `"status":"profile_identity_verified"`) || !strings.Contains(out.String(), `"voice_supported":false`) {
				t.Fatal(err, out.String())
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("probe created Host state")
			}
		})
	}
}
