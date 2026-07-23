package wrapper

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

const bootstrapConfigReadIDForTest = "relay-bootstrap-config-read"

func TestBootstrapHeadlessCodexCompletesInitializeHandshake(t *testing.T) {
	for _, source := range []string{"headless", "cron"} {
		t.Run(source, func(t *testing.T) {
			app := New(Config{
				Source:        source,
				Version:       "test",
				WorkspaceRoot: "/tmp/project",
			})

			bufferedBeforeInitialize := mustJSONLine(t, map[string]any{
				"method": "thread/started",
				"params": map[string]any{
					"thread": map[string]any{
						"id": "thread-before-initialize",
					},
				},
			})
			initializeResponse := mustJSONLine(t, map[string]any{
				"id": relayBootstrapInitializeID,
				"result": map[string]any{
					"userAgent": "mockcodex/0.0.1",
				},
			})
			bufferedWhileWaitingForConfig := mustJSONLine(t, map[string]any{
				"method": "thread/started",
				"params": map[string]any{
					"thread": map[string]any{
						"id": "thread-before-config",
					},
				},
			})
			configReadResponse := mustJSONLine(t, map[string]any{
				"id": bootstrapConfigReadIDForTest,
				"result": map[string]any{
					"config": map[string]any{"model": "gpt-effective"},
				},
			})

			var childStdin bytes.Buffer
			replayedStdout, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(bufferedBeforeInitialize+initializeResponse+bufferedWhileWaitingForConfig+configReadResponse), nil, nil)
			if err != nil {
				t.Fatalf("bootstrap headless codex: %v", err)
			}

			frames := decodeJSONLines(t, childStdin.String())
			if len(frames) != 3 {
				t.Fatalf("expected 3 bootstrap frames, got %d: %s", len(frames), childStdin.String())
			}
			if got := lookupStringFromMap(frames[0], "method"); got != "initialize" {
				t.Fatalf("expected first frame to be initialize, got %q", got)
			}
			if got := lookupStringFromMap(frames[0], "id"); got != relayBootstrapInitializeID {
				t.Fatalf("expected initialize id %q, got %q", relayBootstrapInitializeID, got)
			}
			params, _ := frames[0]["params"].(map[string]any)
			capabilities, _ := params["capabilities"].(map[string]any)
			if experimental, _ := capabilities["experimentalApi"].(bool); !experimental {
				t.Fatalf("expected experimentalApi=true, got %#v", capabilities["experimentalApi"])
			}
			methods, _ := capabilities["optOutNotificationMethods"].([]any)
			if len(methods) != 1 || methods[0] != "item/agentMessage/delta" {
				t.Fatalf("unexpected optOutNotificationMethods: %#v", capabilities["optOutNotificationMethods"])
			}
			if got := lookupStringFromMap(frames[1], "method"); got != "initialized" {
				t.Fatalf("expected second frame to be initialized, got %q", got)
			}
			if got := lookupStringFromMap(frames[2], "method"); got != "config/read" {
				t.Fatalf("expected third frame to be config/read, got %q", got)
			}
			if got := lookupStringFromMap(frames[2], "id"); got != bootstrapConfigReadIDForTest {
				t.Fatalf("expected config/read id %q, got %q", bootstrapConfigReadIDForTest, got)
			}
			configParams, _ := frames[2]["params"].(map[string]any)
			if configParams["cwd"] != "/tmp/project" || configParams["includeLayers"] != false {
				t.Fatalf("unexpected config/read params: %#v", configParams)
			}

			remaining, err := io.ReadAll(replayedStdout)
			if err != nil {
				t.Fatalf("read replayed stdout: %v", err)
			}
			if string(remaining) != bufferedBeforeInitialize+bufferedWhileWaitingForConfig {
				t.Fatalf("expected buffered stdout to be replayed, got %q", string(remaining))
			}

			if _, err := app.runtime.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"thread-current","cwd":"/tmp/project"}}`)); err != nil {
				t.Fatalf("observe current thread: %v", err)
			}
			translated, err := app.runtime.TranslateCommand(agentproto.Command{
				Kind:      agentproto.CommandPromptSend,
				Origin:    agentproto.Origin{ChatID: "surface-1"},
				Target:    agentproto.Target{ThreadID: "thread-current", CWD: "/tmp/project"},
				Prompt:    agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}}},
				Overrides: agentproto.PromptOverrides{PlanMode: "off"},
			})
			if err != nil {
				t.Fatalf("translate prompt using bootstrap config: %v", err)
			}
			if len(translated.Phases) != 1 || len(translated.Phases[0].OutboundToChild) != 1 {
				t.Fatalf("unexpected translated command: %#v", translated)
			}
			turnParams := decodeCommandParamsForWrapperTest(t, translated.Phases[0].OutboundToChild[0])
			collaborationMode, _ := turnParams["collaborationMode"].(map[string]any)
			settings, _ := collaborationMode["settings"].(map[string]any)
			if turnParams["model"] != "gpt-effective" || settings["model"] != "gpt-effective" {
				t.Fatalf("bootstrap model was not applied to turn/start: %#v", turnParams)
			}
		})
	}
}

func TestBootstrapHeadlessCodexFailsWhenInitializeRejected(t *testing.T) {
	for _, source := range []string{"headless", "cron"} {
		t.Run(source, func(t *testing.T) {
			app := New(Config{
				Source:  source,
				Version: "test",
			})

			var childStdin bytes.Buffer
			_, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(mustJSONLine(t, map[string]any{
				"id": relayBootstrapInitializeID,
				"error": map[string]any{
					"message": "Not initialized",
				},
			})), nil, nil)
			if err == nil {
				t.Fatal("expected bootstrap to fail when initialize is rejected")
			}
			if !strings.Contains(err.Error(), "Not initialized") {
				t.Fatalf("expected initialize rejection in error, got %v", err)
			}
		})
	}
}

func TestBootstrapHeadlessCodexFailsWhenConfigReadRejected(t *testing.T) {
	app := New(Config{Source: "headless", Version: "test", WorkspaceRoot: "/tmp/project"})
	initializeResponse := mustJSONLine(t, map[string]any{
		"id":     relayBootstrapInitializeID,
		"result": map[string]any{"userAgent": "mockcodex/0.0.1"},
	})
	configReadResponse := mustJSONLine(t, map[string]any{
		"id": bootstrapConfigReadIDForTest,
		"error": map[string]any{
			"message": "config unavailable",
		},
	})

	var childStdin bytes.Buffer
	_, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(initializeResponse+configReadResponse), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "config unavailable") {
		t.Fatalf("expected config/read rejection, got %v", err)
	}
}

func TestBootstrapHeadlessCodexReplacesOrClearsPreviousModel(t *testing.T) {
	tests := []struct {
		name            string
		secondConfig    map[string]any
		wantSecondModel string
		wantSecondError bool
	}{
		{name: "replaces model", secondConfig: map[string]any{"model": "gpt-second"}, wantSecondModel: "gpt-second"},
		{name: "clears missing model", secondConfig: map[string]any{}, wantSecondError: true},
		{name: "clears null model", secondConfig: map[string]any{"model": nil}, wantSecondError: true},
		{name: "clears empty model", secondConfig: map[string]any{"model": ""}, wantSecondError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := New(Config{Source: "headless", Version: "test", WorkspaceRoot: "/tmp/project"})
			bootstrapHeadlessModelForTest(t, app, map[string]any{"model": "gpt-first"})
			if got, err := translateBootstrapModelForTest(t, app, "thread-first"); err != nil || got != "gpt-first" {
				t.Fatalf("first bootstrap model = %q, err=%v; want gpt-first", got, err)
			}

			bootstrapHeadlessModelForTest(t, app, tt.secondConfig)
			got, err := translateBootstrapModelForTest(t, app, "thread-second")
			if tt.wantSecondError {
				if err == nil || !strings.Contains(err.Error(), "requires a model") {
					t.Fatalf("expected cleared model to fail closed, got model=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != tt.wantSecondModel {
				t.Fatalf("second bootstrap model = %q, err=%v; want %q", got, err, tt.wantSecondModel)
			}
		})
	}
}

func TestBootstrapHeadlessCodexFailsWhenConfigReadResultMissing(t *testing.T) {
	app := New(Config{Source: "headless", Version: "test", WorkspaceRoot: "/tmp/project"})
	input := bootstrapInitializeResponseForTest(t) + mustJSONLine(t, map[string]any{
		"id": bootstrapConfigReadIDForTest,
	})

	var childStdin bytes.Buffer
	_, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(input), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "missing result") {
		t.Fatalf("expected missing config/read result error, got %v", err)
	}
}

func TestBootstrapHeadlessCodexFailsWhenConfigReadResponseMissingBeforeEOF(t *testing.T) {
	app := New(Config{Source: "headless", Version: "test", WorkspaceRoot: "/tmp/project"})

	var childStdin bytes.Buffer
	_, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(bootstrapInitializeResponseForTest(t)), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "config/read response") {
		t.Fatalf("expected config/read EOF error, got %v", err)
	}
}

func TestSyntheticInitializeFrameSkipsNonHeadless(t *testing.T) {
	app := New(Config{
		Source:  "vscode",
		Version: "test",
	})

	frame, err := app.syntheticInitializeFrame()
	if err != nil {
		t.Fatalf("syntheticInitializeFrame: %v", err)
	}
	if len(frame) != 0 {
		t.Fatalf("expected no initialize frame for non-headless source, got %#v", string(frame))
	}
}

func TestNeedsSyntheticBootstrap(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "headless source",
			cfg:  Config{Source: "headless"},
			want: true,
		},
		{
			name: "cron source",
			cfg:  Config{Source: "cron"},
			want: true,
		},
		{
			name: "vscode source",
			cfg:  Config{Source: "vscode"},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			app := New(tt.cfg)
			if got := app.needsSyntheticBootstrap(); got != tt.want {
				t.Fatalf("needsSyntheticBootstrap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func decodeJSONLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	frames := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("unmarshal json line %q: %v", line, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func bootstrapHeadlessModelForTest(t *testing.T, app *App, config map[string]any) {
	t.Helper()
	input := bootstrapInitializeResponseForTest(t) + mustJSONLine(t, map[string]any{
		"id": bootstrapConfigReadIDForTest,
		"result": map[string]any{
			"config": config,
		},
	})
	var childStdin bytes.Buffer
	if _, err := app.bootstrapHeadlessCodex(&childStdin, strings.NewReader(input), nil, nil); err != nil {
		t.Fatalf("bootstrap headless model: %v", err)
	}
}

func bootstrapInitializeResponseForTest(t *testing.T) string {
	t.Helper()
	return mustJSONLine(t, map[string]any{
		"id":     relayBootstrapInitializeID,
		"result": map[string]any{"userAgent": "mockcodex/0.0.1"},
	})
}

func translateBootstrapModelForTest(t *testing.T, app *App, threadID string) (string, error) {
	t.Helper()
	if _, err := app.runtime.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"` + threadID + `","cwd":"/tmp/project"}}`)); err != nil {
		return "", err
	}
	translated, err := app.runtime.TranslateCommand(agentproto.Command{
		Kind:      agentproto.CommandPromptSend,
		Origin:    agentproto.Origin{ChatID: "surface-1"},
		Target:    agentproto.Target{ThreadID: threadID, CWD: "/tmp/project"},
		Prompt:    agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}}},
		Overrides: agentproto.PromptOverrides{PlanMode: "off"},
	})
	if err != nil {
		return "", err
	}
	params := decodeCommandParamsForWrapperTest(t, translated.Phases[0].OutboundToChild[0])
	return lookupStringFromMap(params, "model"), nil
}

func decodeCommandParamsForWrapperTest(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var command map[string]any
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatalf("unmarshal command: %v", err)
	}
	params, _ := command["params"].(map[string]any)
	return params
}
