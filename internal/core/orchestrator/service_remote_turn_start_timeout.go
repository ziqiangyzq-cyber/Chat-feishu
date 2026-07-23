package orchestrator

import (
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

// expirePendingRemoteTurnStarts prevents a prompt that was delivered to a
// wrapper but never produced turn.started from holding the surface queue
// forever. This most commonly happens when the native app-server wedges while
// resuming an old thread.
func (s *Service) expirePendingRemoteTurnStarts(now time.Time) []eventcontract.Event {
	if s == nil || s.turns == nil || s.config.RemoteTurnStartWait <= 0 {
		return nil
	}
	var events []eventcontract.Event
	for instanceID := range s.turns.pendingRemote {
		binding, surface, item := s.pendingRemoteBindingRecord(instanceID)
		if binding == nil || surface == nil || item == nil || binding.DispatchedAt.IsZero() {
			continue
		}
		if now.Before(binding.DispatchedAt.Add(s.config.RemoteTurnStartWait)) {
			continue
		}

		threadID := strings.TrimSpace(binding.ThreadID)
		inst := s.root.Instances[instanceID]
		recycleHeadless := inst != nil && inst.Managed && strings.EqualFold(strings.TrimSpace(inst.Source), "headless")
		noticeText := "本地会话未能及时开始处理，本条消息已取消。请检查本地实例后重新发送刚才的内容。"
		if recycleHeadless {
			noticeText = "本地模型会话未能及时开始处理，卡住的后台会话将自动重启。本条消息已取消；请发送 /new，再重新发送刚才的内容。"
		}
		events = append(events, s.failSurfaceActiveQueueItem(surface, item, &control.Notice{
			Code:     "remote_turn_start_timeout",
			Title:    "本地会话启动超时",
			Text:     noticeText,
			ThemeKey: "error",
		}, false)...)
		if !recycleHeadless {
			continue
		}
		events = append(events, eventcontract.Event{
			Kind:             eventcontract.KindDaemonCommand,
			SurfaceSessionID: surface.SurfaceSessionID,
			DaemonCommand: &control.DaemonCommand{
				Kind:             control.DaemonCommandKillHeadless,
				SurfaceSessionID: surface.SurfaceSessionID,
				InstanceID:       instanceID,
				ThreadID:         threadID,
			},
		})
	}
	return events
}
