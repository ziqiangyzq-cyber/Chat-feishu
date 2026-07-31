package daemon

import (
	"log"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/inboundmedia"
)

func recordInboundImageMetadata(action control.Action) {
	type stagedImageInput struct{ path, mimeType string }
	imageInputs := make([]stagedImageInput, 0)
	seen := make(map[string]bool)
	add := func(imagePath, mimeType string) {
		imagePath = strings.TrimSpace(imagePath)
		if imagePath == "" || seen[imagePath] {
			return
		}
		seen[imagePath] = true
		imageInputs = append(imageInputs, stagedImageInput{imagePath, strings.TrimSpace(mimeType)})
	}
	if action.Kind == control.ActionImageMessage {
		add(action.LocalPath, action.MIMEType)
	}
	for _, input := range append(append([]agentproto.Input(nil), action.Inputs...), action.SteerInputs...) {
		if input.Type == agentproto.InputLocalImage {
			add(input.Path, input.MIMEType)
		}
	}
	receivedAt := time.Now().UTC()
	if action.Inbound != nil && !action.Inbound.MessageCreateTime.IsZero() {
		receivedAt = action.Inbound.MessageCreateTime.UTC()
	}
	sequence := time.Now().UnixNano()
	for _, input := range imageInputs {
		originalName := ""
		if input.path == strings.TrimSpace(action.LocalPath) {
			originalName = action.FileName
		}
		if err := inboundmedia.RecordImageMetadata(input.path, inboundmedia.ImageMetadata{
			GatewayID: action.GatewayID, SurfaceSessionID: action.SurfaceSessionID,
			ActorUserID: action.ActorUserID, SourceMessageID: action.MessageID,
			OriginalName: originalName, MIMEType: input.mimeType,
			ReceivedAt: receivedAt, Sequence: sequence,
		}); err != nil {
			log.Printf("staged image metadata failed: path=%q surface=%q message=%q err=%v", input.path, action.SurfaceSessionID, action.MessageID, err)
		}
		sequence++
	}
}
