package inboundmedia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordImageMetadataWritesAtomicSidecar(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "codex-remote-image-1.jpg")
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(123, 456).UTC()
	if err := RecordImageMetadata(image, ImageMetadata{
		GatewayID: "gw", SurfaceSessionID: "surface", ActorUserID: "user",
		SourceMessageID: "message", MIMEType: "image/jpeg", ReceivedAt: stamp, Sequence: 9,
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(image + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	var got ImageMetadata
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.SurfaceSessionID != "surface" || got.SourceMessageID != "message" || got.Sequence != 9 || !got.ReceivedAt.Equal(stamp) {
		t.Fatalf("unexpected metadata: %#v", got)
	}
}
