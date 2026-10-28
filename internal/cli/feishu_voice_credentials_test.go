package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	credASR   = "FIXTURE_VOICE_ASR_KEY"
	credApp   = "FIXTURE_VOICE_TTS_APP"
	credToken = "FIXTURE_VOICE_TTS_TOKEN"
)

// credentialsFixture writes a Feishu config in dir that references file.
func credentialsFixture(t *testing.T, dir, file string) string {
	t.Helper()
	cfg := meetingConfigFixture()
	voice := voiceTestConfig().Voice
	voice.CredentialsFile = file
	cfg.Voice = &voice
	path := filepath.Join(dir, "channel-feishu-meeting.json")
	writeBotJSON(t, path, cfg)
	return path
}

func writeCredentials(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

const validCredentials = `{"FIXTURE_VOICE_ASR_KEY":"file-asr-secret","FIXTURE_VOICE_TTS_APP":"file-app-secret","FIXTURE_VOICE_TTS_TOKEN":"file-token-secret"}`

func TestFeishuVoiceCredentialsFileResolvesRelativeToConfigNotCwd(t *testing.T) {
	dir := t.TempDir()
	writeCredentials(t, filepath.Join(dir, "voice-credentials.json"), validCredentials)
	path := credentialsFixture(t, dir, "voice-credentials.json")
	t.Chdir(t.TempDir()) // an unrelated cwd must not matter
	loaded, err := loadFeishuMeetingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "voice-credentials.json"); loaded.Voice.CredentialsFile != want {
		t.Fatalf("credentials_file resolved to %q, want %q", loaded.Voice.CredentialsFile, want)
	}
	// The same config loaded through a relative config path agrees.
	t.Chdir(filepath.Dir(dir))
	relative, err := loadFeishuMeetingConfig(filepath.Join(filepath.Base(dir), filepath.Base(path)))
	if err != nil || relative.Voice.CredentialsFile != loaded.Voice.CredentialsFile {
		t.Fatalf("relative config path disagrees: %v %q", err, relative.Voice.CredentialsFile)
	}
	asr, tts, err := feishuVoiceProviders(loaded, func(string) (string, bool) { t.Fatal("inherited environment consulted"); return "", false })
	if err != nil || asr == nil || tts == nil {
		t.Fatal("valid credentials file was rejected", err)
	}
}

func TestFeishuVoiceCredentialsFileIsExclusiveAndNotExported(t *testing.T) {
	setVoiceEnv(t) // a complete inherited environment must not rescue a bad file
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	writeCredentials(t, file, `{"FIXTURE_VOICE_ASR_KEY":"file-asr-secret"}`)
	cfg := voiceTestConfig()
	cfg.Voice.CredentialsFile = file
	_, _, err := feishuVoiceProviders(cfg, os.LookupEnv)
	if err == nil || !strings.Contains(err.Error(), credApp) || !strings.Contains(err.Error(), credToken) {
		t.Fatalf("missing required keys must be reported by name without env fallback: %v", err)
	}
	writeCredentials(t, file, validCredentials)
	t.Setenv(credASR, "")
	t.Setenv(credApp, "")
	t.Setenv(credToken, "")
	if _, _, err := feishuVoiceProviders(cfg, os.LookupEnv); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{credASR, credApp, credToken} {
		if v, _ := os.LookupEnv(name); v != "" {
			t.Fatal("credential value was exported to the process environment")
		}
	}
	// Absent field keeps the env-only behavior.
	cfg.Voice.CredentialsFile = ""
	setVoiceEnv(t)
	if _, _, err := feishuVoiceProviders(cfg, os.LookupEnv); err != nil {
		t.Fatal(err)
	}
}

func TestFeishuVoiceCredentialsFileRejectsInvalidContentWithoutLeaking(t *testing.T) {
	const leak = "leaky-secret-VALUE"
	const unknownKey = "SECRET_LOOKING_UNKNOWN_KEY"
	for name, body := range map[string]string{
		"unknown key":      `{"FIXTURE_VOICE_ASR_KEY":"` + leak + `","FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b","` + unknownKey + `":"x"}`,
		"duplicate key":    `{"FIXTURE_VOICE_ASR_KEY":"` + leak + `","FIXTURE_VOICE_ASR_KEY":"b","FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"null value":       `{"FIXTURE_VOICE_ASR_KEY":null,"FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"non-string":       `{"FIXTURE_VOICE_ASR_KEY":123456789,"FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"empty":            `{"FIXTURE_VOICE_ASR_KEY":"","FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"multiline":        `{"FIXTURE_VOICE_ASR_KEY":"` + leak + `\nsecond","FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"padded":           `{"FIXTURE_VOICE_ASR_KEY":" ` + leak + `","FIXTURE_VOICE_TTS_APP":"a","FIXTURE_VOICE_TTS_TOKEN":"b"}`,
		"not an object":    `["` + leak + `"]`,
		"null document":    `null`,
		"trailing content": validCredentials + ` ` + validCredentials,
	} {
		t.Run(name, func(t *testing.T) {
			setVoiceEnv(t)
			file := filepath.Join(t.TempDir(), "c.json")
			writeCredentials(t, file, body)
			cfg := voiceTestConfig()
			cfg.Voice.CredentialsFile = file
			asr, tts, err := feishuVoiceProviders(cfg, os.LookupEnv)
			if err == nil || asr != nil || tts != nil {
				t.Fatal("invalid credentials file was accepted")
			}
			if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), unknownKey) {
				t.Fatalf("error echoed file content: %v", err)
			}
		})
	}
}

func TestFeishuVoiceCredentialsFileRequiresPrivateRegularFile(t *testing.T) {
	setVoiceEnv(t)
	dir := t.TempDir()
	cfg := voiceTestConfig()
	file := filepath.Join(dir, "c.json")
	writeCredentials(t, file, validCredentials)
	if err := os.Chmod(file, 0640); err != nil {
		t.Fatal(err)
	}
	cfg.Voice.CredentialsFile = file
	if _, _, err := feishuVoiceProviders(cfg, os.LookupEnv); err == nil {
		t.Fatal("group-readable credentials file was accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	cfg.Voice.CredentialsFile = link
	if _, _, err := feishuVoiceProviders(cfg, os.LookupEnv); err == nil {
		t.Fatal("symlinked credentials file was accepted")
	}
	cfg.Voice.CredentialsFile = filepath.Join(dir, "missing.json")
	if _, _, err := feishuVoiceProviders(cfg, os.LookupEnv); err == nil {
		t.Fatal("missing credentials file fell back to the environment")
	}
}

func TestFeishuVoiceDisabledNeverReadsCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	cfg := meetingConfigFixture()
	voice := voiceTestConfig().Voice
	voice.Enabled = false
	voice.CredentialsFile = "does-not-exist.json"
	cfg.Voice = &voice
	path := filepath.Join(dir, "channel-feishu-meeting.json")
	writeBotJSON(t, path, cfg)
	loaded, err := loadFeishuMeetingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	asr, tts, err := feishuVoiceProviders(loaded, func(string) (string, bool) { t.Fatal("credentials read"); return "", false })
	if asr != nil || tts != nil || err != nil {
		t.Fatal("disabled voice resolved providers", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "does-not-exist.json")); err == nil {
		t.Fatal("disabled voice created a credentials file")
	}
}

func TestFeishuVoiceConfigRejectsMultilineCredentialsPath(t *testing.T) {
	v := voiceTestConfig().Voice
	v.CredentialsFile = "a\nb"
	if v.Validate() == nil {
		t.Fatal("multiline credentials_file path was accepted")
	}
}
