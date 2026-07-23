package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestWorkspaceSurfaceContextWrittenAndRemovedForNormalMode(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".git", "info"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git/info): %v", err)
	}
	app := New("127.0.0.1:0", "127.0.0.1:0", nil, agentproto.ServerIdentity{StartedAt: time.Now().UTC()})
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		WorkspaceRoot: workspaceRoot,
		WorkspaceKey:  workspaceRoot,
		Source:        "vscode",
		Online:        true,
		Threads:       map[string]*state.ThreadRecord{},
	})

	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})

	contextPath := workspaceSurfaceContextPath(workspaceRoot)
	var payload workspaceSurfaceContextPayload
	eventually(t, time.Second, func() bool {
		var err error
		payload, err = readWorkspaceSurfaceContext(contextPath)
		return err == nil
	})
	if payload.SurfaceSessionID != "surface-1" {
		t.Fatalf("unexpected surface context payload: %#v", payload)
	}
	var excludeRaw []byte
	eventually(t, time.Second, func() bool {
		var err error
		excludeRaw, err = os.ReadFile(filepath.Join(workspaceRoot, ".git", "info", "exclude"))
		return err == nil
	})
	if !strings.Contains(string(excludeRaw), "/.codex-remote/") {
		t.Fatalf("expected local git exclude entry, got %q", string(excludeRaw))
	}

	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionDetach,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
	})
	eventually(t, time.Second, func() bool {
		_, err := os.Stat(contextPath)
		return os.IsNotExist(err)
	})
}

func TestWorkspaceSurfaceContextNotWrittenForVSCodeMode(t *testing.T) {
	workspaceRoot := t.TempDir()
	app := New("127.0.0.1:0", "127.0.0.1:0", nil, agentproto.ServerIdentity{StartedAt: time.Now().UTC()})
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		WorkspaceRoot: workspaceRoot,
		WorkspaceKey:  workspaceRoot,
		Source:        "vscode",
		Online:        true,
		Threads:       map[string]*state.ThreadRecord{},
	})

	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionModeCommand,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		Text:             "/mode vscode",
	})
	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})

	if _, err := os.Stat(workspaceSurfaceContextPath(workspaceRoot)); !os.IsNotExist(err) {
		t.Fatalf("expected no workspace context file in vscode mode, stat err=%v", err)
	}
}

func TestBlockedWorkspaceSurfaceContextWriteDoesNotBlockApp(t *testing.T) {
	workspaceRoot := t.TempDir()
	app := New("127.0.0.1:0", "127.0.0.1:0", nil, agentproto.ServerIdentity{StartedAt: time.Now().UTC()})
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		WorkspaceRoot: workspaceRoot,
		WorkspaceKey:  workspaceRoot,
		Source:        "vscode",
		Online:        true,
		Threads:       map[string]*state.ThreadRecord{},
	})

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	app.workspaceContextWriter.apply = func(workspaceSurfaceContextWriteRequest) {
		startedOnce.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() { close(release) })

	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("workspace context writer did not start")
	}

	statusDone := make(chan struct{})
	go func() {
		_ = app.runtimeStatusPayload()
		close(statusDone)
	}()
	select {
	case <-statusDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("runtime status blocked behind workspace context filesystem I/O")
	}
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition did not become true before timeout")
	}
}
