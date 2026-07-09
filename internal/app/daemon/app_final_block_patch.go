package daemon

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	previewpkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/preview"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

const minSecondChanceFinalPreviewTimeout = 500 * time.Millisecond

type secondChanceFinalPatchJob struct {
	GatewayID            string
	ChatID               string
	SurfaceSessionID     string
	DaemonLifecycleID    string
	SourceMessageID      string
	SourceMessagePreview string
	SentBlock            render.Block
	SentTurnDiffPreview  *control.TurnDiffPreview
	FileChangeSummary    *control.FileChangeSummary
	TurnDiffSnapshot     *control.TurnDiffSnapshot
	FinalTurnSummary     *control.FinalTurnSummary
	PreviewRequest       previewpkg.FinalBlockPreviewRequest
}

func (a *App) maybeScheduleSecondChanceFinalPatchLocked(gatewayID, chatID string, event eventcontract.Event, operations []feishu.Operation, previewReq previewpkg.FinalBlockPreviewRequest, rewriteErr error) {
	if a == nil || a.finalBlockPreviewer == nil || rewriteErr == nil || a.shuttingDown {
		return
	}
	if event.Kind != eventcontract.KindBlockCommitted || event.Block == nil || !event.Block.Final {
		return
	}
	if previewReq.Block.Kind != render.BlockAssistantMarkdown || strings.TrimSpace(previewReq.Block.Text) == "" {
		return
	}
	primary := firstFinalSendCard(operations)
	if primary == nil || strings.TrimSpace(primary.FinalSourceBody()) == "" {
		return
	}
	sentBlock := *event.Block
	sentBlock.Text = primary.FinalSourceBody()
	previewReq.Block = sentBlock
	job := secondChanceFinalPatchJob{
		GatewayID:            strings.TrimSpace(gatewayID),
		ChatID:               strings.TrimSpace(chatID),
		SurfaceSessionID:     strings.TrimSpace(event.SurfaceSessionID),
		DaemonLifecycleID:    strings.TrimSpace(event.DaemonLifecycleID),
		SourceMessageID:      strings.TrimSpace(event.SourceMessageID),
		SourceMessagePreview: strings.TrimSpace(event.SourceMessagePreview),
		SentBlock:            sentBlock,
		SentTurnDiffPreview:  event.TurnDiffPreview,
		PreviewRequest:       previewReq,
	}
	if event.FileChangeSummary != nil {
		summary := *event.FileChangeSummary
		if len(summary.Files) != 0 {
			summary.Files = append([]control.FileChangeSummaryEntry(nil), summary.Files...)
		}
		job.FileChangeSummary = &summary
	}
	if event.TurnDiffSnapshot != nil {
		snapshot := *event.TurnDiffSnapshot
		job.TurnDiffSnapshot = &snapshot
	}
	if event.FinalTurnSummary != nil {
		summary := *event.FinalTurnSummary
		if summary.Usage != nil {
			usage := *summary.Usage
			summary.Usage = &usage
		}
		if summary.ThreadUsage != nil {
			usage := *summary.ThreadUsage
			summary.ThreadUsage = &usage
		}
		job.FinalTurnSummary = &summary
	}
	go a.runSecondChanceFinalPatch(job)
}

func (a *App) runSecondChanceFinalPatch(job secondChanceFinalPatchJob) {
	a.mu.Lock()
	if a.shuttingDown || a.finalBlockPreviewer == nil {
		a.mu.Unlock()
		return
	}
	previewer := a.finalBlockPreviewer
	projector := a.projector
	gateway := a.gateway
	previewTimeout := secondChanceFinalPreviewTimeout(a.finalPreviewTimeout)
	gatewayTimeout := a.gatewayApplyTimeout
	a.mu.Unlock()

	previewCtx, previewCancel := a.newTimeoutContext(context.Background(), previewTimeout)
	result, err := previewer.RewriteFinalBlock(previewCtx, job.PreviewRequest)
	previewCancel()
	if err != nil {
		log.Printf(
			"second-chance final patch preview rewrite failed: surface=%s thread=%s turn=%s item=%s err=%v",
			job.SurfaceSessionID,
			job.PreviewRequest.Block.ThreadID,
			job.PreviewRequest.Block.TurnID,
			job.PreviewRequest.Block.ItemID,
			err,
		)
		return
	}
	if sameFinalPatchResult(job.SentBlock, job.SentTurnDiffPreview, result) {
		return
	}

	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return
	}
	anchor := a.service.LookupFinalCardForBlock(job.SurfaceSessionID, job.SentBlock, job.DaemonLifecycleID)
	a.mu.Unlock()
	if anchor == nil {
		return
	}

	ops := projector.ProjectEvent(job.ChatID, eventcontract.Event{
		Meta: eventcontract.EventMeta{
			Target:               eventcontract.ExplicitTarget(job.GatewayID, job.SurfaceSessionID),
			SourceMessageID:      job.SourceMessageID,
			SourceMessagePreview: job.SourceMessagePreview,
		},
		Payload: eventcontract.BlockCommittedPayload{
			Block:             result.Block,
			FileChangeSummary: job.FileChangeSummary,
			TurnDiffSnapshot:  job.TurnDiffSnapshot,
			TurnDiffPreview:   result.TurnDiffPreview,
			FinalTurnSummary:  job.FinalTurnSummary,
		},
	})
	a.mu.Lock()
	ops = a.decorateReviewOperationsLocked(eventcontract.Event{
		Kind:             eventcontract.KindBlockCommitted,
		SurfaceSessionID: job.SurfaceSessionID,
		Block:            &result.Block,
	}, ops)
	a.mu.Unlock()
	primary := firstFinalSendCard(ops)
	if primary == nil {
		return
	}
	op := *primary
	op.Kind = feishu.OperationUpdateCard
	op.MessageID = anchor.MessageID
	op.ReplyToMessageID = ""

	applyCtx, applyCancel := a.newTimeoutContext(context.Background(), gatewayTimeout)
	err = gateway.Apply(applyCtx, []feishu.Operation{op})
	applyCancel()
	if err != nil {
		if a.observeFeishuPermissionError(job.GatewayID, err) {
			log.Printf("second-chance final patch observed feishu permission gap: gateway=%s surface=%s err=%v", job.GatewayID, job.SurfaceSessionID, err)
			return
		}
		a.mu.Lock()
		a.recordDeliveryFailureLocked("feishu", job.GatewayID, job.SurfaceSessionID, string(op.Kind), err)
		a.mu.Unlock()
		log.Printf(
			"second-chance final patch apply failed: surface=%s thread=%s turn=%s item=%s message=%s err=%v",
			job.SurfaceSessionID,
			job.SentBlock.ThreadID,
			job.SentBlock.TurnID,
			job.SentBlock.ItemID,
			anchor.MessageID,
			err,
		)
		return
	}
	a.mu.Lock()
	a.recordDeliverySuccessLocked("feishu", job.GatewayID)
	a.mu.Unlock()
}

func secondChanceFinalPreviewTimeout(base time.Duration) time.Duration {
	if base <= 0 {
		return minSecondChanceFinalPreviewTimeout
	}
	timeout := 2 * base
	if timeout < minSecondChanceFinalPreviewTimeout {
		return minSecondChanceFinalPreviewTimeout
	}
	return timeout
}

func firstFinalSendCard(operations []feishu.Operation) *feishu.Operation {
	for i := range operations {
		if operations[i].Kind != feishu.OperationSendCard {
			continue
		}
		if strings.TrimSpace(operations[i].FinalSourceBody()) == "" {
			continue
		}
		return &operations[i]
	}
	return nil
}

func sameFinalPatchResult(previousBlock render.Block, previousPreview *control.TurnDiffPreview, next previewpkg.FinalBlockPreviewResult) bool {
	if previousBlock.Kind != next.Block.Kind ||
		previousBlock.Language != next.Block.Language ||
		previousBlock.Final != next.Block.Final ||
		strings.TrimSpace(previousBlock.Text) != strings.TrimSpace(next.Block.Text) {
		return false
	}
	return strings.TrimSpace(turnDiffPreviewURL(previousPreview)) == strings.TrimSpace(turnDiffPreviewURL(next.TurnDiffPreview))
}

func turnDiffPreviewURL(preview *control.TurnDiffPreview) string {
	if preview == nil {
		return ""
	}
	return preview.URL
}
