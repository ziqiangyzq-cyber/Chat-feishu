package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/adapter/wecom"
)

func (a *App) sendIMVideoTool(ctx context.Context, arguments map[string]any) (map[string]any, *toolError) {
	path, _ := arguments["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, &toolError{
			Code:    "path_required",
			Message: "path is required",
		}
	}
	if apiErr := validateSendVideoPath(path); apiErr != nil {
		return nil, apiErr
	}

	a.mu.Lock()
	resolved, apiErr := a.resolveToolCallerSurfaceContextLocked(toolCallerInstanceIDFromContext(ctx))
	a.mu.Unlock()
	if apiErr != nil {
		return nil, apiErr
	}
	if !resolved.Attached {
		return nil, &toolError{
			Code:    "surface_not_attached",
			Message: "surface is not attached to a workspace",
		}
	}

	result, err := a.sendSurfaceVideo(ctx, resolved, path)
	if err != nil {
		var sendErr *feishu.IMVideoSendError
		if errors.As(err, &sendErr) {
			switch sendErr.Code {
			case feishu.IMVideoSendErrorUploadFailed:
				return nil, &toolError{Code: "upload_failed", Message: sendErr.Error()}
			case feishu.IMVideoSendErrorSendFailed, feishu.IMVideoSendErrorMissingReceiveTarget, feishu.IMVideoSendErrorGatewayNotRunning:
				return nil, &toolError{Code: "send_failed", Message: sendErr.Error(), Retryable: true}
			}
		}
		var wecomErr *wecom.IMMediaSendError
		if errors.As(err, &wecomErr) {
			switch wecomErr.Code {
			case wecom.IMMediaSendErrorUploadFailed:
				return nil, &toolError{Code: "upload_failed", Message: wecomErr.Error()}
			case wecom.IMMediaSendErrorSendFailed, wecom.IMMediaSendErrorNotConnected:
				return nil, &toolError{Code: "send_failed", Message: wecomErr.Error(), Retryable: true}
			}
		}
		return nil, &toolError{
			Code:      "send_failed",
			Message:   err.Error(),
			Retryable: true,
		}
	}
	log.Printf("tool call: tool=%s surface=%s path=%s status=ok message=%s", feishuSendIMVideoToolName, resolved.SurfaceSessionID, path, result.MessageID)
	response := map[string]any{
		"surface_session_id": result.SurfaceSessionID,
		"gateway_id":         result.GatewayID,
		"video_name":         result.FileName,
		"file_key":           result.FileKey,
		"message_id":         result.MessageID,
	}
	if isWeComGateway(result.GatewayID) {
		response["delivery_kind"] = "wecom_attachment_video"
		response["delivery_note"] = "已通过企业微信附件消息发送 MP4。"
	}
	return response, nil
}

func validateSendVideoPath(path string) *toolError {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &toolError{
			Code:    "video_not_found",
			Message: "path does not exist",
		}
	case err != nil:
		return &toolError{
			Code:    "video_access_failed",
			Message: "failed to access local video",
		}
	case info.IsDir():
		return &toolError{
			Code:    "invalid_video_path",
			Message: "path must point to a video file",
		}
	}
	if strings.ToLower(strings.TrimSpace(filepath.Ext(path))) != ".mp4" {
		return &toolError{
			Code:    "invalid_video_path",
			Message: "path must point to an .mp4 video file",
		}
	}
	return nil
}

func (a *App) sendSurfaceVideo(ctx context.Context, resolved resolvedToolSurfaceContext, path string) (surfaceFileSendResult, error) {
	if isWeComGateway(resolved.GatewayID) {
		a.mu.Lock()
		channel := a.wecomChannelForGatewayLocked(resolved.GatewayID)
		a.mu.Unlock()
		sender, ok := channel.(wecom.IMVideoSender)
		if !ok {
			return surfaceFileSendResult{}, errors.New("wecom IM video sending is not available in this runtime")
		}
		result, err := sender.SendIMVideo(ctx, wecom.IMVideoSendRequest{
			GatewayID:        resolved.GatewayID,
			SurfaceSessionID: resolved.SurfaceSessionID,
			ChatID:           resolved.ChatID,
			ActorUserID:      resolved.ActorUserID,
			Path:             path,
		})
		if err != nil {
			return surfaceFileSendResult{}, err
		}
		return surfaceFileSendResult{
			GatewayID:        result.GatewayID,
			SurfaceSessionID: result.SurfaceSessionID,
			FileName:         result.VideoName,
			FileKey:          result.MediaID,
			MessageID:        result.MessageID,
		}, nil
	}
	sender, ok := a.gateway.(feishu.IMVideoSender)
	if !ok {
		return surfaceFileSendResult{}, errors.New("Feishu IM video sending is not available in this runtime")
	}
	result, err := sender.SendIMVideo(ctx, feishu.IMVideoSendRequest{
		GatewayID:        resolved.GatewayID,
		SurfaceSessionID: resolved.SurfaceSessionID,
		ChatID:           resolved.ChatID,
		ActorUserID:      resolved.ActorUserID,
		Path:             path,
	})
	if err != nil {
		_ = a.observeFeishuPermissionError(resolved.GatewayID, err)
		return surfaceFileSendResult{}, err
	}
	return surfaceFileSendResult{
		GatewayID:        result.GatewayID,
		SurfaceSessionID: result.SurfaceSessionID,
		FileName:         result.VideoName,
		FileKey:          result.FileKey,
		MessageID:        result.MessageID,
	}, nil
}
