package wecom

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

func TestProjectTimelineTextRendersTextFrame(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TimelineTextPayload{
			TimelineText: control.TimelineText{Text: "  hello world  "},
		},
	})
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	if frames[0].MsgType != "text" {
		t.Fatalf("expected text frame, got %q", frames[0].MsgType)
	}
	if frames[0].Text == nil || frames[0].Text.Content != "hello world" {
		t.Fatalf("unexpected text body: %+v", frames[0].Text)
	}
}

func TestProjectFinalCodeBlockRendersMarkdownFrame(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.BlockCommittedPayload{
			Block: render.Block{Kind: render.BlockAssistantCode, Language: "go", Text: "fmt.Println(1)", Final: true},
		},
	})
	if len(frames) != 1 || frames[0].MsgType != "markdown" {
		t.Fatalf("expected 1 markdown frame, got %+v", frames)
	}
	want := "```go\nfmt.Println(1)\n```"
	if frames[0].Markdown == nil || frames[0].Markdown.Content != want {
		t.Fatalf("unexpected markdown body: %+v", frames[0].Markdown)
	}
}

func TestProjectNonFinalBlockRendersText(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.BlockCommittedPayload{
			Block: render.Block{Kind: render.BlockAssistantMarkdown, Text: "streaming chunk", Final: false},
		},
	})
	if len(frames) != 1 || frames[0].MsgType != "text" {
		t.Fatalf("expected text frame, got %+v", frames)
	}
}

func TestProjectTargetPickerButtonsWhenSingleDimensionFits(t *testing.T) {
	view := control.FeishuTargetPickerView{
		PickerID:            "picker-1",
		Title:               "选择工作区",
		ShowWorkspaceSelect: true,
		WorkspaceOptions: []control.FeishuTargetPickerWorkspaceOption{
			{Value: "/data/web", Label: "web"},
			{Value: "/data/api", Label: "api"},
		},
		ConfirmLabel: "确认",
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame (no body), got %d", len(frames))
	}
	card := frames[0].TemplateCard
	if card == nil || card.CardType != cardTypeButtonInteraction {
		t.Fatalf("expected button_interaction card, got %+v", card)
	}
	if card.TaskID != "picker-1" {
		t.Fatalf("expected task_id=picker-1, got %q", card.TaskID)
	}
	if len(card.ButtonList) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(card.ButtonList))
	}
	if card.ButtonList[0].Key != keyPrefixTargetWorkspace+keyValueSep+"/data/web" {
		t.Fatalf("unexpected first button key: %q", card.ButtonList[0].Key)
	}
	if card.ButtonList[1].Key != keyPrefixTargetWorkspace+keyValueSep+"/data/api" {
		t.Fatalf("unexpected second button key: %q", card.ButtonList[1].Key)
	}
}

func TestProjectTargetPickerDropdownsWhenBothDimensions(t *testing.T) {
	view := control.FeishuTargetPickerView{
		PickerID:            "picker-2",
		Title:               "选择工作区与会话",
		Question:            "请选择目标",
		ShowWorkspaceSelect: true,
		ShowSessionSelect:   true,
		WorkspaceOptions: []control.FeishuTargetPickerWorkspaceOption{
			{Value: "/data/web", Label: "web"},
		},
		SessionOptions: []control.FeishuTargetPickerSessionOption{
			{Value: "thread-a", Label: "会话 A"},
			{Value: "thread-b", Label: "会话 B"},
		},
		ConfirmLabel: "开始",
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	// Body (question) frame first, then the interactive card.
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames (stream body + card), got %d", len(frames))
	}
	if frames[0].MsgType != "markdown" || !frames[0].Stream {
		t.Fatalf("expected first frame to be a stream markdown body, got %+v", frames[0])
	}
	card := frames[1].TemplateCard
	if card == nil || card.CardType != cardTypeMultipleInteraction {
		t.Fatalf("expected multiple_interaction card, got %+v", card)
	}
	if len(card.SelectList) != 2 {
		t.Fatalf("expected 2 dropdowns, got %d", len(card.SelectList))
	}
	if card.SelectList[0].QuestionKey != questionKeyWorkspace {
		t.Fatalf("unexpected workspace question key: %q", card.SelectList[0].QuestionKey)
	}
	if card.SelectList[1].QuestionKey != questionKeySession {
		t.Fatalf("unexpected session question key: %q", card.SelectList[1].QuestionKey)
	}
	if got := card.SelectList[1].OptionList; len(got) != 2 || got[0].ID != "thread-a" || got[1].ID != "thread-b" {
		t.Fatalf("unexpected session option ids: %+v", got)
	}
	if len(card.ButtonList) != 0 {
		t.Fatalf("multiple_interaction must not use button_list, got %+v", card.ButtonList)
	}
	if card.SubmitButton == nil {
		t.Fatal("expected submit_button")
	}
	if card.SubmitButton.Text != "开始" || card.SubmitButton.Key != keyTargetConfirm+keyValueSep+"picker-2" {
		t.Fatalf("unexpected submit button: %+v", card.SubmitButton)
	}
}

func TestProjectTargetPickerDropdownsWhenSingleDimensionExceedsButtonBudget(t *testing.T) {
	opts := make([]control.FeishuTargetPickerWorkspaceOption, 0, defaultMaxButtons+1)
	for i := 0; i < defaultMaxButtons+1; i++ {
		opts = append(opts, control.FeishuTargetPickerWorkspaceOption{Value: string(rune('a' + i)), Label: "opt"})
	}
	view := control.FeishuTargetPickerView{
		PickerID:            "picker-3",
		ShowWorkspaceSelect: true,
		WorkspaceOptions:    opts,
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	card := frames[len(frames)-1].TemplateCard
	if card == nil || card.CardType != cardTypeMultipleInteraction {
		t.Fatalf("expected multiple_interaction fallback, got %+v", card)
	}
	if card.SubmitButton == nil || card.SubmitButton.Key != keyTargetConfirm+keyValueSep+"picker-3" {
		t.Fatalf("expected submit button on multiple_interaction fallback, got %+v", card.SubmitButton)
	}
}

func TestProjectTargetPickerDropdownOptionsRespectWeComLimit(t *testing.T) {
	opts := make([]control.FeishuTargetPickerSessionOption, 0, maxSelectOptions+3)
	for i := 0; i < maxSelectOptions+3; i++ {
		opts = append(opts, control.FeishuTargetPickerSessionOption{Value: string(rune('a' + i)), Label: "会话"})
	}
	view := control.FeishuTargetPickerView{
		PickerID:          "picker-limit",
		ShowSessionSelect: true,
		SessionOptions:    opts,
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	card := frames[len(frames)-1].TemplateCard
	if card == nil || card.CardType != cardTypeMultipleInteraction {
		t.Fatalf("expected multiple_interaction fallback, got %+v", card)
	}
	if got := len(card.SelectList[0].OptionList); got != maxSelectOptions {
		t.Fatalf("option_list size = %d, want %d", got, maxSelectOptions)
	}
}

func TestProjectTargetPickerDropdownDoesNotDefaultSelectedID(t *testing.T) {
	opts := make([]control.FeishuTargetPickerWorkspaceOption, 0, defaultMaxButtons+1)
	for i := 0; i < defaultMaxButtons+1; i++ {
		opts = append(opts, control.FeishuTargetPickerWorkspaceOption{Value: string(rune('a' + i)), Label: "工作区"})
	}
	view := control.FeishuTargetPickerView{
		PickerID:            "picker-default",
		ShowWorkspaceSelect: true,
		WorkspaceOptions:    opts,
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	card := frames[len(frames)-1].TemplateCard
	if card == nil || len(card.SelectList) != 1 {
		t.Fatalf("expected one dropdown card, got %+v", card)
	}
	if got := card.SelectList[0].SelectedID; got != "" {
		t.Fatalf("selected_id = %q, want empty so WeCom does not auto-submit the first option", got)
	}
}

func TestProjectTargetPickerDropdownKeepsExplicitSelectedID(t *testing.T) {
	opts := make([]control.FeishuTargetPickerWorkspaceOption, 0, defaultMaxButtons+1)
	for i := 0; i < defaultMaxButtons+1; i++ {
		opts = append(opts, control.FeishuTargetPickerWorkspaceOption{Value: string(rune('a' + i)), Label: "工作区"})
	}
	view := control.FeishuTargetPickerView{
		PickerID:             "picker-selected",
		ShowWorkspaceSelect:  true,
		SelectedWorkspaceKey: "c",
		WorkspaceOptions:     opts,
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: view},
	})
	card := frames[len(frames)-1].TemplateCard
	if card == nil || len(card.SelectList) != 1 {
		t.Fatalf("expected one dropdown card, got %+v", card)
	}
	if got := card.SelectList[0].SelectedID; got != "c" {
		t.Fatalf("selected_id = %q, want c", got)
	}
}

func TestProjectTargetPickerSucceededRendersStatusMarkdown(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.TargetPickerPayload{View: control.FeishuTargetPickerView{
			Stage:               control.FeishuTargetPickerStageSucceeded,
			StatusTitle:         "已进入新会话待命",
			StatusText:          "当前工作目标已经准备完成，下一条文本会直接开启新会话。",
			ShowWorkspaceSelect: true,
			WorkspaceOptions: []control.FeishuTargetPickerWorkspaceOption{
				{Value: "/data/web", Label: "web"},
			},
		}},
	})
	if len(frames) != 1 {
		t.Fatalf("expected one status frame, got %d", len(frames))
	}
	if frames[0].MsgType != "markdown" || frames[0].Markdown == nil {
		t.Fatalf("expected markdown status frame, got %+v", frames[0])
	}
	if frames[0].TemplateCard != nil {
		t.Fatalf("succeeded picker must not render an actionable card, got %+v", frames[0].TemplateCard)
	}
	if !strings.Contains(frames[0].Markdown.Content, "已进入新会话待命") || !strings.Contains(frames[0].Markdown.Content, "下一条文本会直接开启新会话") {
		t.Fatalf("unexpected status markdown: %q", frames[0].Markdown.Content)
	}
}

func TestProjectRequestApproveRejectButtons(t *testing.T) {
	view := control.FeishuRequestView{
		RequestID:       "req-1",
		Title:           "是否执行计划?",
		RequestRevision: 3,
		Options: []control.RequestPromptOption{
			{OptionID: "approve", Label: "批准", Style: "primary"},
			{OptionID: "reject", Label: "拒绝"},
		},
	}
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) == 0 {
		t.Fatal("expected at least the interactive card frame")
	}
	card := frames[len(frames)-1].TemplateCard
	if card == nil || card.CardType != cardTypeButtonInteraction {
		t.Fatalf("expected button_interaction card, got %+v", card)
	}
	if card.TaskID != "req-1" {
		t.Fatalf("expected task_id=req-1, got %q", card.TaskID)
	}
	if len(card.ButtonList) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(card.ButtonList))
	}
	if card.ButtonList[0].Key != composeEncodedKey(keyPrefixRequestRespond, "3", "approve") {
		t.Fatalf("unexpected approve key: %q", card.ButtonList[0].Key)
	}
	if card.ButtonList[1].Key != composeEncodedKey(keyPrefixRequestRespond, "3", "reject") {
		t.Fatalf("unexpected reject key: %q", card.ButtonList[1].Key)
	}
	if card.ButtonList[0].Style != 1 {
		t.Fatalf("expected primary style on approve button, got %d", card.ButtonList[0].Style)
	}
}

func TestProjectRequestUserInputDirectOptionsRenderChoiceAndControlCards(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-ui-1",
		RequestType:     "request_user_input",
		RequestRevision: 2,
		Title:           "需要补充输入",
		Questions: []control.RequestPromptQuestion{
			{
				ID:             "model",
				Header:         "模型",
				Question:       "请选择模型",
				Options:        []control.RequestPromptQuestionOption{{Label: "gpt-5.4"}, {Label: "gpt-5.3"}},
				DirectResponse: true,
			},
			{
				ID:       "notes",
				Header:   "备注",
				Question: "补充说明",
				Optional: true,
			},
		},
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 3 {
		t.Fatalf("expected body + choice card + control card, got %d", len(frames))
	}
	if frames[0].MsgType != "markdown" || !frames[0].Stream {
		t.Fatalf("expected first frame to be stream markdown, got %+v", frames[0])
	}
	choiceCard := frames[1].TemplateCard
	if choiceCard == nil || choiceCard.CardType != cardTypeButtonInteraction {
		t.Fatalf("expected button_interaction choice card, got %+v", choiceCard)
	}
	if len(choiceCard.ButtonList) != 2 {
		t.Fatalf("expected 2 choice buttons, got %d", len(choiceCard.ButtonList))
	}
	if choiceCard.ButtonList[0].Key != composeEncodedKey(keyPrefixRequestAnswer, "2", "model", "gpt-5.4") {
		t.Fatalf("unexpected option key: %q", choiceCard.ButtonList[0].Key)
	}
	controlCard := frames[2].TemplateCard
	if controlCard == nil || controlCard.CardType != cardTypeButtonInteraction {
		t.Fatalf("expected control card, got %+v", controlCard)
	}
	if len(controlCard.ButtonList) != 1 || controlCard.ButtonList[0].Text != "取消" {
		t.Fatalf("expected only cancel button on first required question, got %+v", controlCard.ButtonList)
	}
}

func TestProjectRequestUserInputDropdownAndControlButtons(t *testing.T) {
	options := make([]control.RequestPromptQuestionOption, 0, defaultMaxButtons+2)
	for i := 0; i < defaultMaxButtons+2; i++ {
		options = append(options, control.RequestPromptQuestionOption{Label: "opt-" + string(rune('a'+i))})
	}
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-ui-2",
		RequestType:     "request_user_input",
		RequestRevision: 4,
		Questions: []control.RequestPromptQuestion{
			{ID: "done", Header: "前题", Question: "done", Answered: true},
			{
				ID:             "pick",
				Header:         "选择",
				Question:       "请选择",
				Options:        options,
				Optional:       true,
				DirectResponse: true,
			},
			{ID: "later", Header: "后题", Question: "later"},
		},
		CurrentQuestionIndex: 1,
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 3 {
		t.Fatalf("expected body + dropdown card + control card, got %d", len(frames))
	}
	choiceCard := frames[1].TemplateCard
	if choiceCard == nil || choiceCard.CardType != cardTypeMultipleInteraction {
		t.Fatalf("expected dropdown card, got %+v", choiceCard)
	}
	if choiceCard.SubmitButton == nil || choiceCard.SubmitButton.Key != composeEncodedKey(keyPrefixRequestAnswerSubmit, "4", "pick") {
		t.Fatalf("unexpected dropdown submit button: %+v", choiceCard.SubmitButton)
	}
	if len(choiceCard.SelectList) != 1 || choiceCard.SelectList[0].QuestionKey != requestAnswerQuestionKey("pick") {
		t.Fatalf("unexpected dropdown select list: %+v", choiceCard.SelectList)
	}
	controlCard := frames[2].TemplateCard
	if controlCard == nil {
		t.Fatal("expected control card")
	}
	wantKeys := []string{
		composeEncodedKey(keyPrefixRequestRespond, "4", frontstagecontract.RequestPromptOptionStepPrevious),
		composeEncodedKey(keyPrefixRequestControl, "4", frontstagecontract.RequestControlSkipOptional, "pick"),
		composeEncodedKey(keyPrefixRequestControl, "4", frontstagecontract.RequestControlCancelTurn),
	}
	for i, want := range wantKeys {
		if len(controlCard.ButtonList) <= i || controlCard.ButtonList[i].Key != want {
			t.Fatalf("button[%d] = %+v, want key %q", i, controlCard.ButtonList, want)
		}
	}
}

func TestProjectRequestUserInputCompletedRendersRetrySubmit(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-ui-3",
		RequestType:     "request_user_input",
		RequestRevision: 5,
		Questions: []control.RequestPromptQuestion{
			{ID: "model", Header: "模型", Question: "请选择模型", Answered: true},
			{ID: "notes", Header: "备注", Question: "补充说明", Skipped: true, Optional: true},
		},
		CurrentQuestionIndex: 1,
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 2 {
		t.Fatalf("expected body + control card, got %d", len(frames))
	}
	controlCard := frames[1].TemplateCard
	if controlCard == nil || len(controlCard.ButtonList) < 2 {
		t.Fatalf("expected retry submit plus navigation/cancel, got %+v", controlCard)
	}
	found := false
	for _, button := range controlCard.ButtonList {
		if button.Key == composeEncodedKey(keyPrefixRequestSubmit, "5") && button.Text == "重新提交" && button.Style == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected retry submit button, got %+v", controlCard.ButtonList)
	}
}

func TestProjectRequestUserInputUnsupportedTextAnswerShowsGuidance(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-ui-4",
		RequestType:     "request_user_input",
		RequestRevision: 1,
		Questions: []control.RequestPromptQuestion{
			{
				ID:           "notes",
				Header:       "备注",
				Question:     "请补充说明",
				AllowOther:   true,
				Placeholder:  "输入说明",
				DefaultValue: "已有草稿",
			},
		},
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 2 {
		t.Fatalf("expected body + control card, got %d", len(frames))
	}
	if frames[0].Markdown == nil || !strings.Contains(frames[0].Markdown.Content, "支持直接发送一条企微文本继续回答") {
		t.Fatalf("expected free-text guidance in body, got %+v", frames[0].Markdown)
	}
	controlCard := frames[1].TemplateCard
	if controlCard == nil || len(controlCard.ButtonList) != 1 || controlCard.ButtonList[0].Text != "取消" {
		t.Fatalf("expected only cancel control card, got %+v", controlCard)
	}
}

func TestProjectRequestSecretTextAnswerSuggestsFeishuForSensitiveInput(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-ui-5",
		RequestType:     "request_user_input",
		RequestRevision: 1,
		Questions: []control.RequestPromptQuestion{
			{
				ID:       "token",
				Header:   "访问令牌",
				Question: "请输入 token",
				Secret:   true,
			},
		},
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 2 {
		t.Fatalf("expected body + control card, got %d", len(frames))
	}
	if frames[0].Markdown == nil || !strings.Contains(frames[0].Markdown.Content, "仍建议切换到飞书处理") {
		t.Fatalf("expected secret-input guidance in body, got %+v", frames[0].Markdown)
	}
}

func TestProjectRequestStructuredFormShowsGuidanceAndInteractiveControls(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-plan-1",
		RequestType:     "approval",
		RequestRevision: 6,
		StructuredForm: &control.RequestPromptStructuredForm{
			SubmitLabel: "提交",
			Fields: []control.RequestPromptFormField{
				{
					Name:  "scope",
					Label: "授权范围",
					Kind:  control.RequestPromptFormFieldSelectStatic,
					Options: []control.RequestPromptFormFieldOption{
						{Label: "仅当前目录", Value: "dir"},
						{Label: "整个工作区", Value: "workspace"},
					},
				},
			},
		},
		Options: []control.RequestPromptOption{
			{OptionID: frontstagecontract.RequestPromptOptionStepPrevious, Label: "返回"},
			{OptionID: "decline", Label: "拒绝"},
		},
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 3 {
		t.Fatalf("expected body + input card + control card, got %d", len(frames))
	}
	if frames[0].Markdown == nil || !strings.Contains(frames[0].Markdown.Content, "当前企微已支持按字段逐步填写当前结构化表单") {
		t.Fatalf("expected structured-form guidance, got %+v", frames[0].Markdown)
	}
	inputCard := frames[1].TemplateCard
	if inputCard == nil || inputCard.CardType != cardTypeMultipleInteraction {
		t.Fatalf("expected interactive structured-form card, got %+v", inputCard)
	}
	if inputCard.SubmitButton == nil || inputCard.SubmitButton.Key != composeEncodedKey(keyPrefixRequestAnswerSubmit, "6", "scope") {
		t.Fatalf("unexpected structured-form submit button: %+v", inputCard.SubmitButton)
	}
	controlCard := frames[2].TemplateCard
	if controlCard == nil || controlCard.CardType != cardTypeButtonInteraction {
		t.Fatalf("expected structured-form control card, got %+v", controlCard)
	}
}

func TestProjectRequestStructuredFormTextFieldShowsReplyGuidanceAndControls(t *testing.T) {
	view := control.NormalizeFeishuRequestView(control.FeishuRequestView{
		RequestID:       "req-form-text",
		RequestType:     "approval",
		RequestRevision: 7,
		StructuredForm: &control.RequestPromptStructuredForm{
			SubmitLabel: "提交",
			Fields: []control.RequestPromptFormField{
				{Name: "summary", Label: "摘要", Kind: control.RequestPromptFormFieldText, Placeholder: "输入摘要"},
				{Name: "scope", Label: "范围", Kind: control.RequestPromptFormFieldSelectStatic, Options: []control.RequestPromptFormFieldOption{{Label: "仅当前目录", Value: "dir"}}},
			},
		},
	})
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.RequestPayload{View: view},
	})
	if len(frames) != 1 {
		t.Fatalf("expected body-only guidance for pending text field, got %d", len(frames))
	}
	if frames[0].Markdown == nil || !strings.Contains(frames[0].Markdown.Content, "当前字段支持直接发送一条企微文本继续填写") {
		t.Fatalf("expected free-text structured-field guidance, got %+v", frames[0].Markdown)
	}
}

func TestProjectPlanUpdateMarkdown(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.PlanUpdatePayload{
			PlanUpdate: control.PlanUpdate{
				Steps: []control.PlanUpdateStep{
					{Step: "done step", Status: agentproto.TurnPlanStepStatusCompleted},
					{Step: "active step", Status: agentproto.TurnPlanStepStatusInProgress},
					{Step: "todo step", Status: agentproto.TurnPlanStepStatusPending},
				},
			},
		},
	})
	if len(frames) != 1 || frames[0].MsgType != "markdown" {
		t.Fatalf("expected 1 markdown frame, got %+v", frames)
	}
	got := frames[0].Markdown.Content
	for _, want := range []string{"[x] done step", "[~] active step", "[ ] todo step"} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown %q missing %q", got, want)
		}
	}
}

func TestProjectImageOutputRendersLocalImageFrame(t *testing.T) {
	frames := NewProjector().ProjectEvent(eventcontract.Event{
		Payload: eventcontract.ImageOutputPayload{ImageOutput: control.ImageOutput{SavedPath: "/tmp/x.png"}},
	})
	if len(frames) != 1 {
		t.Fatalf("expected one image frame, got %d", len(frames))
	}
	if frames[0].MsgType != "image" || frames[0].LocalPath != "/tmp/x.png" {
		t.Fatalf("unexpected image frame: %+v", frames[0])
	}
}
