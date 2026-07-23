package codex

import (
	"encoding/json"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestTranslatePromptSendDoesNotInjectResolvedDefaultIntoThreadStart(t *testing.T) {
	tr := NewTranslator("inst-1")
	tr.SetDefaultModel("gpt-config-default")

	commands, err := tr.TranslateCommand(planModePromptCommand("", "/tmp/project"))
	if err != nil {
		t.Fatalf("translate command: %v", err)
	}
	params := decodeCommandParams(t, commands[0])
	if model := lookupStringFromAny(params["model"]); model != "" {
		t.Fatalf("bootstrap default leaked into thread/start model: %q", model)
	}
}

func TestObserveServerThreadStartResultModelEnablesFollowupTurn(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.TranslateCommand(planModePromptCommand("", "/tmp/project")); err != nil {
		t.Fatalf("translate command: %v", err)
	}

	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-start-0","result":{"thread":{"id":"thread-created"},"model":"gpt-thread-resolved"}}`))
	if err != nil {
		t.Fatalf("observe thread/start response: %v", err)
	}
	if !result.Suppress || len(result.OutboundToCodex) != 1 {
		t.Fatalf("expected suppressed followup turn/start, got %#v", result)
	}
	assertTurnStartModel(t, result.OutboundToCodex[0], "gpt-thread-resolved")
}

func TestObserveServerThreadResultModelWinsOverResolvedDefault(t *testing.T) {
	tr := NewTranslator("inst-1")
	tr.SetDefaultModel("gpt-config-default")
	if _, err := tr.TranslateCommand(planModePromptCommand("", "/tmp/project")); err != nil {
		t.Fatalf("translate command: %v", err)
	}

	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-start-0","result":{"thread":{"id":"thread-created"},"model":"gpt-thread-resolved"}}`))
	if err != nil {
		t.Fatalf("observe thread/start response: %v", err)
	}
	if len(result.OutboundToCodex) != 1 {
		t.Fatalf("expected followup turn/start, got %#v", result)
	}
	assertTurnStartModel(t, result.OutboundToCodex[0], "gpt-thread-resolved")
}

func TestObserveServerThreadResumeResultModelEnablesFollowupTurn(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project")); err != nil {
		t.Fatalf("translate command: %v", err)
	}

	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-resume-0","result":{"thread":{"id":"thread-existing"},"model":"gpt-resume-resolved"}}`))
	if err != nil {
		t.Fatalf("observe thread/resume response: %v", err)
	}
	if !result.Suppress || len(result.OutboundToCodex) != 1 {
		t.Fatalf("expected suppressed followup turn/start, got %#v", result)
	}
	assertTurnStartModel(t, result.OutboundToCodex[0], "gpt-resume-resolved")
}

func TestObserveServerThreadResumeResultModelWinsOverAnotherThreadTemplate(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"thread-a","cwd":"/tmp/project","model":"gpt-thread-a","collaborationMode":{"mode":"default","settings":{"model":"gpt-thread-a","reasoning_effort":"medium","developer_instructions":null}}}}`)); err != nil {
		t.Fatalf("observe thread A turn/start: %v", err)
	}

	if _, err := tr.TranslateCommand(planModePromptCommand("thread-b", "/tmp/project")); err != nil {
		t.Fatalf("translate prompt for thread B: %v", err)
	}
	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-resume-0","result":{"thread":{"id":"thread-b"},"model":"gpt-thread-b"}}`))
	if err != nil {
		t.Fatalf("observe thread B resume response: %v", err)
	}
	if !result.Suppress || len(result.OutboundToCodex) != 1 {
		t.Fatalf("expected suppressed followup turn/start, got %#v", result)
	}
	assertTurnStartModel(t, result.OutboundToCodex[0], "gpt-thread-b")
}

func TestObserveServerChildRestartResumeResultModelIsReused(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.ObserveClient([]byte(`{"method":"thread/resume","params":{"threadId":"thread-existing","cwd":"/tmp/project"}}`)); err != nil {
		t.Fatalf("observe local thread/resume: %v", err)
	}
	_, requestID, ok, err := tr.BuildChildRestartRestoreFrame("restart-1")
	if err != nil || !ok {
		t.Fatalf("build restart restore frame: ok=%t err=%v", ok, err)
	}
	if _, err := tr.ObserveServer([]byte(`{"id":"` + requestID + `","result":{"thread":{"id":"thread-existing"},"model":"gpt-restored-resolved"}}`)); err != nil {
		t.Fatalf("observe restart resume response: %v", err)
	}

	commands, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project"))
	if err != nil {
		t.Fatalf("translate prompt after restart restore: %v", err)
	}
	assertTurnStartModel(t, commands[0], "gpt-restored-resolved")
}

func TestObserveServerChildRestartResumeResultModelReplacesOldThreadTemplate(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"thread-existing","cwd":"/tmp/project","model":"gpt-before-restart","collaborationMode":{"mode":"default","settings":{"model":"gpt-before-restart","reasoning_effort":"medium","developer_instructions":null}}}}`)); err != nil {
		t.Fatalf("observe pre-restart turn/start: %v", err)
	}
	_, requestID, ok, err := tr.BuildChildRestartRestoreFrame("restart-1")
	if err != nil || !ok {
		t.Fatalf("build restart restore frame: ok=%t err=%v", ok, err)
	}
	if _, err := tr.ObserveServer([]byte(`{"id":"` + requestID + `","result":{"thread":{"id":"thread-existing"},"model":"gpt-after-restart"}}`)); err != nil {
		t.Fatalf("observe restart resume response: %v", err)
	}

	commands, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project"))
	if err != nil {
		t.Fatalf("translate prompt after restart restore: %v", err)
	}
	assertTurnStartModel(t, commands[0], "gpt-after-restart")
}

func TestObserveClientTargetThreadModelReplacesEarlierResumeModel(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project")); err != nil {
		t.Fatalf("translate prompt requiring resume: %v", err)
	}
	if _, err := tr.ObserveServer([]byte(`{"id":"relay-thread-resume-0","result":{"thread":{"id":"thread-existing"},"model":"gpt-resume-old"}}`)); err != nil {
		t.Fatalf("observe earlier resume response: %v", err)
	}
	if _, err := tr.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"thread-existing","cwd":"/tmp/project","model":"gpt-local-new","collaborationMode":{"mode":"default","settings":{"model":"gpt-local-new","reasoning_effort":"medium","developer_instructions":null}}}}`)); err != nil {
		t.Fatalf("observe newer local turn/start: %v", err)
	}

	commands, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project"))
	if err != nil {
		t.Fatalf("translate prompt after local model update: %v", err)
	}
	assertTurnStartModel(t, commands[0], "gpt-local-new")
}

func planModePromptCommand(threadID, cwd string) agentproto.Command {
	return agentproto.Command{
		Kind:      agentproto.CommandPromptSend,
		Origin:    agentproto.Origin{ChatID: "surface-1"},
		Target:    agentproto.Target{ThreadID: threadID, CWD: cwd},
		Prompt:    agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "hello"}}},
		Overrides: agentproto.PromptOverrides{PlanMode: "off"},
	}
}

func decodeCommandParams(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var command map[string]any
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatalf("unmarshal command: %v", err)
	}
	params, _ := command["params"].(map[string]any)
	return params
}

func assertTurnStartModel(t *testing.T, raw []byte, want string) {
	t.Helper()
	params := decodeCommandParams(t, raw)
	if got := lookupStringFromAny(params["model"]); got != want {
		t.Fatalf("turn/start model = %q, want %q; params=%#v", got, want, params)
	}
	collaborationMode, _ := params["collaborationMode"].(map[string]any)
	settings, _ := collaborationMode["settings"].(map[string]any)
	if got := lookupStringFromAny(settings["model"]); got != want {
		t.Fatalf("collaborationMode model = %q, want %q; params=%#v", got, want, params)
	}
}
