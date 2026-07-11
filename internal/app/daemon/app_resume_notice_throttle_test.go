package daemon

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

func resumeFailureNoticeEvent(surfaceID, code string, family control.NoticeDeliveryFamily) eventcontract.Event {
	notice := &control.Notice{Code: code, Title: "恢复失败", Text: "测试通知"}
	if family != "" {
		notice.DeliveryClass = control.NoticeDeliveryClassGlobalRuntime
		notice.DeliveryFamily = family
	}
	return eventcontract.Event{
		Kind:             eventcontract.KindNotice,
		SurfaceSessionID: surfaceID,
		Notice:           notice,
	}
}

func TestSurfaceResumeNoticeThrottleAllowsOncePerWindow(t *testing.T) {
	throttle := &surfaceResumeNoticeThrottle{}
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	if !throttle.allow("surface-1", base) {
		t.Fatal("expected first notice to pass")
	}
	// 同 surface 连发 N 次，窗口内全部被压制。
	for i := 1; i <= 5; i++ {
		if throttle.allow("surface-1", base.Add(time.Duration(i)*3*time.Second)) {
			t.Fatalf("expected notice %d within window to be throttled", i)
		}
	}
	if throttle.allow("surface-1", base.Add(9*time.Minute)) {
		t.Fatal("expected notice at 9m to still be throttled")
	}
	// 10 分钟后允许再次发出。
	if !throttle.allow("surface-1", base.Add(10*time.Minute)) {
		t.Fatal("expected notice after window to pass")
	}
	// 不同 surface 互不影响。
	if !throttle.allow("surface-2", base.Add(time.Second)) {
		t.Fatal("expected different surface to be unaffected")
	}
}

func TestIsSurfaceResumeFailureNoticeEvent(t *testing.T) {
	cases := []struct {
		name  string
		event eventcontract.Event
		want  bool
	}{
		{"headless restore failure", resumeFailureNoticeEvent("s", "headless_restore_thread_busy", ""), true},
		{"headless restore policy denied", resumeFailureNoticeEvent("s", "headless_restore_workspace_policy_denied", ""), true},
		{"surface resume family", resumeFailureNoticeEvent("s", "surface_resume_workspace_busy", control.NoticeDeliveryFamilySurfaceResume), true},
		{"vscode resume family", resumeFailureNoticeEvent("s", "surface_resume_instance_busy", control.NoticeDeliveryFamilyVSCodeResume), true},
		{"restore success not throttled", resumeFailureNoticeEvent("s", "headless_restore_attached", ""), false},
		{"resume success not throttled", resumeFailureNoticeEvent("s", "surface_resume_attached", ""), false},
		{"workspace resume success not throttled", resumeFailureNoticeEvent("s", "surface_resume_workspace_attached", ""), false},
		{"instance resume success not throttled", resumeFailureNoticeEvent("s", "surface_resume_instance_attached", ""), false},
		{"unrelated notice", resumeFailureNoticeEvent("s", "workspace_attached", ""), false},
		{"non-notice event", eventcontract.Event{Kind: eventcontract.KindSnapshot, SurfaceSessionID: "s"}, false},
	}
	for _, tc := range cases {
		if got := isSurfaceResumeFailureNoticeEvent(tc.event); got != tc.want {
			t.Fatalf("%s: isSurfaceResumeFailureNoticeEvent = %v, want %v", tc.name, got, tc.want)
		}
	}
}
