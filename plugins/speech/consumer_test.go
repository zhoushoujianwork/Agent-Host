package speech_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIndependentConsumer(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mod := `module example.invalid/speech-consumer

go 1.23.0

require github.com/zhoushoujianwork/Agent-Host/plugins/speech v0.0.0
replace github.com/zhoushoujianwork/Agent-Host/plugins/speech => ` + strconv.Quote(filepath.ToSlash(root)) + "\n"
	source := `package consumer
import (
 "context"
 "testing"
 "github.com/zhoushoujianwork/Agent-Host/plugins/speech"
 "github.com/zhoushoujianwork/Agent-Host/plugins/speech/doubao"
)
var _ speech.ASRProvider = doubao.ASRProvider{}
var _ speech.TTSProvider = doubao.TTSProvider{}
func TestExplicitConfig(t *testing.T) {
 s,err:=(doubao.ASRProvider{}).Open(context.Background(),speech.ASRConfig{})
 if s!=nil || err==nil { t.Fatal("missing explicit config must be rejected") }
 a,err:=(doubao.TTSProvider{}).Synthesize(context.Background(),speech.TTSRequest{})
 if a!=nil || err==nil { t.Fatal("missing explicit config must be rejected") }
}
`
	for name, body := range map[string]string{"go.mod": mod, "consumer_test.go": source} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOPROXY=") && !strings.HasPrefix(entry, "GOSUMDB=") && !strings.HasPrefix(entry, "GOWORK=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GOPROXY=off", "GOSUMDB=off", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("independent consumer: %v\n%s", err, out)
	}
}
