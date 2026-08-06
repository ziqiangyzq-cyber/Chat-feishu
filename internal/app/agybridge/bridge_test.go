package agybridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGYBRIDGE_TEST_HELPER") == "1" {
		fmt.Println(`{"event":"init","conversation_id":"agy-conv-1"}`)
		fmt.Println(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"AGY_OK"}}`)
		fmt.Println(`{"event":"result","result":{"conversation_id":"agy-conv-1","status":"SUCCESS","response":"AGY_OK","usage":{"input_tokens":11,"output_tokens":2,"cache_read_tokens":3}}}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPromptFromClaudeFrame(t *testing.T) {
	frame := map[string]any{
		"message": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "inspect this"},
				map[string]any{"type": "image", "source": map[string]any{"path": "/tmp/a.png"}},
			},
		},
	}
	if got := promptFromClaudeFrame(frame); got != "inspect this\n[Local image: /tmp/a.png]" {
		t.Fatalf("unexpected prompt: %q", got)
	}
}

func TestRunTurnTranslatesAgyStreamToClaudeFrames(t *testing.T) {
	dir := t.TempDir()
	fake, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGYBRIDGE_TEST_HELPER", "1")
	t.Setenv("AGY_BINARY", fake)
	var output bytes.Buffer
	b := &bridge{out: &output, errOut: &bytes.Buffer{}, cwd: dir, permissionMode: "bypassPermissions"}
	conversationID, err := b.runTurn(context.Background(), "hello", "", b.permissionMode, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if conversationID != "agy-conv-1" {
		t.Fatalf("conversation id = %q", conversationID)
	}
	text := output.String()
	for _, want := range []string{
		`"session_id":"agy-conv-1"`,
		`"type":"message_start"`,
		`"text":"AGY_OK"`,
		`"cache_read_input_tokens":3`,
		`"subtype":"success"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %s: %s", want, text)
		}
	}
}
