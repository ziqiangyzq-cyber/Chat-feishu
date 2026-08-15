package grokbridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("GROKBRIDGE_TEST_HELPER") == "1" {
		if os.Getenv("GROKBRIDGE_TEST_FAIL") == "1" {
			fmt.Println(`{"type":"system","subtype":"init","session_id":"grok-session-error"}`)
			fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"unknown model","session_id":"grok-session-error"}`)
			os.Exit(1)
		}
		fmt.Println(`{"type":"system","subtype":"init","session_id":"grok-session-1","model":"grok-4.6","cwd":"/tmp","permissionMode":"default"}`)
		fmt.Println(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"GROK_OK"}]},"session_id":"grok-session-1"}`)
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"GROK_OK","session_id":"grok-session-1"}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunTurnDoesNotDuplicateStructuredGrokError(t *testing.T) {
	fake, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROKBRIDGE_TEST_HELPER", "1")
	t.Setenv("GROKBRIDGE_TEST_FAIL", "1")
	t.Setenv("GROK_BINARY", fake)
	var output bytes.Buffer
	b := &bridge{out: &output, errOut: &bytes.Buffer{}, cwd: t.TempDir(), permissionMode: "bypassPermissions"}
	_, err = b.runTurn(context.Background(), "hello", "", b.permissionMode, "bad-model", "")
	if err == nil {
		t.Fatal("expected fake Grok failure")
	}
	if got := strings.Count(output.String(), `"type":"result"`); got != 1 {
		t.Fatalf("structured error result count = %d, want 1: %s", got, output.String())
	}
}

func TestPromptFromClaudeFrame(t *testing.T) {
	frame := map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "inspect this"},
		map[string]any{"type": "image", "source": map[string]any{"path": "/tmp/a.png"}},
	}}}
	if got := promptFromClaudeFrame(frame); got != "inspect this\n[Local image: /tmp/a.png]" {
		t.Fatalf("unexpected prompt: %q", got)
	}
}

func TestWritePromptFileUsesPrivateFile(t *testing.T) {
	path, err := writePromptFile("private prompt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt file mode = %#o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "private prompt" {
		t.Fatalf("prompt file content = %q", content)
	}
}

func TestRunTurnPassesThroughGrokMessagesAndTracksSession(t *testing.T) {
	fake, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROKBRIDGE_TEST_HELPER", "1")
	t.Setenv("GROK_BINARY", fake)
	var output bytes.Buffer
	b := &bridge{out: &output, errOut: &bytes.Buffer{}, cwd: t.TempDir(), permissionMode: "bypassPermissions"}
	sessionID, err := b.runTurn(context.Background(), "hello", "", b.permissionMode, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "grok-session-1" {
		t.Fatalf("session id = %q", sessionID)
	}
	for _, want := range []string{`"session_id":"grok-session-1"`, `"type":"assistant"`, `"text":"GROK_OK"`, `"subtype":"success"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %s: %s", want, output.String())
		}
	}
}
