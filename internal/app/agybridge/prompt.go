package agybridge

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AGY print/stream-json input only accepts text. Materialize Claude image
// blocks and give its native view_file tool real files to read for this turn.
func promptFromClaudeFrame(frame map[string]any) (string, func(), error) {
	temporaryDir := ""
	cleanup := func() {
		if temporaryDir != "" {
			_ = os.RemoveAll(temporaryDir)
		}
	}
	fail := func(err error) (string, func(), error) { cleanup(); return "", func() {}, err }
	content := mapValue(frame["message"])["content"]
	if text, ok := content.(string); ok {
		return text, cleanup, nil
	}
	items, ok := content.([]any)
	if !ok {
		return fail(fmt.Errorf("unsupported prompt content"))
	}
	parts := make([]string, 0, len(items))
	imageCount := 0
	for _, raw := range items {
		item := mapValue(raw)
		switch stringValue(item["type"]) {
		case "text":
			if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		case "image":
			source := mapValue(item["source"])
			path := firstNonEmpty(stringValue(source["path"]), stringValue(item["path"]))
			if stringValue(source["type"]) == "base64" {
				extension, ok := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[stringValue(source["media_type"])]
				if !ok {
					return fail(fmt.Errorf("unsupported image media type %q", source["media_type"]))
				}
				data, err := base64.StdEncoding.DecodeString(stringValue(source["data"]))
				if err != nil || len(data) == 0 {
					return fail(fmt.Errorf("invalid or empty base64 image"))
				}
				if temporaryDir == "" {
					temporaryDir, err = os.MkdirTemp("", "codex-remote-agy-images-")
					if err != nil {
						return fail(fmt.Errorf("create image directory: %w", err))
					}
				}
				path = filepath.Join(temporaryDir, fmt.Sprintf("image-%d%s", imageCount+1, extension))
				if err := os.WriteFile(path, data, 0600); err != nil {
					return fail(fmt.Errorf("write image: %w", err))
				}
			}
			if path == "" {
				return fail(fmt.Errorf("unsupported image source"))
			}
			info, err := os.Stat(path)
			if err != nil {
				return fail(fmt.Errorf("read image %q: %w", path, err))
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return fail(fmt.Errorf("image %q is not a nonempty file", path))
			}
			imageCount++
			parts = append(parts, "[Local image: "+path+"]")
		default:
			return fail(fmt.Errorf("unsupported prompt block %q", item["type"]))
		}
	}
	if imageCount > 0 {
		parts = append(parts, "Read each attached local image above with view_file before answering the request. The image contents are part of the user's message.")
	}
	return strings.Join(parts, "\n"), cleanup, nil
}
