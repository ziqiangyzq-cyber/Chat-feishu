package agybridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	fake := filepath.Join(dir, "agy")
	script := `#!/bin/sh
printf '%s\n' '{"event":"init","conversation_id":"agy-conv-1"}'
printf '%s\n' '{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"AGY_OK"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"agy-conv-1","status":"SUCCESS","response":"AGY_OK","usage":{"input_tokens":11,"output_tokens":2,"cache_read_tokens":3}}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
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
