package wecom

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type IMFileSender interface {
	SendIMFile(context.Context, IMFileSendRequest) (IMFileSendResult, error)
}

type IMImageSender interface {
	SendIMImage(context.Context, IMImageSendRequest) (IMImageSendResult, error)
}

type IMVideoSender interface {
	SendIMVideo(context.Context, IMVideoSendRequest) (IMVideoSendResult, error)
}

type IMFileSendRequest struct {
	GatewayID        string
	SurfaceSessionID string
	ChatID           string
	ActorUserID      string
	Path             string
}

type IMImageSendRequest struct {
	GatewayID        string
	SurfaceSessionID string
	ChatID           string
	ActorUserID      string
	Path             string
}

type IMVideoSendRequest struct {
	GatewayID        string
	SurfaceSessionID string
	ChatID           string
	ActorUserID      string
	Path             string
}

type IMFileSendResult struct {
	GatewayID        string
	SurfaceSessionID string
	FileName         string
	MediaID          string
	MessageID        string
}

type IMImageSendResult struct {
	GatewayID        string
	SurfaceSessionID string
	ImageName        string
	MediaID          string
	MessageID        string
}

type IMVideoSendResult struct {
	GatewayID        string
	SurfaceSessionID string
	VideoName        string
	MediaID          string
	MessageID        string
}

type IMMediaSendErrorCode string

const (
	IMMediaSendErrorNotConnected   IMMediaSendErrorCode = "not_connected"
	IMMediaSendErrorUploadFailed   IMMediaSendErrorCode = "upload_failed"
	IMMediaSendErrorSendFailed     IMMediaSendErrorCode = "send_failed"
	IMMediaSendErrorInvalidRequest IMMediaSendErrorCode = "invalid_request"
)

type IMMediaSendError struct {
	Code IMMediaSendErrorCode
	Err  error
}

func (e *IMMediaSendError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.Code)
	}
	return e.Err.Error()
}

func (e *IMMediaSendError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (c *Channel) SendIMFile(ctx context.Context, req IMFileSendRequest) (IMFileSendResult, error) {
	return c.sendMedia(ctx, req.GatewayID, req.SurfaceSessionID, req.ChatID, req.Path, mediaTypeFile)
}

func (c *Channel) SendIMImage(ctx context.Context, req IMImageSendRequest) (IMImageSendResult, error) {
	fileResult, err := c.sendMedia(ctx, req.GatewayID, req.SurfaceSessionID, req.ChatID, req.Path, mediaTypeImage)
	if err != nil {
		return IMImageSendResult{}, err
	}
	return IMImageSendResult{
		GatewayID:        fileResult.GatewayID,
		SurfaceSessionID: fileResult.SurfaceSessionID,
		ImageName:        fileResult.FileName,
		MediaID:          fileResult.MediaID,
		MessageID:        fileResult.MessageID,
	}, nil
}

func (c *Channel) SendIMVideo(ctx context.Context, req IMVideoSendRequest) (IMVideoSendResult, error) {
	fileResult, err := c.sendMedia(ctx, req.GatewayID, req.SurfaceSessionID, req.ChatID, req.Path, mediaTypeFile)
	if err != nil {
		return IMVideoSendResult{}, err
	}
	return IMVideoSendResult{
		GatewayID:        fileResult.GatewayID,
		SurfaceSessionID: fileResult.SurfaceSessionID,
		VideoName:        fileResult.FileName,
		MediaID:          fileResult.MediaID,
		MessageID:        fileResult.MessageID,
	}, nil
}

func (c *Channel) sendMedia(ctx context.Context, gatewayID, surfaceSessionID, chatID, path string, mediaType mediaType) (IMFileSendResult, error) {
	result := IMFileSendResult{
		GatewayID:        strings.TrimSpace(gatewayID),
		SurfaceSessionID: strings.TrimSpace(surfaceSessionID),
		FileName:         strings.TrimSpace(filepath.Base(path)),
	}
	if c == nil || c.client == nil {
		return result, &IMMediaSendError{
			Code: IMMediaSendErrorNotConnected,
			Err:  errors.New("wecom: media sending is unavailable"),
		}
	}
	chatID = strings.TrimSpace(chatID)
	path = strings.TrimSpace(path)
	if chatID == "" || path == "" {
		return result, &IMMediaSendError{
			Code: IMMediaSendErrorInvalidRequest,
			Err:  fmt.Errorf("wecom: media send requires chatID and path"),
		}
	}
	mediaID, fileName, err := c.client.uploadMedia(ctx, path, mediaType)
	if err != nil {
		return result, &IMMediaSendError{
			Code: IMMediaSendErrorUploadFailed,
			Err:  err,
		}
	}
	result.FileName = firstNonEmpty(strings.TrimSpace(fileName), result.FileName)
	result.MediaID = mediaID
	frame := mediaFrame(mediaType, mediaID)
	if err := c.client.sendFrame(ctx, chatID, frame); err != nil {
		return result, &IMMediaSendError{
			Code: IMMediaSendErrorSendFailed,
			Err:  err,
		}
	}
	if messageID, err := c.client.lastAckMessageID(); err == nil {
		result.MessageID = messageID
	}
	return result, nil
}
