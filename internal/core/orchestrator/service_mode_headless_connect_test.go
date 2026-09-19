package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestModeSwitchKeepsBackendAfterFreshWorkspaceConnect(t *testing.T) {
	for _, backend := range []agentproto.Backend{agentproto.BackendAgy, agentproto.BackendGrok, agentproto.BackendClaude} {
		t.Run(string(backend), func(t *testing.T) {
			now := time.Date(2026, 9, 19, 12, 17, 0, 0, time.UTC)
			svc := newServiceForTest(&now)
			workspace := t.TempDir()
			svc.UpsertInstance(&state.InstanceRecord{
				InstanceID: "old-codex", WorkspaceRoot: workspace, WorkspaceKey: workspace,
				Backend: agentproto.BackendCodex, Source: "headless", Managed: true, Online: true,
				Threads: map[string]*state.ThreadRecord{},
			})
			svc.ApplySurfaceAction(control.Action{
				Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1",
				ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "old-codex",
			})
			surface := svc.root.Surfaces["surface-1"]
			if surface.AttachedInstanceID != "old-codex" {
				t.Fatal("expected initial Codex attachment")
			}
			svc.ApplySurfaceAction(control.Action{
				Kind: control.ActionModeCommand, SurfaceSessionID: "surface-1", Text: "/mode " + string(backend),
			})
			pending := surface.PendingHeadless
			if pending == nil || pending.Backend != backend || pending.Purpose != state.HeadlessLaunchPurposeFreshWorkspace || !pending.PrepareNewThread {
				t.Fatalf("expected fresh launch for %s, got %#v", backend, pending)
			}
			svc.UpsertInstance(&state.InstanceRecord{
				InstanceID: pending.InstanceID, WorkspaceRoot: workspace, WorkspaceKey: workspace,
				Backend: backend, ClaudeProfileID: pending.ClaudeProfileID,
				Source: "headless", Managed: true, Online: true, Threads: map[string]*state.ThreadRecord{},
			})
			svc.ApplyInstanceConnected(pending.InstanceID)
			if surface.Backend != backend || surface.AttachedInstanceID != pending.InstanceID {
				t.Fatalf("connect must retain %s instance %s; got backend=%s instance=%s", backend, pending.InstanceID, surface.Backend, surface.AttachedInstanceID)
			}
			if surface.PendingHeadless != nil || surface.RouteMode != state.RouteModeNewThreadReady {
				t.Fatalf("expected completed launch ready for a new thread; pending=%#v route=%s", surface.PendingHeadless, surface.RouteMode)
			}
			events := svc.ApplySurfaceAction(control.Action{
				Kind: control.ActionTextMessage, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1",
				MessageID: "first-message", Text: "hello", Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}},
			})
			for _, event := range events {
				if event.Command == nil || event.Command.Kind != agentproto.CommandPromptSend {
					continue
				}
				if surface.AttachedInstanceID != pending.InstanceID || !event.Command.Target.CreateThreadIfMissing || event.Command.Target.CWD != workspace {
					t.Fatalf("first message must create a thread on the selected backend: %#v", event)
				}
				binding := svc.turns.pendingRemote[pending.InstanceID]
				if binding == nil || binding.SurfaceSessionID != "surface-1" || svc.turns.pendingRemote["old-codex"] != nil {
					t.Fatal("first message must dispatch to the new backend, not the old Codex instance")
				}
				return
			}
			t.Fatal("expected first message to dispatch a prompt")
		})
	}
}
