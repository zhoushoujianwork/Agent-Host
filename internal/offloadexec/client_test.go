//go:build unix

package offloadexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fake writes an executable shell script standing in for Offload. The script
// records its argv (one per line) and stdin under dir, then runs body.
func fake(t *testing.T, body string) (cmd, dir string) {
	t.Helper()
	dir = t.TempDir()
	cmd = filepath.Join(dir, "offload")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + dir + "/argv'\ncat > '" + dir + "/stdin'\n" + body + "\n"
	if err := os.WriteFile(cmd, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmd, dir
}

func newClient(t *testing.T, cmd string, mod func(*Config)) *Client {
	t.Helper()
	cfg := Config{Command: cmd, Profile: "alias", Timeout: time.Minute}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.termGrace = 300 * time.Millisecond
	return c
}

func okBody(session, profile string) string {
	return `printf '%s\n' '{"ts":"t","status":"ok","exit_code":0,"profile":"` + profile + `","backend":"b","model":"m","session":"` + session +
		`","result":"done","usage":{"in":1},"future":{"x":[1]}}'`
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunSuccessFlagsAndStdin(t *testing.T) {
	cmd, dir := fake(t, okBody("s1", "canon"))
	ws := t.TempDir()
	c := newClient(t, cmd, func(c *Config) { c.Model = "mm" })
	res, err := c.Run(context.Background(), Request{Prompt: "SECRET-PROMPT", Instructions: "INSTR", Workspace: ws, Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || res.Text != "done" || res.Session != "s1" || res.Profile != "canon" || res.Model != "m" ||
		res.Backend != "b" || res.ExitCode != 0 || string(res.Usage) != `{"in":1}` || !json.Valid(res.Raw) {
		t.Fatalf("result = %+v", res)
	}
	argv := readFile(t, dir+"/argv")
	want := []string{"-json", "-no-salvage", "-p", "alias", "-d", ws, "-t", "1m0s", "-m", "mm", "-w"}
	if got := strings.Split(strings.TrimSpace(argv), "\n"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q", got)
	}
	if strings.Contains(argv, "SECRET-PROMPT") || strings.Contains(argv, "INSTR") {
		t.Fatal("task text leaked into argv")
	}
	if got := readFile(t, dir+"/stdin"); got != "INSTR\n\nSECRET-PROMPT" {
		t.Fatalf("stdin = %q", got)
	}
}

func TestRunPromptOnlyAndNoOptionalFlags(t *testing.T) {
	cmd, dir := fake(t, okBody("s1", "canon"))
	c := newClient(t, cmd, nil)
	if _, err := c.Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	argv := readFile(t, dir+"/argv")
	for _, f := range []string{"-m", "-w", "-r"} {
		if strings.Contains("\n"+argv, "\n"+f+"\n") {
			t.Fatalf("unexpected %s in %q", f, argv)
		}
	}
	if readFile(t, dir+"/stdin") != "p" {
		t.Fatal("stdin mismatch")
	}
}

func TestResumeUsesCanonicalProfile(t *testing.T) {
	cmd, dir := fake(t, okBody("s1", "canon"))
	c := newClient(t, cmd, nil)
	res, err := c.Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir(), Session: "s1", ResumeProfile: "canon"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Profile != "canon" {
		t.Fatal(res.Profile)
	}
	argv := "\n" + readFile(t, dir+"/argv")
	if !strings.Contains(argv, "\n-p\ncanon\n") || !strings.Contains(argv, "\n-r\ns1\n") || strings.Contains(argv, "alias") {
		t.Fatalf("argv = %q", argv)
	}
}

func TestResumeMismatchRejected(t *testing.T) {
	ws := t.TempDir()
	for name, body := range map[string]string{
		"session": okBody("other", "canon"),
		"profile": okBody("s1", "different"),
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _ := fake(t, body)
			res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: ws, Session: "s1", ResumeProfile: "canon"})
			var re *RunError
			if !errors.As(err, &re) || re.Kind != KindInvalidOutput || res.Status != StatusError || res.Session != "" || len(res.Raw) == 0 {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

func TestRequestValidation(t *testing.T) {
	cmd, dir := fake(t, okBody("s1", "canon"))
	c := newClient(t, cmd, nil)
	ws := t.TempDir()
	file := filepath.Join(ws, "f")
	_ = os.WriteFile(file, nil, 0o600)
	big := strings.Repeat("a", MaxInputBytes)
	cases := map[string]Request{
		"empty prompt":         {Workspace: ws},
		"relative workspace":   {Prompt: "p", Workspace: "rel"},
		"missing workspace":    {Prompt: "p", Workspace: filepath.Join(ws, "nope")},
		"file workspace":       {Prompt: "p", Workspace: file},
		"resume profile alone": {Prompt: "p", Workspace: ws, ResumeProfile: "canon"},
		"control in session":   {Prompt: "p", Workspace: ws, Session: "a\nb"},
		"control in resume":    {Prompt: "p", Workspace: ws, Session: "s", ResumeProfile: "a\x00b"},
		"control in prompt":    {Prompt: "a\x00b", Workspace: ws},
		"combined over limit":  {Prompt: big, Instructions: "x", Workspace: ws},
		"invalid utf8 prompt":  {Prompt: "\xff", Workspace: ws},
		"control in workspace": {Prompt: "p", Workspace: ws + "\n"},
	}
	for name, req := range cases {
		res, err := c.Run(context.Background(), req)
		var re *RunError
		if !errors.As(err, &re) || re.Kind != KindInvalidRequest || res.ErrorKind != KindInvalidRequest {
			t.Errorf("%s: res=%+v err=%v", name, res, err)
		}
	}
	if _, err := os.Stat(dir + "/argv"); err == nil {
		t.Fatal("process ran for an invalid request")
	}
}

func TestStdinLimitIncludesSeparator(t *testing.T) {
	cmd, dir := fake(t, okBody("s1", "canon"))
	c := newClient(t, cmd, nil)
	ws := t.TempDir()
	run := func(instr, prompt string) error {
		_, err := c.Run(context.Background(), Request{Prompt: prompt, Instructions: instr, Workspace: ws})
		return err
	}
	// Prompt alone: exactly the limit is accepted, one more byte is not.
	if err := run("", strings.Repeat("a", MaxInputBytes)); err != nil {
		t.Fatalf("prompt at limit: %v", err)
	}
	if got := len(readFile(t, dir+"/stdin")); got != MaxInputBytes {
		t.Fatalf("stdin = %d bytes", got)
	}
	if err := run("", strings.Repeat("a", MaxInputBytes+1)); err == nil {
		t.Fatal("prompt over limit accepted")
	}
	// With instructions the two separator bytes count: 1+2+(Max-3) fits exactly.
	if err := run("i", strings.Repeat("a", MaxInputBytes-3)); err != nil {
		t.Fatalf("combined at limit: %v", err)
	}
	if got := len(readFile(t, dir+"/stdin")); got != MaxInputBytes {
		t.Fatalf("stdin = %d bytes", got)
	}
	err := run("i", strings.Repeat("a", MaxInputBytes-2))
	var re *RunError
	if !errors.As(err, &re) || re.Kind != KindInvalidRequest {
		t.Fatalf("one byte over via separator accepted: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	good := Config{Command: "/bin/echo", Profile: "p", Timeout: time.Second}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Config{
		"relative":       {Command: "offload", Profile: "p", Timeout: time.Second},
		"unclean":        {Command: "/bin/../bin/echo", Profile: "p", Timeout: time.Second},
		"no profile":     {Command: "/bin/echo", Timeout: time.Second},
		"control":        {Command: "/bin/echo", Profile: "p\n", Timeout: time.Second},
		"model control":  {Command: "/bin/echo", Profile: "p", Model: "m\x01", Timeout: time.Second},
		"zero timeout":   {Command: "/bin/echo", Profile: "p"},
		"long profile":   {Command: "/bin/echo", Profile: strings.Repeat("a", 300), Timeout: time.Second},
		"excess timeout": {Command: "/bin/echo", Profile: "p", Timeout: 48 * time.Hour},
	}
	for name, cfg := range bad {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Config{Command: "/bin/echo", Profile: "gpt-5.5/high:x", Timeout: time.Second}).Validate(); err != nil {
		t.Fatalf("supported profile name rejected: %v", err)
	}
}

func TestOffloadErrorAndTimeoutStatus(t *testing.T) {
	for _, tc := range []struct {
		status, kind string
		code         int
	}{
		{"error", KindOffloadError, 1},
		{"timeout", KindOffloadTimeout, 124},
	} {
		body := `printf '%s\n' '{"status":"` + tc.status + `","exit_code":` + strconv.Itoa(tc.code) + `,"profile":"p","session":"","result":"provider said sk-SECRET","error":"sk-SECRET"}'; echo sk-SECRET >&2; exit ` + strconv.Itoa(tc.code)
		cmd, _ := fake(t, body)
		res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
		var re *RunError
		if !errors.As(err, &re) || re.Kind != tc.kind || res.Status != tc.status || res.ExitCode != tc.code || len(res.Raw) == 0 {
			t.Fatalf("%s: res=%+v err=%v", tc.status, res, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("error leaks content: %v", err)
		}
	}
}

func TestInvalidOutputs(t *testing.T) {
	cases := map[string]struct {
		body string
		kind string
	}{
		"empty":            {`exit 0`, KindInvalidOutput},
		"malformed":        {`echo '{"status":'`, KindInvalidOutput},
		"not object":       {`echo '[1]'`, KindInvalidOutput},
		"multiple":         {`echo '{"status":"ok","exit_code":0,"profile":"p","session":"s"}'; echo '{"status":"ok","exit_code":0,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"trailing garbage": {`echo '{"status":"ok","exit_code":0,"profile":"p","session":"s"} x'`, KindInvalidOutput},
		"duplicate key":    {`echo '{"status":"ok","status":"ok","exit_code":0,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"nested duplicate": {`echo '{"status":"ok","exit_code":0,"profile":"p","session":"s","usage":{"a":1,"a":2}}'`, KindInvalidOutput},
		"case duplicate":   {`echo '{"status":"ok","Status":"ok","exit_code":0,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"missing status":   {`echo '{"exit_code":0,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"missing exit":     {`echo '{"status":"ok","profile":"p","session":"s"}'`, KindInvalidOutput},
		"null exit":        {`echo '{"status":"ok","exit_code":null,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"missing profile":  {`echo '{"status":"ok","exit_code":0,"session":"s"}'`, KindInvalidOutput},
		"empty profile":    {`echo '{"status":"ok","exit_code":0,"profile":"","session":"s"}'`, KindInvalidOutput},
		"missing session":  {`echo '{"status":"ok","exit_code":0,"profile":"p"}'`, KindInvalidOutput},
		"control session":  {`echo '{"status":"ok","exit_code":0,"profile":"p","session":"a\u0000b"}'`, KindInvalidOutput},
		"unknown status":   {`echo '{"status":"weird","exit_code":0,"profile":"p","session":"s"}'`, KindInvalidOutput},
		"ok nonzero":       {`echo '{"status":"ok","exit_code":1,"profile":"p","session":"s"}'; exit 1`, KindInvalidOutput},
		"exit mismatch":    {`echo '{"status":"ok","exit_code":0,"profile":"p","session":"s"}'; exit 3`, KindInvalidOutput},
		"wrong type":       {`echo '{"status":"ok","exit_code":"0","profile":"p","session":"s"}'`, KindInvalidOutput},
		"nonzero no json":  {`echo oops >&2; exit 2`, KindExit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, _ := fake(t, tc.body)
			res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
			var re *RunError
			if !errors.As(err, &re) || re.Kind != tc.kind || res.Status == StatusOK || res.Session != "" {
				t.Fatalf("res=%+v err=%v", res, err)
			}
		})
	}
}

func TestFutureFieldsAccepted(t *testing.T) {
	cmd, _ := fake(t, `echo '{"status":"ok","exit_code":0,"profile":"p","session":"s","new_thing":{"a":[1,2]},"usage":null}'`)
	res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
	if err != nil || res.Usage != nil || !strings.Contains(string(res.Raw), "new_thing") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestMissingBinary(t *testing.T) {
	c := newClient(t, filepath.Join(t.TempDir(), "absent"), nil)
	res, err := c.Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
	var re *RunError
	if !errors.As(err, &re) || re.Kind != KindStart || res.ErrorKind != KindStart {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestOutputLimit(t *testing.T) {
	for name, body := range map[string]string{
		"stdout": `head -c 5000000 /dev/zero | tr '\0' a`,
		"stderr": `head -c 5000000 /dev/zero | tr '\0' a >&2; ` + okBody("s", "p"),
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _ := fake(t, body)
			start := time.Now()
			res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
			var re *RunError
			if !errors.As(err, &re) || re.Kind != KindOutputLimit || res.Status == StatusOK || len(res.Raw) != 0 {
				t.Fatalf("res kind=%q err=%v", res.ErrorKind, err)
			}
			if time.Since(start) > 10*time.Second {
				t.Fatal("too slow")
			}
		})
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func pidFrom(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pid file not written")
	return 0
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d still alive", pid)
}

// stubborn ignores TERM in the script and in a background child so only KILL stops them.
func stubbornBody(dir string) string {
	return `trap '' TERM
sh -c 'trap "" TERM; echo $$ > ` + dir + `/child.pid; while :; do sleep 1; done' &
while :; do sleep 1; done`
}

func TestCancelTerminatesOwnProcessGroup(t *testing.T) {
	// An unrelated process in the test's own group must survive.
	bystander := startBystander(t)
	cmd, dir := fake(t, "")
	_ = os.WriteFile(cmd, []byte("#!/bin/sh\ncat >/dev/null\n"+stubbornBody(dir)+"\n"), 0o700)
	c := newClient(t, cmd, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		pidFrom(t, dir+"/child.pid")
		cancel()
	}()
	res, err := c.Run(ctx, Request{Prompt: "p", Workspace: t.TempDir()})
	var re *RunError
	if !errors.As(err, &re) || res.Status != StatusCanceled || res.ErrorKind != KindCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	waitDead(t, pidFrom(t, dir+"/child.pid"))
	if !alive(bystander) {
		t.Fatal("bystander process was killed")
	}
}

func TestDeadlineTerminatesGroup(t *testing.T) {
	cmd, dir := fake(t, "")
	_ = os.WriteFile(cmd, []byte("#!/bin/sh\ncat >/dev/null\n"+stubbornBody(dir)+"\n"), 0o700)
	c := newClient(t, cmd, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	res, err := c.Run(ctx, Request{Prompt: "p", Workspace: t.TempDir()})
	if res.Status != StatusTimeout || res.ErrorKind != KindDeadline || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	waitDead(t, pidFrom(t, dir+"/child.pid"))
}

func TestClientTimeoutIsNotCancellation(t *testing.T) {
	cmd, dir := fake(t, "")
	_ = os.WriteFile(cmd, []byte("#!/bin/sh\ncat >/dev/null\n"+stubbornBody(dir)+"\n"), 0o700)
	c := newClient(t, cmd, func(c *Config) { c.Timeout = time.Second })
	c.waitGrace = 200 * time.Millisecond
	res, err := c.Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
	if res.Status != StatusTimeout || res.ErrorKind != KindClientTimeout || err == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	waitDead(t, pidFrom(t, dir+"/child.pid"))
}

func TestAlreadyCanceledDoesNotStart(t *testing.T) {
	cmd, dir := fake(t, okBody("s", "p"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := newClient(t, cmd, nil).Run(ctx, Request{Prompt: "p", Workspace: t.TempDir()})
	if res.Status != StatusCanceled || err == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, statErr := os.Stat(dir + "/argv"); statErr == nil {
		t.Fatal("process started")
	}
}

func TestDescendantAfterSuccessIsReaped(t *testing.T) {
	cmd, dir := fake(t, "")
	_ = os.WriteFile(cmd, []byte("#!/bin/sh\ncat >/dev/null\n(sleep 300 >/dev/null 2>&1 </dev/null & echo $! > "+dir+"/child.pid)\n"+okBody("s", "p")+"\n"), 0o700)
	res, err := newClient(t, cmd, nil).Run(context.Background(), Request{Prompt: "p", Workspace: t.TempDir()})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	waitDead(t, pidFrom(t, dir+"/child.pid"))
}

func startBystander(t *testing.T) int {
	t.Helper()
	p, err := os.StartProcess("/bin/sleep", []string{"sleep", "60"}, &os.ProcAttr{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill(); _, _ = p.Wait() })
	return p.Pid
}

// A wrapper that needs longer than the old 5s grace for its own cleanup must
// finish it under the default shutdown policy and must not be killed early.
func TestCancelDefaultGraceLetsSlowWrapperCleanupFinish(t *testing.T) {
	dir := t.TempDir()
	cmd := filepath.Join(dir, "offload")
	script := `#!/bin/sh
cat >/dev/null
trap 'sleep 6; echo done > '"'` + dir + `/cleanup-finished'"'; exit 143' TERM
echo ready > '` + dir + `/ready'
sleep 30 &
child=$!
echo $child > '` + dir + `/child.pid'
wait $child
`
	if err := os.WriteFile(cmd, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Command: cmd, Profile: "alias", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(dir + "/child.pid"); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	res, runErr := c.Run(ctx, Request{Prompt: "p", Workspace: t.TempDir()})
	elapsed := time.Since(start)
	// Run owns and reaps this process group. Do not signal a saved child PID
	// after shutdown: the kernel may already have reused it for another process.
	if _, err := os.Stat(dir + "/ready"); err != nil {
		t.Fatal("fixture never became ready; cancellation was not tested")
	}
	if res.Status != StatusCanceled || res.ErrorKind != KindCanceled || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("res=%+v err=%v", res, runErr)
	}
	if _, err := os.Stat(dir + "/cleanup-finished"); err != nil {
		t.Fatalf("wrapper was killed before its 6s cleanup finished (Run took %s)", elapsed)
	}
	if elapsed < 5*time.Second || elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want the wrapper's ~6s cleanup, bounded under 10s", elapsed)
	}
	waitDead(t, pidFrom(t, dir+"/child.pid"))
}
