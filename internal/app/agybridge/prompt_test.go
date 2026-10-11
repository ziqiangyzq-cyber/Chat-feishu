package agybridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func imageBlock(data []byte, mediaType string) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(data)}}
}

func TestPromptImageFilesPreserveBytesOrderAndCleanup(t *testing.T) {
	first, second := []byte("first-image"), []byte("second-image")
	frame := map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "  first question  "}, imageBlock(first, "image/png"), imageBlock(second, "image/jpeg"),
	}}}
	prompt, cleanup, err := promptFromClaudeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	lines := strings.Split(prompt, "\n")
	if lines[0] != "  first question  " {
		t.Fatalf("text changed: %q", lines[0])
	}
	var paths []string
	for i, want := range [][]byte{first, second} {
		path := strings.TrimSuffix(strings.TrimPrefix(lines[i+1], "[Local image: "), "]")
		paths = append(paths, path)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("image %d: data=%q err=%v", i, got, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("image permissions %v", info.Mode())
		}
	}
	if !strings.Contains(prompt, "view_file") {
		t.Fatal("native image reader not requested")
	}
	cleanup()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary image remains: %s", path)
		}
	}
}

func TestPromptImageOnlyAndPathInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.png")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, block := range []any{imageBlock([]byte("image"), "image/webp"), map[string]any{"type": "image", "source": map[string]any{"path": path}}} {
		prompt, cleanup, err := promptFromClaudeFrame(map[string]any{"message": map[string]any{"content": []any{block}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, "[Local image:") || !strings.Contains(prompt, "view_file") {
			t.Fatalf("image-only prompt %q", prompt)
		}
		cleanup()
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cleanup removed caller-owned image")
	}
}

func TestPromptRejectsInvalidImagesInsteadOfDroppingThem(t *testing.T) {
	for _, source := range []map[string]any{
		{"type": "base64", "media_type": "image/png", "data": "!!!"},
		{"type": "base64", "media_type": "image/png", "data": ""},
		{"type": "base64", "media_type": "application/pdf", "data": "YQ=="},
		{"type": "url", "url": "https://example.com/a.png"},
		{"path": filepath.Join(t.TempDir(), "missing.png")},
	} {
		_, cleanup, err := promptFromClaudeFrame(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "image", "source": source}}}})
		cleanup()
		if err == nil {
			t.Fatalf("invalid image silently accepted: %v", source)
		}
	}
}

func TestScanLinesAcceptsBase64ImageLargerThanFourMiB(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{imageBlock(bytes.Repeat([]byte{42}, 4*1024*1024), "image/png")}}})
	lines := make(chan []byte, 1)
	errs := make(chan error, 1)
	scanLines(bytes.NewReader(append(frame, '\n')), lines, errs)
	if err := <-errs; err != io.EOF {
		t.Fatalf("large image frame rejected: %v", err)
	}
	if len(lines) != 1 {
		t.Fatal("large image frame was dropped")
	}
}
