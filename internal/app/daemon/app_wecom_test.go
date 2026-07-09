package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

// recordingWeComChannel is a fake surface.Channel that records the chat ids and
// events passed to Deliver, so a routing test can assert whether an event was
// routed to WeCom.
type recordingWeComChannel struct {
	mu       sync.Mutex
	chatIDs  []string
	events   []eventcontract.Event
	delivers int
}

type flakyWeComChannel struct {
	mu        sync.Mutex
	starts    int
	stops     int
	failures  int
	onSuccess context.CancelFunc
}

func (c *flakyWeComChannel) Name() string { return "wecom" }

func (c *flakyWeComChannel) Start(ctx context.Context, _ surface.ActionHandler) error {
	c.mu.Lock()
	c.starts++
	starts := c.starts
	failures := c.failures
	cancel := c.onSuccess
	c.mu.Unlock()
	if starts <= failures {
		return errors.New("temporary wecom failure")
	}
	if cancel != nil {
		cancel()
	}
	return ctx.Err()
}

func (c *flakyWeComChannel) Deliver(context.Context, string, eventcontract.Event) error { return nil }

func (c *flakyWeComChannel) Stop(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops++
	return nil
}

func (c *flakyWeComChannel) Capabilities() surface.Capabilities { return surface.Capabilities{} }

func (c *flakyWeComChannel) snapshot() (starts, stops int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts, c.stops
}

func (c *recordingWeComChannel) Name() string { return "wecom" }

func (c *recordingWeComChannel) Start(context.Context, surface.ActionHandler) error { return nil }

func (c *recordingWeComChannel) Deliver(_ context.Context, chatID string, event eventcontract.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chatIDs = append(c.chatIDs, chatID)
	c.events = append(c.events, event)
	c.delivers++
	return nil
}

func (c *recordingWeComChannel) Stop(context.Context) error { return nil }

func (c *recordingWeComChannel) Capabilities() surface.Capabilities { return surface.Capabilities{} }

func (c *recordingWeComChannel) snapshot() (int, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delivers, append([]string(nil), c.chatIDs...)
}

func TestIsWeComGateway(t *testing.T) {
	cases := []struct {
		name      string
		gatewayID string
		want      bool
	}{
		{"empty", "", false},
		{"feishu app id", "cli_a1b2c3", false},
		{"feishu-like token", "app-1", false},
		{"wecom bot", wecomGatewayID, true},
		{"wecom prefixed", "wecom:anything", true},
		{"wecom bot padded", "  " + wecomGatewayID + "  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWeComGateway(tc.gatewayID); got != tc.want {
				t.Fatalf("isWeComGateway(%q) = %v, want %v", tc.gatewayID, got, tc.want)
			}
		})
	}
}

func TestWeComSurfaceIDIsRejectedByFeishuParser(t *testing.T) {
	surfaceID := wecomSurfaceID("chat-xyz")
	if surfaceID != wecomGatewayID+":chat:chat-xyz" {
		t.Fatalf("unexpected wecom surface id: %q", surfaceID)
	}
	if wecomSurfaceID("   ") != "" {
		t.Fatalf("blank chat id must yield empty surface id")
	}
	// Collision-safety: a WeCom surface id must never parse as a Feishu surface,
	// so Feishu-specific consumers skip it gracefully.
	if _, ok := feishu.ParseSurfaceRef(surfaceID); ok {
		t.Fatalf("feishu.ParseSurfaceRef accepted a wecom surface id %q", surfaceID)
	}
}

func TestTagWeComInboundAction(t *testing.T) {
	t.Run("tags empty gateway and surface from chat id", func(t *testing.T) {
		got := tagWeComInboundAction(control.Action{
			Kind:   control.ActionTextMessage,
			ChatID: "chat-1",
			Text:   "hi",
		})
		if got.GatewayID != wecomGatewayID {
			t.Fatalf("GatewayID = %q, want %q", got.GatewayID, wecomGatewayID)
		}
		if want := wecomSurfaceID("chat-1"); got.SurfaceSessionID != want {
			t.Fatalf("SurfaceSessionID = %q, want %q", got.SurfaceSessionID, want)
		}
	})
	t.Run("preserves explicit gateway and surface", func(t *testing.T) {
		got := tagWeComInboundAction(control.Action{
			GatewayID:        "explicit-gw",
			SurfaceSessionID: "explicit-surface",
			ChatID:           "chat-1",
		})
		if got.GatewayID != "explicit-gw" || got.SurfaceSessionID != "explicit-surface" {
			t.Fatalf("explicit values overwritten: %#v", got)
		}
	})
	t.Run("leaves surface empty when no chat id", func(t *testing.T) {
		got := tagWeComInboundAction(control.Action{Kind: control.ActionStatus})
		if got.GatewayID != wecomGatewayID {
			t.Fatalf("GatewayID = %q, want %q", got.GatewayID, wecomGatewayID)
		}
		if got.SurfaceSessionID != "" {
			t.Fatalf("SurfaceSessionID = %q, want empty", got.SurfaceSessionID)
		}
	})
}

func TestWeComTextAutoAttachesDefaultWorkspace(t *testing.T) {
	gateway := &messageIDAssigningGateway{}
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	wecomCh := &recordingWeComChannel{}
	app.SetWeComChannel(wecomCh)
	home := t.TempDir()
	t.Setenv("HOME", home)

	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-pool",
		DisplayName:   "headless",
		WorkspaceRoot: home + "/.local/state/codex-remote",
		WorkspaceKey:  home + "/.local/state/codex-remote",
		ShortName:     "codex-remote",
		Backend:       agentproto.BackendCodex,
		Source:        "headless",
		Managed:       true,
		Online:        true,
	})
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-work",
		DisplayName:   "work",
		WorkspaceRoot: "/data/work",
		WorkspaceKey:  "/data/work",
		ShortName:     "work",
		Backend:       agentproto.BackendCodex,
		Source:        "headless",
		Managed:       true,
		Online:        true,
	})

	action := tagWeComInboundAction(control.Action{
		Kind:        control.ActionTextMessage,
		ChatID:      "wcchat-1",
		ActorUserID: "wecom-user",
		MessageID:   "msg-1",
		Text:        "你好",
	})
	app.HandleAction(context.Background(), action)

	surface := app.service.Surface(wecomSurfaceID("wcchat-1"))
	if surface == nil {
		t.Fatal("expected wecom surface")
	}
	if surface.AttachedInstanceID != "inst-work" || surface.ClaimedWorkspaceKey != "/data/work" {
		t.Fatalf("expected default workspace attach to /data/work, got surface=%#v", surface)
	}
	if surface.Platform != "wecom" {
		t.Fatalf("expected wecom platform, got %#v", surface)
	}
}

func TestWeComTextAutoAttachesSharedWorkspaceWhenFeishuOwnsIt(t *testing.T) {
	gateway := &messageIDAssigningGateway{}
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	t.Setenv("HOME", t.TempDir())

	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-shadow",
		DisplayName:   "shadow",
		WorkspaceRoot: "/data/work",
		WorkspaceKey:  "/data/work",
		ShortName:     "shadow",
		Backend:       agentproto.BackendCodex,
		Source:        "headless",
		Managed:       true,
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-shadow": {ThreadID: "thread-shadow", CWD: "/data/work", Loaded: true},
		},
	})

	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-work",
		DisplayName:   "work",
		WorkspaceRoot: "/data/work",
		WorkspaceKey:  "/data/work",
		ShortName:     "work",
		Backend:       agentproto.BackendCodex,
		Source:        "headless",
		Managed:       true,
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", CWD: "/data/work", Loaded: true},
		},
	})

	app.service.MaterializeSurface("feishu:app-1:chat:1", "app-1", "chat-1", "user-1")
	owner := app.service.Surface("feishu:app-1:chat:1")
	if owner == nil {
		t.Fatal("expected feishu owner surface")
	}
	app.service.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachWorkspace,
		SurfaceSessionID: "feishu:app-1:chat:1",
		GatewayID:        "app-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		WorkspaceKey:     "/data/work",
	})
	if owner.AttachedInstanceID == "" {
		t.Fatalf("expected owner attach to choose an online instance, got %#v", owner)
	}

	action := tagWeComInboundAction(control.Action{
		Kind:        control.ActionTextMessage,
		ChatID:      "wcchat-1",
		ActorUserID: "wecom-user",
		MessageID:   "msg-1",
		Text:        "你好",
	})
	app.service.MaterializeSurface(action.SurfaceSessionID, action.GatewayID, action.ChatID, action.ActorUserID)
	app.mu.Lock()
	app.maybeAttachDefaultWeComWorkspaceLocked(context.Background(), action)
	app.mu.Unlock()

	surface := app.service.Surface(wecomSurfaceID("wcchat-1"))
	if surface == nil {
		t.Fatal("expected wecom surface")
	}
	if surface.AttachedInstanceID != owner.AttachedInstanceID || surface.ClaimedWorkspaceKey != "/data/work" {
		t.Fatalf("expected shared workspace attach to follow owner instance %q, got surface=%#v owner=%#v", owner.AttachedInstanceID, surface, owner)
	}
	if !surface.SharedAttach {
		t.Fatalf("expected shared attach flag, got %#v", surface)
	}
}

func TestRunWeComChannelReconnectsAfterTemporaryFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := &flakyWeComChannel{failures: 2, onSuccess: cancel}

	runWeComChannelWithReconnect(ctx, ch, func(context.Context, control.Action) *surface.ActionResult {
		return nil
	}, time.Millisecond, time.Millisecond)

	starts, stops := ch.snapshot()
	if starts != 3 {
		t.Fatalf("starts = %d, want 3", starts)
	}
	if stops != 3 {
		t.Fatalf("stops = %d, want 3", stops)
	}
}

// TestDeliverRoutesFeishuSurfaceToFeishuOnly asserts a Feishu-owned surface
// delivers through the Feishu gateway and NEVER touches the WeCom channel.
func TestDeliverRoutesFeishuSurfaceToFeishuOnly(t *testing.T) {
	gateway := &messageIDAssigningGateway{}
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	app.SetFinalBlockPreviewer(&stubMarkdownPreviewer{})
	wecomCh := &recordingWeComChannel{}
	app.SetWeComChannel(wecomCh)

	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		DisplayName:   "droid",
		WorkspaceRoot: "/data/dl/droid",
		WorkspaceKey:  "/data/dl/droid",
		ShortName:     "droid",
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", CWD: "/data/dl/droid", Loaded: true},
		},
	})
	materializeAttachedSurfaceForFinalCardTest(app, "feishu:app-1:chat:1", "app-1", "chat-1", "ou_user", "inst-1", "/data/dl/droid")

	event := eventcontract.Event{
		Kind:             eventcontract.KindBlockCommitted,
		SurfaceSessionID: "feishu:app-1:chat:1",
		SourceMessageID:  "msg-1",
		Block: &render.Block{
			Kind:       render.BlockAssistantMarkdown,
			InstanceID: "inst-1",
			ThreadID:   "thread-1",
			TurnID:     "turn-1",
			ItemID:     "item-1",
			Text:       "done",
			Final:      true,
		},
	}
	if err := app.deliverUIEventWithContext(context.Background(), event); err != nil {
		t.Fatalf("deliver feishu event: %v", err)
	}

	if ops := gateway.snapshotOperations(); len(ops) == 0 {
		t.Fatal("expected feishu gateway to receive operations")
	}
	if delivers, _ := wecomCh.snapshot(); delivers != 0 {
		t.Fatalf("wecom channel must not receive feishu-owned events, got %d delivers", delivers)
	}
}

// TestDeliverRoutesWeComSurfaceToWeComOnly asserts a WeCom-namespaced surface
// delivers through the WeCom channel and NEVER touches the Feishu gateway.
func TestDeliverRoutesWeComSurfaceToWeComOnly(t *testing.T) {
	gateway := &messageIDAssigningGateway{}
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	wecomCh := &recordingWeComChannel{}
	app.SetWeComChannel(wecomCh)

	surfaceID := wecomSurfaceID("wcchat-1")
	app.service.MaterializeSurface(surfaceID, wecomGatewayID, "wcchat-1", "wecom-user")

	event := eventcontract.Event{
		Kind:             eventcontract.KindBlockCommitted,
		SurfaceSessionID: surfaceID,
		SourceMessageID:  "wmsg-1",
		Block: &render.Block{
			Kind:   render.BlockAssistantMarkdown,
			TurnID: "turn-1",
			ItemID: "item-1",
			Text:   "hello wecom",
			Final:  true,
		},
	}
	if err := app.deliverUIEventWithContext(context.Background(), event); err != nil {
		t.Fatalf("deliver wecom event: %v", err)
	}

	delivers, chatIDs := wecomCh.snapshot()
	if delivers != 1 {
		t.Fatalf("expected exactly 1 wecom deliver, got %d", delivers)
	}
	if len(chatIDs) != 1 || chatIDs[0] != "wcchat-1" {
		t.Fatalf("unexpected wecom chat ids: %#v", chatIDs)
	}
	if ops := gateway.snapshotOperations(); len(ops) != 0 {
		t.Fatalf("feishu gateway must not receive wecom-owned events, got %d ops", len(ops))
	}
}
