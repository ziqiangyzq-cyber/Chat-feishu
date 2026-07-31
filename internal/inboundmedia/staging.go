package inboundmedia

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ImageMetadata struct {
	Version          int       `json:"version"`
	GatewayID        string    `json:"gatewayId"`
	SurfaceSessionID string    `json:"surfaceSessionId"`
	ActorUserID      string    `json:"actorUserId"`
	SourceMessageID  string    `json:"sourceMessageId"`
	OriginalName     string    `json:"originalName,omitempty"`
	MIMEType         string    `json:"mimeType"`
	ReceivedAt       time.Time `json:"receivedAt"`
	Sequence         int64     `json:"sequence"`
}

// RecordImageMetadata atomically binds transport identity and arrival order to
// a staged image. Consumers must ignore unmanifested images by default.
func RecordImageMetadata(imagePath string, metadata ImageMetadata) error {
	metadata.Version = 1
	if metadata.ReceivedAt.IsZero() {
		metadata.ReceivedAt = time.Now().UTC()
	}
	if metadata.Sequence == 0 {
		metadata.Sequence = metadata.ReceivedAt.UnixNano()
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	target := imagePath + ".meta.json"
	temporary, err := os.CreateTemp(filepath.Dir(target), ".image-meta-*")
	if err != nil {
		return err
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, target)
}

// StageImage writes an inbound image to the shared temporary-media area used
// by chat adapters and returns the detected MIME type.
func StageImage(tempDir string, data []byte) (string, string, error) {
	dir, err := ensureTempDir(tempDir)
	if err != nil {
		return "", "", err
	}
	file, err := os.CreateTemp(dir, "codex-remote-image-*")
	if err != nil {
		return "", "", err
	}
	target := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(target)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", "", err
	}
	if err := file.Close(); err != nil {
		return "", "", err
	}
	mimeType := http.DetectContentType(data)
	if ext := imageExtension(mimeType); ext != "" && !strings.HasSuffix(target, ext) {
		renamed := target + ext
		if err := os.Rename(target, renamed); err == nil {
			target = renamed
		}
	}
	keep = true
	return target, mimeType, nil
}

// StageFile writes an inbound file to the shared temporary-media area used by
// chat adapters. The optional source name is retained in the temp-file prefix.
func StageFile(tempDir, fileName string, reader io.Reader) (string, error) {
	dir, err := ensureTempDir(tempDir)
	if err != nil {
		return "", err
	}
	pattern := "codex-remote-file-*"
	if trimmed := strings.TrimSpace(fileName); trimmed != "" {
		pattern = strings.NewReplacer("\n", "-", "\r", "-", "\\", "-", "/", "-").Replace(trimmed) + "-*"
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	target := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(file, reader); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return target, nil
}

func ensureTempDir(tempDir string) (string, error) {
	dir := strings.TrimSpace(tempDir)
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func imageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}
