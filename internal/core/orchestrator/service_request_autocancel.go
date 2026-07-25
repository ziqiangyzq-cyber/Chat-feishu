package orchestrator

import (
	"fmt"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// expireStaleRequestUserInputPrompts auto-cancels AskUserQuestion / request_user_input
// cards that have sat unanswered longer than RequestUserInputAutoCancelWait. Without this
// an unanswered question card blocks the turn indefinitely, so the user is forced to tap a
// button to make progress ("卡死"). On expiry we run the same cancel/interrupt flow as a
// manual "取消" tap and post a notice explaining the card timed out.
func (s *Service) expireStaleRequestUserInputPrompts(surface *state.SurfaceConsoleRecord, now time.Time) []eventcontract.Event {
	if surface == nil || len(surface.PendingRequests) == 0 {
		return nil
	}
	ttl := s.config.RequestUserInputAutoCancelWait
	if ttl <= 0 {
		return nil
	}
	// Snapshot candidate IDs first; cancelling mutates PendingRequests/PendingRequestOrder.
	ensurePendingRequestOrder(surface)
	candidates := make([]string, 0, len(surface.PendingRequestOrder))
	for _, requestID := range surface.PendingRequestOrder {
		request := surface.PendingRequests[requestID]
		if requestUserInputPromptExpired(request, now, ttl) {
			candidates = append(candidates, requestID)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	minutes := int((ttl + time.Minute - 1) / time.Minute)
	interruptText := fmt.Sprintf("问答卡片超过 %d 分钟未回复，已自动取消并停止当前 turn。", minutes)
	noTurnText := fmt.Sprintf("问答卡片超过 %d 分钟未回复，已自动取消。", minutes)
	var events []eventcontract.Event
	for _, requestID := range candidates {
		request := surface.PendingRequests[requestID]
		if request == nil {
			continue
		}
		events = append(events, s.cancelRequestUserInputTurnWithReason(surface, request, control.Action{}, interruptText, noTurnText)...)
		events = append(events, eventcontract.Event{
			Kind:             eventcontract.KindNotice,
			SurfaceSessionID: surface.SurfaceSessionID,
			Notice: &control.Notice{
				Code: "request_user_input_auto_cancelled",
				Text: interruptText,
			},
		})
	}
	return events
}

// requestUserInputPromptExpired reports whether an unanswered request_user_input card is
// past its auto-cancel deadline. It deliberately skips cards that are already submitting,
// resolved, or aborted, and non request_user_input requests (e.g. approvals) which the
// cancel_turn flow does not support.
func requestUserInputPromptExpired(request *state.RequestPromptRecord, now time.Time, ttl time.Duration) bool {
	if request == nil || ttl <= 0 {
		return false
	}
	if normalizeRequestType(request.RequestType) != "request_user_input" {
		return false
	}
	if requestLifecycleDispatchBlocked(request) {
		return false
	}
	switch normalizeRequestLifecycleState(request.LifecycleState) {
	case requestLifecycleResolved, requestLifecycleAborted:
		return false
	}
	if request.CreatedAt.IsZero() {
		return false
	}
	return !now.Before(request.CreatedAt.Add(ttl))
}
