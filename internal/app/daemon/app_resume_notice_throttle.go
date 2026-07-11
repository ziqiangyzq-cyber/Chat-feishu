package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

// surfaceResumeFailureNoticeThrottleWindow 限定同一 surface 的
// surface-resume / headless-restore 家族失败通知在 10 分钟内最多发出 1 条。
//
// 背景（2026-07-11 刷屏事故）：升级后 pinned 恢复目标失效，恢复失败通知除了
// recovery map 的 LastNoticeCode 去重路径外，还会经 attach / queue dispatch 等
// 其他发射路径流出（rewriteHeadlessRestoreFailureEvents 观测到约每 3 秒 1 条，
// 单聊天数小时内累计 457 条）。因此在 daemon 出站收敛点做统一限流，
// 不再依赖各发射路径自身的去重逻辑。
const surfaceResumeFailureNoticeThrottleWindow = 10 * time.Minute

// surfaceResumeNoticeThrottle 是内存态限流器；daemon 重启后清零（可接受）。
// 使用独立互斥锁，避免与 App.mu 产生锁序耦合（出站投递路径在持锁/不持锁
// 两种模式下都会经过这里）。
type surfaceResumeNoticeThrottle struct {
	mu         sync.Mutex
	lastSentAt map[string]time.Time
}

// allow 判断当前时刻是否允许向 surfaceID 发送一条同族失败通知；
// 允许时同步记录发送时间。
func (t *surfaceResumeNoticeThrottle) allow(surfaceID string, now time.Time) bool {
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSentAt == nil {
		t.lastSentAt = map[string]time.Time{}
	}
	if last, ok := t.lastSentAt[surfaceID]; ok && now.Sub(last) < surfaceResumeFailureNoticeThrottleWindow {
		return false
	}
	t.lastSentAt[surfaceID] = now
	return true
}

// isSurfaceResumeFailureNoticeEvent 识别 surface-resume / headless-restore
// 家族的"恢复失败"类通知。恢复成功通知（headless_restore_attached、
// surface_resume_attached、surface_resume_workspace_attached、
// surface_resume_instance_attached 等，均不带失败 family 且不匹配失败前缀）
// 不受限流影响。
func isSurfaceResumeFailureNoticeEvent(event eventcontract.Event) bool {
	if event.Kind != eventcontract.KindNotice || event.Notice == nil {
		return false
	}
	switch event.Notice.DeliveryFamily {
	case control.NoticeDeliveryFamilySurfaceResume, control.NoticeDeliveryFamilyVSCodeResume:
		return true
	}
	code := strings.TrimSpace(event.Notice.Code)
	return strings.HasPrefix(code, "headless_restore_") && code != "headless_restore_attached"
}
