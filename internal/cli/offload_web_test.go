package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The Web bridge executes this real CLI entry inside the test binary. Only the
// native executable is fake; signal handling matches cmd/agent-host.
func TestOffloadWebProcess(t *testing.T) {
	if os.Getenv("AGH_WEB_TEST_PROCESS") != "1" {
		t.Skip("subprocess helper")
	}
	index := 0
	for index < len(os.Args) && os.Args[index] != "--" {
		index++
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := Run(ctx, os.Args[index+1:], nil, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestOffloadWebBridgeActualCLIAndFakeNative(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python bridge unavailable")
	}
	o, _ := chatOffloadFixture(t)
	dir := filepath.Dir(o.offloadConfig)
	script := offloadFakeScript(dir)
	script = strings.Replace(script, `session="native-$n"`, `case "$(cat "$dir/stdin.$n")" in *slow-room*) exec sleep 60;; esac
session="native-$n"`, 1)
	if err = os.WriteFile(o.offloadProfile.Command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "agh-test")
	quoted := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "'"
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quoted+" -test.run=^TestOffloadWebProcess$ -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "web-chat.json")
	writeBotJSON(t, config, map[string]any{"binary": wrapper, "cwd": o.workspace, "args": []string{"--home", o.home, "--workspace", o.workspace, "--agent", "default", "--offload-config", o.offloadConfig}})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	viewer := filepath.Clean(filepath.Join(cwd, "..", "..", "tools", "run-viewer"))
	code := `
import sys,time,pathlib
sys.path.insert(0,sys.argv[1])
from chat_runtime import ChatRuntime
config=pathlib.Path(sys.argv[2]); directory=config.parent
runtime=ChatRuntime(config,room_file=directory/'web-room.json')
def wait(turn_id):
 for _ in range(600):
  turn=next(t for t in runtime.snapshot()['turns'] if t['id']==turn_id)
  if turn['status'] not in ('starting','running','stopping'):return turn
  time.sleep(.02)
 raise AssertionError('turn did not finish')
try:
 first=runtime.start('request-one','first')
 a=wait(first)
 assert runtime.snapshot()['max_concurrent']==1
 assert a['status']=='completed',a
 assert a['text']=='answer-1',a
 assert a['engine']=='offload' and a['memory_status']=='skipped',a
 second=runtime.start('request-two','second')
 b=wait(second)
 assert b['status']=='completed' and b['text']=='answer-2',b
 assert runtime.start('request-two','second')==second
 assert (directory/'count').read_text().strip()=='2'
 runtime.close()
 runtime=ChatRuntime(config,room_file=directory/'web-room.json')
 assert len(runtime.snapshot()['turns'])==2
 assert runtime.start('request-two','second')==second
 assert (directory/'count').read_text().strip()=='2'
 slow=runtime.start('request-slow','slow-room')
 for _ in range(300):
  if (directory/'argv.3').exists():break
  time.sleep(.02)
 runtime.stop(slow)
 canceled=wait(slow)
 assert canceled['status']=='canceled',canceled
 assert canceled['execution_status']=='canceled',canceled
finally:
 runtime.close()
`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-B", "-c", code, viewer, config)
	command.Env = append(os.Environ(), "AGH_WEB_TEST_PROCESS=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Web/CLI integration: %v\n%s", err, output)
	}
}
