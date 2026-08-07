package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

const outboundArtifactPolicyTestTimeout = 5 * time.Second

func attachOutboundPolicyTestSurface(t *testing.T, app *App) {
	t.Helper()
	workspaceRoot := t.TempDir()
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		WorkspaceRoot: workspaceRoot,
		WorkspaceKey:  workspaceRoot,
		Source:        "headless",
		Online:        true,
		Threads:       map[string]*state.ThreadRecord{},
	})
	app.HandleAction(context.Background(), control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		GatewayID:        "app-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})
	startToolTestRemoteTurn(t, app, "surface-1", "inst-1", "thread-1", "turn-1")
}

func writeOutboundPolicyCommand(t *testing.T, responsePath string, exitCode int) string {
	t.Helper()
	commandPath := filepath.Join(t.TempDir(), "policy.sh")
	response, err := json.Marshal(map[string]any{
		"path":           responsePath,
		"watermarked":    true,
		"policy_version": "test-v1",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	script := "#!/bin/sh\ncat >/dev/null\n"
	if exitCode == 0 {
		script += "printf '%s\\n' " + shellSingleQuote(string(response)) + "\n"
	} else {
		script += "printf '%s\\n' 'blocked by policy' >&2\nexit 5\n"
	}
	if err := os.WriteFile(commandPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return commandPath
}

func shellSingleQuote(value string) string {
	quoted := "'"
	for _, char := range value {
		if char == '\'' {
			quoted += "'\"'\"'"
		} else {
			quoted += string(char)
		}
	}
	return quoted + "'"
}

func TestOutboundArtifactPolicyConfigMapsPerGateway(t *testing.T) {
	policies := outboundArtifactPoliciesFromFeishuApps([]config.FeishuAppConfig{
		{
			ID: "app-1",
			OutboundArtifactPolicy: &config.OutboundArtifactPolicyConfig{
				Command:        " /opt/policy ",
				TimeoutSeconds: 12,
			},
		},
		{ID: "app-2"},
	})
	if len(policies) != 1 {
		t.Fatalf("expected one policy, got %#v", policies)
	}
	if got := policies["app-1"]; got.Command != "/opt/policy" || got.Timeout != 12*time.Second {
		t.Fatalf("unexpected policy = %#v", got)
	}
}

func TestSendIMFileToolUsesPreparedArtifactPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses unix shell scripts")
	}

	sender := &fakeToolSender{}
	app, _ := newToolServiceTestApp(t, sender)
	if err := app.Bind(); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	attachOutboundPolicyTestSurface(t, app)

	source := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(source, []byte("raw"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	prepared := filepath.Join(t.TempDir(), "summary-watermarked.pdf")
	if err := os.WriteFile(prepared, []byte("%PDF-test"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	app.SetOutboundArtifactPolicies(map[string]outboundArtifactPolicy{
		"app-1": {
			Command: writeOutboundPolicyCommand(t, prepared, 0),
			Timeout: outboundArtifactPolicyTestTimeout,
		},
	})

	_, toolErr := app.sendIMFileTool(
		withToolCallerInstanceID(context.Background(), "inst-1"),
		map[string]any{"path": source},
	)
	if toolErr != nil {
		t.Fatalf("sendIMFileTool() error = %#v", toolErr)
	}
	if len(sender.fileCalls) != 1 || sender.fileCalls[0].Path != prepared {
		t.Fatalf("expected prepared path, got %#v", sender.fileCalls)
	}
}

func TestSendIMImageToolFailsClosedWhenPolicyRejects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses unix shell scripts")
	}

	sender := &fakeToolSender{}
	app, _ := newToolServiceTestApp(t, sender)
	if err := app.Bind(); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	attachOutboundPolicyTestSurface(t, app)

	source := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(source, []byte("raw"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	app.SetOutboundArtifactPolicies(map[string]outboundArtifactPolicy{
		"app-1": {
			Command: writeOutboundPolicyCommand(t, "", 5),
			Timeout: outboundArtifactPolicyTestTimeout,
		},
	})

	_, toolErr := app.sendIMImageTool(
		withToolCallerInstanceID(context.Background(), "inst-1"),
		map[string]any{"path": source},
	)
	if toolErr == nil || toolErr.Code != "artifact_policy_rejected" {
		t.Fatalf("expected policy rejection, got %#v", toolErr)
	}
	if len(sender.imageCalls) != 0 {
		t.Fatalf("image must not be sent after rejection: %#v", sender.imageCalls)
	}
}

func TestSendIMVideoToolCannotBypassConfiguredPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses unix shell scripts")
	}

	sender := &fakeToolSender{}
	app, _ := newToolServiceTestApp(t, sender)
	if err := app.Bind(); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	attachOutboundPolicyTestSurface(t, app)

	source := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(source, []byte("raw"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	app.SetOutboundArtifactPolicies(map[string]outboundArtifactPolicy{
		"app-1": {
			Command: writeOutboundPolicyCommand(t, "", 5),
			Timeout: outboundArtifactPolicyTestTimeout,
		},
	})

	_, toolErr := app.sendIMVideoTool(
		withToolCallerInstanceID(context.Background(), "inst-1"),
		map[string]any{"path": source},
	)
	if toolErr == nil || toolErr.Code != "artifact_policy_rejected" {
		t.Fatalf("expected policy rejection, got %#v", toolErr)
	}
	if len(sender.videoCalls) != 0 {
		t.Fatalf("video must not be sent after rejection: %#v", sender.videoCalls)
	}
}

var _ feishu.Gateway = (*fakeToolSender)(nil)
