package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/inboundmedia"
)

func TestRecordInboundImageMetadataCoversStandaloneAndStructuredImages(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "codex-remote-image-1.jpg"), filepath.Join(dir, "codex-remote-image-2.jpg")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	recordInboundImageMetadata(control.Action{
		Kind: control.ActionImageMessage, GatewayID: "gw", SurfaceSessionID: "surface",
		ActorUserID: "actor", MessageID: "message", LocalPath: paths[0], MIMEType: "image/jpeg",
		Inputs:      []agentproto.Input{{Type: agentproto.InputLocalImage, Path: paths[1], MIMEType: "image/jpeg"}},
		SteerInputs: []agentproto.Input{{Type: agentproto.InputLocalImage, Path: paths[1], MIMEType: "image/jpeg"}},
	})
	for index, path := range paths {
		payload, err := os.ReadFile(path + ".meta.json")
		if err != nil {
			t.Fatal(err)
		}
		var metadata inboundmedia.ImageMetadata
		if err := json.Unmarshal(payload, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.SourceMessageID != "message" || metadata.SurfaceSessionID != "surface" {
			t.Fatalf("bad metadata: %#v", metadata)
		}
		if index == 1 {
			var first inboundmedia.ImageMetadata
			payload, _ := os.ReadFile(paths[0] + ".meta.json")
			_ = json.Unmarshal(payload, &first)
			if metadata.Sequence <= first.Sequence {
				t.Fatalf("sequence not ordered: %d <= %d", metadata.Sequence, first.Sequence)
			}
		}
	}
}
