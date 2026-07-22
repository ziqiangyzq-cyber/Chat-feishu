package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestDetachReleasesWorkspaceClaim(t *testing.T) {
	now := time.Date(2026, 4, 9, 11, 20, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:              "inst-1",
		DisplayName:             "droid-a",
		WorkspaceRoot:           "/data/dl/droid",
		WorkspaceKey:            "/data/dl/droid",
		ShortName:               "droid-a",
		Online:                  true,
		ObservedFocusedThreadID: "thread-1",
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", Name: "修复登录流程", CWD: "/data/dl/droid"},
		},
	})
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:              "inst-2",
		DisplayName:             "droid-b",
		WorkspaceRoot:           "/data/dl/droid",
		WorkspaceKey:            "/data/dl/droid",
		ShortName:               "droid-b",
		Online:                  true,
		ObservedFocusedThreadID: "thread-2",
		Threads: map[string]*state.ThreadRecord{
			"thread-2": {ThreadID: "thread-2", Name: "整理日志", CWD: "/data/dl/droid"},
		},
	})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "inst-1"})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionDetach, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1"})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-2",
		ChatID:           "chat-2",
		ActorUserID:      "user-2",
		InstanceID:       "inst-2",
	})

	surface := svc.root.Surfaces["surface-2"]
	if surface.AttachedInstanceID != "inst-2" || surface.ClaimedWorkspaceKey != "/data/dl/droid" {
		t.Fatalf("expected workspace claim to be released for second attach, got %#v", surface)
	}
	if len(events) == 0 || events[0].Notice == nil || events[0].Notice.Code != "attached" {
		t.Fatalf("expected attach success after detach release, got %#v", events)
	}
}

func TestNormalModeListIncludesHeadlessWorkspace(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-headless-1",
		DisplayName:   "headless",
		WorkspaceRoot: "/data/dl/runtime/headless",
		WorkspaceKey:  "/data/dl/runtime/headless",
		ShortName:     "headless",
		Source:        "headless",
		Managed:       true,
		Online:        true,
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionListInstances,
		SurfaceSessionID: "feishu:app-1:chat:1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
	})

	if len(events) != 1 {
		t.Fatalf("expected one target picker for headless-only runtime, got %#v", events)
	}
	view := targetPickerFromEvent(t, events[0])
	if view.Source != control.TargetPickerRequestSourceList || view.Title != "切换工作区与会话" {
		t.Fatalf("unexpected target picker: %#v", view)
	}
	if len(view.WorkspaceOptions) != 1 {
		t.Fatalf("expected only headless workspace in existing-workspace mode, got %#v", view.WorkspaceOptions)
	}
	if _, ok := targetPickerWorkspaceOption(view, "/data/dl/runtime/headless"); !ok {
		t.Fatalf("expected only headless workspace in target picker, got %#v", view.WorkspaceOptions)
	}
	if len(view.SessionOptions) != 1 {
		t.Fatalf("expected headless-only workspace without sessions to offer new-thread only, got %#v", view.SessionOptions)
	}
	if option := view.SessionOptions[0]; option.Value != targetPickerNewThreadValue || option.Kind != control.FeishuTargetPickerSessionNewThread {
		t.Fatalf("expected headless-only workspace to offer new-thread fallback, got %#v", view.SessionOptions)
	}
	if view.SelectedSessionValue != targetPickerNewThreadValue || view.ConfirmLabel != "新建会话" || !view.CanConfirm {
		t.Fatalf("expected headless-only workspace to default to new-thread, got %#v", view)
	}
}
