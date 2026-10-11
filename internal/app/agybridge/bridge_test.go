package agybridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGYBRIDGE_TEST_HELPER") == "1" {
		if capture := os.Getenv("AGYBRIDGE_CAPTURE"); capture != "" {
			for i, arg := range os.Args {
				if arg == "--print" && i+1 < len(os.Args) {
					prompt := os.Args[i+1]
					_ = os.WriteFile(capture, []byte(prompt), 0600)
					if start := strings.Index(prompt, "[Local image: "); start >= 0 {
						path := strings.SplitN(prompt[start+len("[Local image: "):], "]", 2)[0]
						data, err := os.ReadFile(path)
						if err != nil {
							os.Exit(2)
						}
						_ = os.WriteFile(capture+".image", data, 0600)
					}
				}
			}
		}
		if os.Getenv("AGYBRIDGE_WAIT") == "1" {
			time.Sleep(time.Minute)
		}
		fmt.Println(`{"event":"init","conversation_id":"agy-conv-1"}`)
		fmt.Println(`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"AGY_OK"}}`)
		fmt.Println(`{"event":"result","result":{"conversation_id":"agy-conv-1","status":"SUCCESS","response":"AGY_OK","usage":{"input_tokens":11,"output_tokens":2,"cache_read_tokens":3}}}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
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

type resultWriter struct{ result chan []byte }

func (w *resultWriter) Write(data []byte) (int, error) {
	var frame map[string]any
	if json.Unmarshal(data, &frame) == nil && frame["type"] == "result" {
		w.result <- append([]byte(nil), data...)
	}
	return len(data), nil
}

func TestRunMainPreservesBase64Image(t *testing.T) {
	fake, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGYBRIDGE_TEST_HELPER", "1")
	t.Setenv("AGY_BINARY", fake)
	capture := filepath.Join(t.TempDir(), "prompt")
	t.Setenv("AGYBRIDGE_CAPTURE", capture)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	out := &resultWriter{result: make(chan []byte, 1)}
	done := make(chan error, 1)
	go func() { done <- RunMain(context.Background(), nil, reader, out, io.Discard) }()
	image := []byte("real-image-bytes")
	frame := map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(image)}},
		map[string]any{"type": "text", "text": "inspect this"},
	}}}
	encoded, _ := json.Marshal(frame)
	_, _ = writer.Write(append(encoded, '\n'))
	select {
	case <-out.result:
	case <-time.After(5 * time.Second):
		t.Fatal("no terminal result")
	}
	prompt, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "[Local image: ") {
		t.Fatalf("image was dropped from native prompt: %q", prompt)
	}
	actual, err := os.ReadFile(capture + ".image")
	if err != nil || !bytes.Equal(actual, image) {
		t.Fatalf("native received image %q, err=%v", actual, err)
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunMainShutdownCleansActiveImages(t *testing.T) {
	for _, shutdown := range []string{"cancel", "eof"} {
		t.Run(shutdown, func(t *testing.T) {
			fake, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGYBRIDGE_TEST_HELPER", "1")
			t.Setenv("AGY_BINARY", fake)
			t.Setenv("AGYBRIDGE_WAIT", "1")
			capture := filepath.Join(t.TempDir(), "prompt")
			t.Setenv("AGYBRIDGE_CAPTURE", capture)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunMain(ctx, nil, reader, io.Discard, io.Discard) }()
			frame, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{imageBlock([]byte("image"), "image/png")}}})
			_, _ = writer.Write(append(frame, '\n'))
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(capture + ".image"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native did not receive image")
				}
				time.Sleep(10 * time.Millisecond)
			}
			prompt, _ := os.ReadFile(capture)
			path := strings.SplitN(strings.TrimPrefix(string(prompt), "[Local image: "), "]", 2)[0]
			if shutdown == "cancel" {
				cancel()
			} else {
				_ = writer.Close()
			}
			select {
			case err := <-done:
				if shutdown == "eof" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("temporary image dir remains after shutdown: %s", path)
			}
		})
	}
}
