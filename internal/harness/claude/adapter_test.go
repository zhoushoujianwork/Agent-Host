//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/testutil"
)

const testSession = "e7a0d011-08ab-48ca-908e-77d853094ba0"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "managed-skill-fixture" {
		if err := serveManagedSkillFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if mode := os.Getenv("AGENT_HOST_CLAUDE_FIXTURE"); mode != "" {
		fixture(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixture(mode string) {
	apiError := strings.HasPrefix(mode, "api_error_nonzero")
	repeatedInit := strings.HasPrefix(mode, "image_reinit_") || strings.HasPrefix(mode, "image_background_")
	if mode == "pipe_holder" {
		time.Sleep(10 * time.Second)
		return
	}
	if mode == "grandchild" {
		time.Sleep(350 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("AGENT_HOST_FIXTURE_MARKER"), []byte("still alive"), 0600)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		version := os.Getenv("AGENT_HOST_FIXTURE_VERSION")
		if version == "" {
			version = "2.1.fixture"
		}
		fmt.Println(version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		if mode != "skills_unsupported" {
			fmt.Println("--plugin-dir <path> session-local skill package")
		}
		return
	}
	session := testSession
	for i, arg := range os.Args {
		if (arg == "--session-id" || arg == "--resume") && i+1 < len(os.Args) {
			session = os.Args[i+1]
		}
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if mode == "restricted_auth" {
		body, _ := json.Marshal(map[string]string{"token": os.Getenv("ANTHROPIC_AUTH_TOKEN"), "base_url": os.Getenv("ANTHROPIC_BASE_URL"), "unreviewed": os.Getenv("UNREVIEWED_PROFILE_ENV")})
		_ = os.WriteFile(os.Getenv("AGENT_HOST_FIXTURE_PROVIDER_ENV"), body, 0600)
	}
	if path := os.Getenv("AGENT_HOST_FIXTURE_REQUEST"); path != "" {
		body, _ := json.Marshal(map[string]any{"args": os.Args[1:], "prompt": string(prompt), "cwd": func() string { cwd, _ := os.Getwd(); return cwd }(), "auto_memory_disabled": os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY"), "additional_instructions": os.Getenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD"), "nonstreaming_fallback_disabled": os.Getenv("CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"), "max_output_tokens": os.Getenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS"), "native_profile": os.Getenv("CLAUDE_CONFIG_DIR")})
		_ = os.WriteFile(path, body, 0600)
	}
	if mode == "stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("diagnostic!", 5000))
		os.Exit(3)
	}
	write := func(v any) { body, _ := json.Marshal(v); fmt.Println(string(body)) }
	if mode != "missing_init" && mode != "api_error_nonzero_missing_init" {
		init := map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": "claude-fixture"}
		if mode == "restricted_auth" || mode == "restricted_readonly" {
			init["tools"] = []string{"Read", "Glob", "Grep"}
		}
		if mode == "restricted_forbidden" {
			init["tools"] = []string{"Read", "Bash"}
		}
		if repeatedInit {
			init["uuid"] = "first"
		}
		if strings.HasPrefix(mode, "mcp_") {
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			for i, arg := range os.Args {
				if arg == "--mcp-config" && i+1 < len(os.Args) {
					_ = json.Unmarshal([]byte(os.Args[i+1]), &config)
				}
			}
			servers := []any{}
			for name := range config.Servers {
				status := strings.TrimPrefix(mode, "mcp_")
				servers = append(servers, map[string]string{"name": name, "status": status})
			}
			if mode == "mcp_ambient" {
				servers = []any{map[string]string{"name": "ambient", "status": "connected"}}
			}
			init["mcp_servers"] = servers
		}
		write(init)
	}
	if mode == "slow_sink" {
		// Wait until the sink holds the first event, then exit with unread output.
		marker := os.Getenv("AGENT_HOST_FIXTURE_MARKER")
		for i := 0; i < 1000; i++ {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		write(map[string]any{"type": "assistant", "session_id": session,
			"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 4000)}}}})
		write(map[string]any{"type": "result", "subtype": "success", "session_id": session,
			"is_error": false, "result": strings.Repeat("x", 4000)})
		_ = os.WriteFile(marker+".exit", []byte("output written"), 0600)
		return
	}
	if mode == "held_stdout" || mode == "held_stdout_partial" {
		exe, _ := os.Executable()
		child := exec.Command(exe)
		child.Env = append(os.Environ(), "AGENT_HOST_CLAUDE_FIXTURE=pipe_holder")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			panic(err)
		}
		if mode == "held_stdout_partial" {
			fmt.Print(`{"type":"result"`)
			return
		}
	}
	if mode == "cancel_tree" || mode == "cancel_detached_tree" {
		exe, _ := os.Executable()
		child := exec.Command(exe)
		child.Env = append(os.Environ(), "AGENT_HOST_CLAUDE_FIXTURE=grandchild")
		if mode == "cancel_detached_tree" {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
	}
	if mode == "image_echo" {
		// A native input acknowledgment must not journal image payload bytes.
		fmt.Print(string(prompt))
	}
	if strings.HasPrefix(mode, "image_background_") {
		fixtureBackgroundContinuation(mode, session, write)
		return
	}
	if strings.HasPrefix(mode, "image_sync_") {
		fixtureSynchronousTask(mode, session, write)
		return
	}
	write(map[string]any{"type": "stream_event", "session_id": session,
		"event": map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-resolved"}}})
	write(map[string]any{"type": "stream_event", "session_id": session,
		"event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "hello"}}})
	if repeatedInit {
		write(map[string]any{"type": "system", "subtype": "task_notification", "session_id": session,
			"task_id": "background", "status": "completed"})
		write(map[string]any{"type": "system", "subtype": "init", "session_id": session,
			"model": "claude-fixture", "uuid": "refreshed"})
		write(map[string]any{"type": "result", "subtype": "success", "session_id": session,
			"is_error": false, "result": "background task result", "origin": map[string]string{"kind": "task-notification"}})
	}
	if mode == "stream_gate" {
		deadline := time.Now().Add(4 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("AGENT_HOST_FIXTURE_MARKER")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(4)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if mode == "cancel_tree" || mode == "cancel_detached_tree" || mode == "callback" {
		time.Sleep(10 * time.Second)
		return
	}
	if mode == "malformed" {
		fmt.Println("{malformed")
		return
	}
	if mode == "missing_final" || mode == "image_reinit_missing_final" {
		return
	}
	text, model := "hello", "claude-resolved"
	if apiError {
		text, model = "API Error: 400 streaming is required for operations that may take longer than 10 minutes", "<synthetic>"
	}
	write(map[string]any{"type": "assistant", "session_id": session, "is_api_error_message": apiError,
		"message": map[string]any{"model": model, "content": []any{map[string]any{"type": "text", "text": text}}}})
	if mode == "mismatch" || mode == "api_error_nonzero_mismatched_session" {
		session = "other-session"
	}
	final := map[string]any{"type": "result", "subtype": "success", "session_id": session, "is_error": mode == "error" || mode == "image_reinit_error" || apiError, "result": text}
	if mode == "structured" {
		final["structured_output"] = map[string]any{"candidates": []any{}}
	}
	if mode == "structured_null" {
		final["structured_output"] = nil
	}
	if mode == "missing_error_field" {
		delete(final, "is_error")
	}
	if mode == "error_subtype" {
		final["subtype"] = "error_max_turns"
	}
	if mode == "unterminated_line" || mode == "nonzero_unterminated" {
		body, _ := json.Marshal(final)
		fmt.Print(string(body))
	} else {
		write(final)
	}
	if mode == "duplicate" || mode == "image_reinit_duplicate_final" {
		write(final)
	}
	if mode == "nonzero" || mode == "nonzero_unterminated" {
		os.Exit(7)
	}
	if apiError {
		os.Exit(1)
	}
}

func fixtureBackgroundContinuation(mode, session string, write func(any)) {
	continuationBeforeMain := mode == "image_background_continuation_first" || strings.HasPrefix(mode, "image_background_duplicate_before_main")
	if mode != "image_background_no_call" {
		write(map[string]any{"type": "assistant", "session_id": session,
			"message": map[string]any{"model": "claude-resolved", "content": []any{map[string]any{
				"type": "tool_use", "id": "agent", "name": "Agent", "input": map[string]any{"run_in_background": true}}}}})
	}
	write(map[string]any{"type": "system", "subtype": "task_started", "session_id": session,
		"task_id": "background", "tool_use_id": "agent", "task_type": "local_agent"})
	main := map[string]any{"type": "result", "subtype": "success", "session_id": session,
		"is_error": mode == "image_background_main_error", "result": "waiting for Agent"}
	if mode == "image_background_early_main" || mode == "image_background_pending" || mode == "image_background_duplicate_main" || mode == "image_background_inline_missing_result" {
		write(main)
	}
	if mode == "image_background_duplicate_main" {
		write(main)
		return
	}
	if mode == "image_background_pending" {
		return
	}
	taskID := "background"
	toolID := "agent"
	if mode == "image_background_unmatched_notice" {
		taskID = "unmatched"
	}
	if mode == "image_background_wrong_tool" {
		toolID = "other-tool"
	}
	write(map[string]any{"type": "system", "subtype": "task_notification", "session_id": session,
		"task_id": taskID, "tool_use_id": toolID, "status": "completed"})
	if strings.HasPrefix(mode, "image_background_inline_") {
		stream := map[string]any{"type": "stream_event", "session_id": session, "parent_tool_use_id": nil,
			"event": map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-resolved"}}}
		switch mode {
		case "image_background_inline_child":
			stream["parent_tool_use_id"] = "child-agent"
		case "image_background_inline_subagent":
			stream["subagent_type"] = "Explore"
		case "image_background_inline_missing_session":
			delete(stream, "session_id")
		}
		write(stream)
		if mode == "image_background_inline_missing_result" {
			return
		}
		if mode == "image_background_inline_reinit" {
			write(main)
			write(map[string]any{"type": "system", "subtype": "init", "session_id": session,
				"model": "claude-fixture", "uuid": "refreshed"})
			write(map[string]any{"type": "result", "subtype": "success", "session_id": session,
				"is_error": false, "result": "actual final answer", "origin": map[string]string{"kind": "task-notification"}})
			return
		}
		main["result"] = "actual inline final answer"
		write(main)
		if mode == "image_background_inline_nonzero" {
			os.Exit(7)
		}
		return
	}
	if mode == "image_background_missing_refresh" {
		write(main)
		return
	}
	write(map[string]any{"type": "system", "subtype": "init", "session_id": session,
		"model": "claude-fixture", "uuid": "refreshed"})
	if mode != "image_background_early_main" && mode != "image_background_missing_main" && !continuationBeforeMain {
		write(main)
	}
	if mode == "image_background_missing_continuation" {
		return
	}
	write(map[string]any{"type": "stream_event", "session_id": session,
		"event": map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-resolved"}}})
	continuation := map[string]any{"type": "result", "subtype": "success", "session_id": session,
		"is_error": mode == "image_background_continuation_error", "result": "actual final answer",
		"origin": map[string]string{"kind": "task-notification"}}
	write(continuation)
	if strings.HasPrefix(mode, "image_background_duplicate_before_main") {
		if mode == "image_background_duplicate_before_main_error" {
			continuation["is_error"] = true
			continuation["result"] = "duplicate failed result"
		}
		write(continuation)
		return
	}
	if mode == "image_background_continuation_first" {
		write(main)
	}
	if mode == "image_background_duplicate_continuation" {
		write(continuation)
	}
}

func fixtureSynchronousTask(mode, session string, write func(any)) {
	write(map[string]any{"type": "assistant", "session_id": session,
		"message": map[string]any{"model": "claude-resolved", "content": []any{map[string]any{
			"type": "tool_use", "id": "agent", "name": "Agent", "input": map[string]any{"run_in_background": false}}}}})
	write(map[string]any{"type": "system", "subtype": "task_started", "session_id": session,
		"task_id": "synchronous", "tool_use_id": "agent", "task_type": "local_agent"})
	if mode == "image_sync_stale_main" {
		write(map[string]any{"type": "result", "subtype": "success", "session_id": session,
			"is_error": false, "result": "waiting for Agent"})
	}
	write(map[string]any{"type": "system", "subtype": "task_notification", "session_id": session,
		"task_id": "synchronous", "tool_use_id": "agent", "status": "completed"})
	toolID := "agent"
	toolResult := map[string]any{"status": "completed"}
	switch mode {
	case "image_sync_false":
		toolResult["isAsync"] = false
	case "image_sync_true":
		toolResult["isAsync"] = true
	case "image_sync_null":
		toolResult["isAsync"] = nil
	case "image_sync_invalid":
		toolResult["isAsync"] = "false"
	case "image_sync_async_launched":
		toolResult["status"] = "async_launched"
	case "image_sync_wrong_tool":
		toolID = "other-tool"
	}
	write(map[string]any{"type": "user", "session_id": session, "tool_use_result": toolResult,
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "Agent completed"}}}})
	if mode == "image_sync_stale_main" {
		return
	}
	write(map[string]any{"type": "result", "subtype": "success", "session_id": session,
		"is_error": false, "result": "actual final answer"})
}

func fixtureAdapter(t *testing.T, mode string) (*Adapter, harness.Request) {
	t.Helper()
	t.Setenv("AGENT_HOST_CLAUDE_FIXTURE", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(exe), harness.Request{Prompt: "private prompt", Workspace: workspace, Model: "requested-model", NativeSessionID: testSession}
}

func TestStreamingArrivesBeforeNativeExit(t *testing.T) {
	adapter, request := fixtureAdapter(t, "stream_gate")
	marker := filepath.Join(t.TempDir(), "release")
	t.Setenv("AGENT_HOST_FIXTURE_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var observations []harness.Observation
	var output strings.Builder
	result, err := adapter.Run(ctx, request, func(o harness.Observation) error {
		observations = append(observations, o)
		if o.Delta != "" {
			output.WriteString(o.Delta)
			return os.WriteFile(marker, []byte("received before exit"), 0600)
		}
		return nil
	})
	if err != nil || result.Status != "completed" || result.ExitCode != 0 || result.Text != "hello" || result.Model != "claude-resolved" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if output.String() != "hello" {
		t.Fatalf("text duplicated or absent: %q", output.String())
	}
	if len(observations) != 5 {
		t.Fatalf("retained %d native observations, want 5", len(observations))
	}
	for _, observation := range observations {
		if !json.Valid(observation.Raw) {
			t.Fatalf("native evidence lost: %+v", observation)
		}
	}
}

func TestSlowSinkDrainsOutputAfterNativeExit(t *testing.T) {
	adapter, request := fixtureAdapter(t, "slow_sink")
	marker := filepath.Join(t.TempDir(), "sink")
	t.Setenv("AGENT_HOST_FIXTURE_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first := true
	result, err := adapter.Run(ctx, request, func(o harness.Observation) error {
		if !first {
			return nil
		}
		first = false
		if err := os.WriteFile(marker, []byte("sink entered"), 0600); err != nil {
			return err
		}
		for {
			if _, err := os.Stat(marker + ".exit"); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
		// Exceed WaitDelay after the child has written its entire result.
		time.Sleep(3 * time.Second)
		return nil
	})
	if err != nil || result.Status != "completed" || result.Text != strings.Repeat("x", 4000) {
		t.Fatalf("status=%s text bytes=%d err=%v", result.Status, len(result.Text), err)
	}
}

func TestInheritedStdoutRemainsBoundedWithoutMalformedTail(t *testing.T) {
	for _, mode := range []string{"held_stdout", "held_stdout_partial"} {
		t.Run(mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			result, err := adapter.Run(ctx, request, nil)
			if !errors.Is(err, exec.ErrWaitDelay) || result.Status != "unknown" || ctx.Err() != nil {
				t.Fatalf("status=%s err=%v context=%v", result.Status, err, ctx.Err())
			}
		})
	}
}

func TestCompletionNeedsProtocolAndExitAgreement(t *testing.T) {
	cases := []struct{ mode, status, contains string }{
		{"error", "failed", "unsuccessful"},
		{"error_subtype", "failed", "error_max_turns"},
		{"nonzero", "failed", "exit status 7"},
		{"nonzero_unterminated", "failed", "exit status 7"},
		{"missing_final", "unknown", "missing final"},
		{"missing_init", "unknown", "missing session initialization"},
		{"malformed", "unknown", "malformed event"},
		{"mismatch", "unknown", "does not match"},
		{"duplicate", "unknown", "after final"},
		{"missing_error_field", "unknown", "lacks session"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, tc.mode)
			result, err := adapter.Run(context.Background(), request, nil)
			if err == nil || result.Status != tc.status || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
		})
	}
}

func TestImageRunWithBackgroundInitializationRefreshRequiresSubmittedTurnResult(t *testing.T) {
	for _, tc := range []struct{ mode, status, contains string }{
		{"success", "completed", ""},
		{"error", "failed", "unsuccessful"},
		{"missing_final", "unknown", "missing final result"},
		{"duplicate_final", "unknown", "after final result"},
	} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resume=%t", tc.mode, resume), func(t *testing.T) {
				adapter, request := fixtureAdapter(t, "image_reinit_"+tc.mode)
				request.Resume = resume
				request.Images = []harness.Image{{MediaType: "image/png", Data: []byte("fixture image")}}
				var observations []harness.Observation
				result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
					observations = append(observations, o)
					return nil
				})
				if (err != nil) != (tc.contains != "") || result.Status != tc.status || result.Model != "claude-resolved" || result.NativeSessionID != request.NativeSessionID {
					t.Fatalf("image run outcome changed after native initialization refresh: %+v, %v", result, err)
				}
				if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
					t.Fatalf("native failure gate changed: %v", err)
				}
				if tc.mode == "missing_final" && result.Text != "" {
					t.Fatalf("background result became submitted task completion: %+v", result)
				}
				if tc.mode == "success" && (result.Text != "hello" || result.ExitCode != 0) {
					t.Fatalf("submitted turn result lost: %+v", result)
				}
				initializations, refreshes := 0, 0
				for _, observation := range observations {
					if observation.Type == "init" {
						initializations++
					}
					if strings.Contains(string(observation.Raw), `"uuid":"refreshed"`) {
						if observation.Type != "native" || observation.Model != "claude-resolved" {
							t.Fatalf("initialization refresh restarted execution or replaced resolved model: %+v", observation)
						}
						refreshes++
					}
				}
				if initializations != 1 || refreshes != 1 {
					t.Fatalf("initialization evidence lost or normalized twice: init=%d refresh=%d", initializations, refreshes)
				}
			})
		}
	}
}

func TestImageBackgroundContinuationRetainsFinalAnswerAndCompletionGates(t *testing.T) {
	for _, tc := range []struct{ mode, status, text, contains string }{
		{"early_main", "completed", "actual final answer", ""},
		{"delayed_main", "completed", "actual final answer", ""},
		{"continuation_first", "completed", "actual final answer", ""},
		{"continuation_error", "failed", "actual final answer", "unsuccessful"},
		{"main_error", "failed", "waiting for Agent", "unsuccessful"},
		{"missing_continuation", "unknown", "waiting for Agent", "missing final result"},
		{"missing_main", "unknown", "actual final answer", "missing final result"},
		{"pending", "unknown", "waiting for Agent", "missing final result"},
		{"missing_refresh", "unknown", "waiting for Agent", "missing final result"},
		{"unmatched_notice", "unknown", "waiting for Agent", "missing final result"},
		{"wrong_tool", "unknown", "waiting for Agent", "missing final result"},
		{"no_call", "unknown", "waiting for Agent", "after final result"},
		{"duplicate_main", "unknown", "waiting for Agent", "duplicate submitted turn result"},
		{"duplicate_continuation", "unknown", "actual final answer", "after final result"},
		{"duplicate_before_main", "unknown", "actual final answer", "duplicate native continuation result"},
		{"duplicate_before_main_error", "unknown", "actual final answer", "duplicate native continuation result"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "image_background_"+tc.mode)
			request.Images = []harness.Image{{MediaType: "image/png", Data: []byte("fixture image")}}
			var observations []harness.Observation
			result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
				observations = append(observations, o)
				return nil
			})
			if (err != nil) != (tc.contains != "") || result.Status != tc.status || result.Text != tc.text {
				t.Fatalf("background continuation changed task outcome: %+v, %v", result, err)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("background continuation lost failure gate: %v", err)
			}
			if tc.status == "completed" || tc.status == "failed" {
				if result.Model != "claude-resolved" || result.ExitCode != 0 {
					t.Fatalf("native model or process outcome changed: %+v", result)
				}
				initializations, finals := 0, 0
				for _, observation := range observations {
					if observation.Type == "init" {
						initializations++
					}
					if observation.Type == "result" {
						finals++
					}
				}
				if initializations != 1 || finals != 1 {
					t.Fatalf("native continuation restarted execution or completed twice: init=%d finals=%d", initializations, finals)
				}
			}
		})
	}
}

func TestImageSynchronousTaskRequiresCorrelatedCompletedToolResult(t *testing.T) {
	for _, mode := range []string{"absent", "false", "true", "null", "invalid", "async_launched", "wrong_tool", "stale_main"} {
		t.Run(mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "image_sync_"+mode)
			request.Images = []harness.Image{{MediaType: "image/png", Data: []byte("fixture image")}}
			var observations []harness.Observation
			result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
				observations = append(observations, o)
				return nil
			})
			completed := mode == "absent" || mode == "false"
			if completed {
				if err != nil || result.Status != "completed" || result.Text != "actual final answer" || result.Model != "claude-resolved" || result.ExitCode != 0 {
					t.Fatalf("correlated synchronous completion rejected: %+v %v", result, err)
				}
			} else if err == nil || result.Status != "unknown" || !strings.Contains(err.Error(), "missing final result") {
				t.Fatalf("unrelated or asynchronous tool result completed the task: %+v %v", result, err)
			}
			if mode == "stale_main" && result.Text != "waiting for Agent" {
				t.Fatalf("earlier waiting evidence lost: %+v", result)
			}
			initializations, finals := 0, 0
			for _, observation := range observations {
				if observation.Type == "init" {
					initializations++
				}
				if observation.Type == "result" {
					finals++
				}
			}
			if initializations != 1 || completed && finals != 1 || !completed && finals != 0 {
				t.Fatalf("synchronous native events restarted or prematurely completed execution: init=%d finals=%d", initializations, finals)
			}
		})
	}
}

func TestImageInlineTaskNotificationRequiresParentResponseAndCleanExit(t *testing.T) {
	for _, tc := range []struct{ mode, status, text, contains string }{
		{"complete", "completed", "actual inline final answer", ""},
		{"child", "unknown", "actual inline final answer", "missing final result"},
		{"subagent", "unknown", "actual inline final answer", "missing final result"},
		{"missing_session", "unknown", "actual inline final answer", "missing final result"},
		{"missing_result", "unknown", "waiting for Agent", "missing final result"},
		{"reinit", "completed", "actual final answer", ""},
		{"nonzero", "failed", "actual inline final answer", "exit status 7"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "image_background_inline_"+tc.mode)
			request.Images = []harness.Image{{MediaType: "image/png", Data: []byte("fixture image")}}
			result, err := adapter.Run(context.Background(), request, nil)
			if (err != nil) != (tc.contains != "") || result.Status != tc.status || result.Text != tc.text {
				t.Fatalf("inline notification changed task outcome: %+v %v", result, err)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("inline notification lost failure gate: %v", err)
			}
		})
	}
}

func TestNativeAPIFailureRetainsFinalCauseAndProcessExit(t *testing.T) {
	adapter, request := fixtureAdapter(t, "api_error_nonzero")
	result, err := adapter.Run(context.Background(), request, nil)
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || result.ExitCode != 1 || result.Status != "failed" {
		t.Fatalf("native API failure lost its process outcome: result=%+v err=%v", result, err)
	}
	for _, cause := range []string{"unsuccessful result (success)", "API Error: 400 streaming is required", "Claude process: exit status 1"} {
		if !strings.Contains(err.Error(), cause) || !strings.Contains(result.Error, cause) {
			t.Fatalf("native failure diagnostic omitted %q: result=%+v err=%v", cause, result, err)
		}
	}
	if result.Text != "API Error: 400 streaming is required for operations that may take longer than 10 minutes" || result.Model != "<synthetic>" {
		t.Fatalf("native API evidence changed: %+v", result)
	}
}

func TestNativeAPIFailureRequiresValidProtocolBeforePromotingDiagnostic(t *testing.T) {
	for _, mode := range []string{"api_error_nonzero_missing_init", "api_error_nonzero_mismatched_session"} {
		t.Run(mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, mode)
			result, err := adapter.Run(context.Background(), request, nil)
			if err == nil || strings.Contains(err.Error(), "API Error: 400") || strings.Contains(result.Error, "API Error: 400") {
				t.Fatalf("invalid native protocol promoted a final diagnostic: result=%+v err=%v", result, err)
			}
			if mode == "api_error_nonzero_missing_init" && (result.Status != "failed" || !strings.Contains(err.Error(), "Claude process: exit status 1")) {
				t.Fatalf("nonzero exit without initialization changed outcome: result=%+v err=%v", result, err)
			}
			if mode == "api_error_nonzero_mismatched_session" && (result.Status != "unknown" || !strings.Contains(err.Error(), "does not match requested session")) {
				t.Fatalf("invalid session lost protocol failure precedence: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestFinalLineWithoutNewline(t *testing.T) {
	adapter, request := fixtureAdapter(t, "unterminated_line")
	result, err := adapter.Run(context.Background(), request, nil)
	if err != nil || result.Status != "completed" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestPromptUsesStdinAndToolFreeModeRestrictsNativeFacilities(t *testing.T) {
	adapter, request := fixtureAdapter(t, "normal")
	path := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
	request.ToolsDisabled, request.Resume = true, true
	_, err := adapter.Run(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args   []string `json:"args"`
		Prompt string   `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Prompt != request.Prompt || slices.Contains(captured.Args, request.Prompt) {
		t.Fatalf("prompt transport = %+v", captured)
	}
	for _, flag := range []string{"--resume", "--model", "--include-partial-messages", "--strict-mcp-config", "--disable-slash-commands"} {
		if !slices.Contains(captured.Args, flag) {
			t.Errorf("missing flag %s", flag)
		}
	}
	toolsIndex := slices.Index(captured.Args, "--tools")
	if toolsIndex < 0 || captured.Args[toolsIndex+1] != "" {
		t.Fatal("built-in tools were not disabled")
	}
	for _, arg := range captured.Args {
		if strings.Contains(arg, "skip-permissions") || arg == "bypassPermissions" || arg == "--fallback-model" || arg == "--session-id" {
			t.Errorf("unexpected argument %q", arg)
		}
	}
}

func TestNativeStreamingPolicyMatchesValidatedEnvironmentForTaskAndExtraction(t *testing.T) {
	for _, extraction := range []bool{false, true} {
		for _, tc := range []struct{ name, disabled, limit string }{
			{"default", "1", ""},
			{"native-recovery", "0", ""},
			{"configured-recovery", "0", "64000"},
		} {
			t.Run(fmt.Sprintf("extraction=%t/%s", extraction, tc.name), func(t *testing.T) {
				adapter, request := fixtureAdapter(t, "normal")
				request.Extraction, request.ToolsDisabled = extraction, extraction
				path := filepath.Join(t.TempDir(), "request.json")
				t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
				t.Setenv(nativeFallbackEnvironment, "1")
				t.Setenv(nativeOutputEnvironment, "8192")
				wantFallback, wantLimit := tc.disabled, tc.limit
				if tc.disabled == "0" {
					t.Setenv(nativeFallbackEnvironment, "0")
				} else {
					if err := os.Unsetenv(nativeFallbackEnvironment); err != nil {
						t.Fatal(err)
					}
				}
				if tc.limit == "" {
					if err := os.Unsetenv(nativeOutputEnvironment); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(nativeOutputEnvironment, tc.limit)
				}
				result, err := adapter.Run(context.Background(), request, nil)
				if err != nil || result.Status != "completed" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var captured struct {
					Args             []string `json:"args"`
					FallbackDisabled string   `json:"nonstreaming_fallback_disabled"`
					MaxOutputTokens  string   `json:"max_output_tokens"`
				}
				if err := json.Unmarshal(raw, &captured); err != nil {
					t.Fatal(err)
				}
				if captured.FallbackDisabled != wantFallback || captured.MaxOutputTokens != wantLimit {
					t.Fatalf("native environment differs from validated policy: %+v", captured)
				}
				var settings struct {
					Env               map[string]string `json:"env"`
					AutoMemoryEnabled *bool             `json:"autoMemoryEnabled"`
					DisableAllHooks   *bool             `json:"disableAllHooks"`
				}
				index := slices.Index(captured.Args, "--settings")
				if index < 0 || index+1 >= len(captured.Args) || json.Unmarshal([]byte(captured.Args[index+1]), &settings) != nil || settings.Env[nativeFallbackEnvironment] != wantFallback || settings.Env[nativeOutputEnvironment] != wantLimit {
					t.Fatalf("native settings could override the validated policy: %v", captured.Args)
				}
				if _, supplied := settings.Env[nativeOutputEnvironment]; supplied != (wantLimit != "") {
					t.Fatalf("native model output defaults were overridden: %v", settings.Env)
				}
				if settings.AutoMemoryEnabled == nil || *settings.AutoMemoryEnabled || extraction && (settings.DisableAllHooks == nil || !*settings.DisableAllHooks) || !extraction && settings.DisableAllHooks != nil {
					t.Fatalf("streaming policy changed native memory or hook restrictions: %+v", settings)
				}
				if slices.Contains(captured.Args, "--fallback-model") {
					t.Fatal("native recovery configured a different model")
				}
			})
		}
	}
}

func TestNativeStreamingPolicyRejectsMalformedConfigurationBeforeLaunch(t *testing.T) {
	for _, tc := range []struct{ name, disabled, limit string }{
		{"empty-limit", "0", ""},
		{"zero-limit", "0", "0"},
		{"negative-limit", "0", "-1"},
		{"invalid-limit", "0", "many"},
		{"leading-zero", "0", "08192"},
		{"signed-limit", "0", "+8192"},
		{"whitespace-limit", "0", "8192 "},
		{"overflow-limit", "0", "99999999999999999999999999999"},
		{"invalid-disable", "true", "8192"},
		{"empty-disable", "", "8192"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "normal")
			path := filepath.Join(t.TempDir(), "request.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
			t.Setenv(nativeFallbackEnvironment, tc.disabled)
			t.Setenv(nativeOutputEnvironment, tc.limit)
			var observed int
			result, err := adapter.Run(context.Background(), request, func(harness.Observation) error { observed++; return nil })
			if err == nil || result.Status != "failed" || result.ExitCode != -1 || observed != 0 {
				t.Fatalf("unsafe policy reached native execution: result=%+v err=%v observed=%d", result, err, observed)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe policy launched the native fixture: %v", err)
			}
		})
	}
}

func TestNativeStreamingPolicyPreservesNativeOutputCaps(t *testing.T) {
	for _, disabled := range []string{"0", "1"} {
		for _, limit := range []string{"1", "64000", "128000"} {
			policy, err := NativeStreamEnvironment(map[string]string{nativeFallbackEnvironment: disabled, nativeOutputEnvironment: limit})
			if err != nil || policy[nativeOutputEnvironment] != limit {
				t.Fatalf("native output configuration changed: policy=%v err=%v", policy, err)
			}
		}
	}
}

func TestCancellationStopsProcessGroup(t *testing.T) {
	testutil.RequireProcessListing(t, "/bin/ps", "-axo", "pid=,ppid=")
	for _, mode := range []string{"cancel_tree", "cancel_detached_tree"} {
		t.Run(mode, func(t *testing.T) {
			unrelated := exec.Command("/bin/sleep", "10")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
			adapter, request := fixtureAdapter(t, mode)
			marker := filepath.Join(t.TempDir(), "child-survived")
			t.Setenv("AGENT_HOST_FIXTURE_MARKER", marker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			result, err := adapter.Run(ctx, request, func(o harness.Observation) error {
				if o.Type == "text_delta" {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || result.Status != "canceled" || time.Since(start) > 2*time.Second {
				t.Fatalf("cancellation result = %+v, err = %v", result, err)
			}
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("cancellation touched an unrelated process: %v", err)
			}
			time.Sleep(500 * time.Millisecond)
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("descendant survived group cancellation: %v", err)
			}
		})
	}
}

func TestCallbackFailureAbortsNativeProcess(t *testing.T) {
	adapter, request := fixtureAdapter(t, "callback")
	want := errors.New("journal disk full")
	start := time.Now()
	result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error {
		if o.Delta != "" {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) || result.Status != "unknown" || time.Since(start) > 2*time.Second {
		t.Fatalf("callback result = %+v, err = %v", result, err)
	}
}

func TestStderrIsBoundedAndFailureVisible(t *testing.T) {
	adapter, request := fixtureAdapter(t, "stderr")
	result, err := adapter.Run(context.Background(), request, nil)
	if err == nil || result.Status != "failed" || !strings.Contains(result.Error, "[truncated]") || len(result.Error) > stderrLimit+200 {
		t.Fatalf("stderr result status=%s length=%d err=%v", result.Status, len(result.Error), err)
	}
}

func TestNativeVersion(t *testing.T) {
	adapter, _ := fixtureAdapter(t, "normal")
	version, err := adapter.Version(context.Background())
	if err != nil || version != "2.1.fixture" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}

func TestExplicitAllowedToolsAndExtractionFlags(t *testing.T) {
	base := harness.Request{Prompt: "extract", Workspace: t.TempDir(), NativeSessionID: testSession, AllowedTools: []string{"Read", "Edit", "Bash(go test *)"}}
	args, err := arguments(base)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(args, "--allowedTools")
	if index < 0 || !slices.Equal(args[index+1:index+4], base.AllowedTools) {
		t.Fatalf("native permission rules changed: %v", args)
	}
	base.AllowedTools = nil
	base.ToolsDisabled, base.Extraction = true, true
	base.JSONSchema = `{"type":"object","properties":{"candidates":{"type":"array"}},"required":["candidates"]}`
	args, err = arguments(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--safe-mode", "--no-session-persistence", "--system-prompt", "--session-id", "--json-schema", "--tools", "--disable-slash-commands", "--strict-mcp-config", "--no-chrome"} {
		if !slices.Contains(args, flag) {
			t.Fatalf("missing extraction restriction %s: %v", flag, args)
		}
	}
	if args[slices.Index(args, "--system-prompt")+1] != extractionSystemPrompt || args[slices.Index(args, "--tools")+1] != "" || args[slices.Index(args, "--json-schema")+1] != base.JSONSchema {
		t.Fatal("extraction context flags changed")
	}
	for _, arg := range args {
		if strings.Contains(arg, "skip-permissions") || arg == "bypassPermissions" || arg == "--fallback-model" || arg == "--resume" || arg == "--bare" {
			t.Fatalf("unsafe extraction argument %s", arg)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*harness.Request)
	}{
		{"resume", func(r *harness.Request) { r.Resume = true }},
		{"additional-directory", func(r *harness.Request) { r.AdditionalDirectories = []string{t.TempDir()} }},
		{"tools-enabled", func(r *harness.Request) { r.ToolsDisabled = false }},
		{"business-permissions", func(r *harness.Request) { r.PermissionMode = "bypassPermissions" }},
		{"business-instructions", func(r *harness.Request) { r.HostInstructions = "business role" }},
		{"allowed-tools", func(r *harness.Request) { r.AllowedTools = []string{"Read"} }},
		{"mcp-server", func(r *harness.Request) {
			r.MCPServers = []harness.MCPServer{{Name: "knowledge_dokki", Command: "/bin/agent-host"}}
		}},
		{"invalid-schema", func(r *harness.Request) { r.JSONSchema = `{"broken"` }},
		{"null-schema", func(r *harness.Request) { r.JSONSchema = `null` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if _, err := arguments(r); err == nil {
				t.Fatal("invalid extraction request accepted")
			}
		})
	}
	for _, value := range []string{"", "  ", "--dangerously-skip-permissions", "Read\x00Edit"} {
		base.ToolsDisabled, base.Extraction = false, false
		base.AllowedTools = []string{value}
		if _, err := arguments(base); err == nil {
			t.Fatalf("invalid allowed-tool argument accepted: %q", value)
		}
	}
}

func TestNativeMCPMountsAreExplicitAndPreserveResumeAndPermissions(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			adapter, request := fixtureAdapter(t, "mcp_connected")
			request.Resume = resume
			request.MCPServers = []harness.MCPServer{
				{Name: "knowledge_dokki", Command: "/opt/agent-host", Args: []string{"knowledge", "serve", "dokki"}},
				{Name: "knowledge_confluence", Command: "/opt/agent-host", Args: []string{"knowledge", "serve", "confluence"}},
			}
			path := filepath.Join(t.TempDir(), "request.json")
			t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
			result, err := adapter.Run(context.Background(), request, nil)
			if err != nil || result.Status != "completed" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var captured struct {
				Args []string `json:"args"`
			}
			if err := json.Unmarshal(raw, &captured); err != nil {
				t.Fatal(err)
			}
			args := captured.Args
			if !slices.Contains(args, "--strict-mcp-config") || slices.Contains(args, "--resume") != resume || slices.Contains(args, "--session-id") == resume {
				t.Fatalf("MCP selection or native session changed: %v", args)
			}
			for _, arg := range args {
				if strings.Contains(arg, "skip-permissions") || arg == "bypassPermissions" || arg == "--allowedTools" {
					t.Fatalf("MCP mount granted permissions: %s", arg)
				}
			}
			index := slices.Index(args, "--mcp-config")
			var config struct {
				Servers map[string]map[string]json.RawMessage `json:"mcpServers"`
			}
			if index < 0 || json.Unmarshal([]byte(args[index+1]), &config) != nil || len(config.Servers) != 2 {
				t.Fatalf("missing exact mount set: %v", args)
			}
			for _, server := range request.MCPServers {
				mount := config.Servers[server.Name]
				command, _ := json.Marshal(server.Command)
				wantArgs, _ := json.Marshal(server.Args)
				if len(mount) != 3 || string(mount["type"]) != `"stdio"` || string(mount["command"]) != string(command) || string(mount["args"]) != string(wantArgs) {
					t.Fatalf("native mount changed: %+v", mount)
				}
			}
		})
	}
}

func TestNativeMCPEmptyConfigurationAndValidation(t *testing.T) {
	base := harness.Request{Prompt: "task", Workspace: t.TempDir(), NativeSessionID: testSession}
	args, err := arguments(base)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(args, "--mcp-config")
	if !slices.Contains(args, "--strict-mcp-config") || index < 0 || args[index+1] != `{"mcpServers":{}}` {
		t.Fatalf("ambient MCP configuration allowed: %v", args)
	}
	good := harness.MCPServer{Name: "knowledge_dokki", Command: "/bin/agent-host", Args: []string{"serve"}}
	for _, tc := range []struct {
		name    string
		servers []harness.MCPServer
	}{
		{"duplicate", []harness.MCPServer{good, good}},
		{"empty-name", []harness.MCPServer{{Command: good.Command}}},
		{"unsafe-name", []harness.MCPServer{{Name: "source/name", Command: good.Command}}},
		{"reserved-name", []harness.MCPServer{{Name: "__proto__", Command: good.Command}}},
		{"relative-command", []harness.MCPServer{{Name: good.Name, Command: "agent-host"}}},
		{"nul-command", []harness.MCPServer{{Name: good.Name, Command: "/bin/host\x00"}}},
		{"nul-argument", []harness.MCPServer{{Name: good.Name, Command: good.Command, Args: []string{"bad\x00argument"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			request.MCPServers = tc.servers
			if _, err := arguments(request); err == nil {
				t.Fatal("invalid native MCP configuration accepted")
			}
		})
	}
	base.ToolsDisabled, base.MCPServers = true, []harness.MCPServer{good}
	if _, err := arguments(base); err == nil || !strings.Contains(err.Error(), "tool-free") {
		t.Fatalf("tool-free execution accepted MCP mount: %v", err)
	}
}

func TestNativeMCPSetupFailureCannotBecomeSuccessfulExecution(t *testing.T) {
	for _, mode := range []string{"normal", "mcp_failed", "mcp_pending", "mcp_ambient"} {
		t.Run(mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, mode)
			request.MCPServers = []harness.MCPServer{{Name: "knowledge_dokki", Command: "/bin/agent-host"}}
			var observed harness.Observation
			result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error { observed = o; return nil })
			if err == nil || result.Status != "failed" || observed.Type != "protocol_error" || !strings.Contains(result.Error, "MCP") {
				t.Fatalf("setup failure disappeared: result=%+v err=%v observation=%+v", result, err, observed)
			}
		})
	}
}

func TestStructuredOutputComesFromNativeResult(t *testing.T) {
	for _, mode := range []string{"structured", "normal", "structured_null"} {
		t.Run(mode, func(t *testing.T) {
			adapter, request := fixtureAdapter(t, mode)
			request.ToolsDisabled, request.Extraction = true, true
			request.JSONSchema = `{"type":"object"}`
			result, err := adapter.Run(context.Background(), request, nil)
			if mode == "structured" {
				if err != nil || string(result.StructuredOutput) != `{"candidates":[]}` || result.Status != "completed" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "missing structured output") || result.Status != "unknown" {
				t.Fatalf("missing output accepted: %+v %v", result, err)
			}
		})
	}
}
