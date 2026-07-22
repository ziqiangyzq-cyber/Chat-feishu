package wecom

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestMapCardEventSelectWorkspace(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		Cmd:            frameCmdEventCallback,
		ChatID:         "chat-1",
		OperatorUserID: "user-9",
		MessageID:      "msg-1",
		TaskID:         "picker-1",
		EventKey:       keyPrefixTargetWorkspace + keyValueSep + "/data/web",
	})
	if !ok {
		t.Fatal("expected mapping to succeed")
	}
	if action.Kind != control.ActionTargetPickerSelectWorkspace {
		t.Fatalf("unexpected kind: %q", action.Kind)
	}
	if action.PickerID != "picker-1" || action.WorkspaceKey != "/data/web" {
		t.Fatalf("unexpected payload: pickerID=%q ws=%q", action.PickerID, action.WorkspaceKey)
	}
	if action.ChatID != "chat-1" || action.ActorUserID != "user-9" || action.MessageID != "msg-1" {
		t.Fatalf("unexpected inbound context: %+v", action)
	}
}

func TestMapCardEventSelectSession(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "picker-1",
		EventKey: keyPrefixTargetSession + keyValueSep + "thread-a",
	})
	if !ok || action.Kind != control.ActionTargetPickerSelectSession {
		t.Fatalf("unexpected result: ok=%v kind=%q", ok, action.Kind)
	}
	if action.PickerID != "picker-1" || action.TargetPickerValue != "thread-a" {
		t.Fatalf("unexpected payload: %+v", action)
	}
}

func TestMapCardEventConfirmRecoversSelections(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "tp-unique",
		EventKey: keyTargetConfirm + keyValueSep + "picker-2",
		Selections: []InboundSelection{
			{QuestionKey: questionKeyWorkspace, OptionIDs: []string{"/data/web"}},
			{QuestionKey: questionKeySession, OptionIDs: []string{"thread-b"}},
		},
	})
	if !ok || action.Kind != control.ActionTargetPickerConfirm {
		t.Fatalf("unexpected result: ok=%v kind=%q", ok, action.Kind)
	}
	if action.PickerID != "picker-2" {
		t.Fatalf("unexpected picker id: %q", action.PickerID)
	}
	if action.WorkspaceKey != "/data/web" || action.TargetPickerValue != "thread-b" {
		t.Fatalf("confirm did not recover selections: ws=%q sess=%q", action.WorkspaceKey, action.TargetPickerValue)
	}
}

func TestDecodeInboundCardEventNestedTemplateCardEvent(t *testing.T) {
	raw := []byte(`{
		"cmd":"aibot_event_callback",
		"headers":{"req_id":"req-1"},
		"body":{
			"msgid":"msg-1",
			"aibotid":"bot-1",
			"from":{"userid":"user-1"},
			"chatid":"chat-1",
			"chattype":"single",
			"msgtype":"event",
			"event":{
				"eventtype":"template_card_event",
				"template_card_event":{
					"card_type":"multiple_interaction",
					"event_key":"tp.confirm::picker-2",
					"task_id":"tp-unique",
					"selected_items":{
						"selected_item":[
							{
								"question_key":"tp.ws",
								"option_ids":{"option_id":["/data/web"]}
							},
							{
								"question_key":"tp.sess",
								"option_ids":{"option_id":["thread-b"]}
							}
						]
					}
				}
			}
		}
	}`)
	event, err := decodeInboundCardEvent(raw)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if event.Cmd != frameCmdEventCallback || event.Headers.ReqID != "req-1" {
		t.Fatalf("unexpected frame metadata: %+v", event)
	}
	if event.ChatID != "chat-1" || event.OperatorUserID != "user-1" || event.MessageID != "msg-1" {
		t.Fatalf("unexpected inbound context: %+v", event)
	}
	action, ok := MapCardEventToAction(event)
	if !ok || action.Kind != control.ActionTargetPickerConfirm {
		t.Fatalf("unexpected action: ok=%v action=%+v", ok, action)
	}
	if action.PickerID != "picker-2" || action.WorkspaceKey != "/data/web" || action.TargetPickerValue != "thread-b" {
		t.Fatalf("unexpected mapped selection: %+v", action)
	}
}

func TestMapCardEventCancel(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "picker-3",
		EventKey: keyTargetCancel,
	})
	if !ok || action.Kind != control.ActionTargetPickerCancel || action.PickerID != "picker-3" {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
}

func TestMapCardEventRequestRespond(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-7",
		EventKey: composeEncodedKey(keyPrefixRequestRespond, "3", "approve"),
	})
	if !ok || action.Kind != control.ActionRespondRequest {
		t.Fatalf("unexpected result: ok=%v kind=%q", ok, action.Kind)
	}
	if action.Request == nil {
		t.Fatal("expected Request payload")
	}
	if action.Request.RequestID != "req-7" || action.Request.RequestOptionID != "approve" || action.Request.RequestRevision != 3 {
		t.Fatalf("unexpected request payload: %+v", action.Request)
	}
}

func TestMapCardEventRequestRespondDecodesUniqueCardTaskID(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   requestCardTaskID("req-unique-7", 3, "question-control"),
		EventKey: composeEncodedKey(keyPrefixRequestRespond, "3", "approve"),
	})
	if !ok || action.Kind != control.ActionRespondRequest || action.Request == nil {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
	if action.Request.RequestID != "req-unique-7" || action.Request.RequestRevision != 3 {
		t.Fatalf("unique card task ID did not recover request identity: %+v", action.Request)
	}
}

func TestMapCardEventRejectSemanticallyEquivalent(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-7",
		EventKey: composeEncodedKey(keyPrefixRequestRespond, "4", "reject"),
	})
	if !ok || action.Request == nil || action.Request.RequestOptionID != "reject" || action.Request.RequestRevision != 4 {
		t.Fatalf("unexpected reject mapping: ok=%v action=%+v", ok, action)
	}
}

func TestMapCardEventRequestSubmitCarriesRevision(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-8",
		EventKey: composeEncodedKey(keyPrefixRequestSubmit, "5"),
	})
	if !ok || action.Kind != control.ActionRespondRequest || action.Request == nil {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
	if action.Request.RequestID != "req-8" || action.Request.RequestRevision != 5 || action.Request.RequestOptionID != "" {
		t.Fatalf("unexpected submit payload: %+v", action.Request)
	}
}

func TestMapCardEventRequestAnswerButtonBuildsAnswers(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-9",
		EventKey: composeEncodedKey(keyPrefixRequestAnswer, "2", "model", "gpt-5.4"),
	})
	if !ok || action.Kind != control.ActionRespondRequest || action.Request == nil {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
	if action.Request.RequestRevision != 2 {
		t.Fatalf("expected revision 2, got %+v", action.Request)
	}
	answers := action.Request.Answers
	if len(answers) != 1 || len(answers["model"]) != 1 || answers["model"][0] != "gpt-5.4" {
		t.Fatalf("unexpected answer payload: %+v", answers)
	}
}

func TestMapCardEventRequestAnswerSubmitRecoversDropdownSelection(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-10",
		EventKey: composeEncodedKey(keyPrefixRequestAnswerSubmit, "7", "model"),
		Selections: []InboundSelection{{
			QuestionKey: requestAnswerQuestionKey("model"),
			OptionIDs:   []string{"gpt-5.3"},
		}},
	})
	if !ok || action.Kind != control.ActionRespondRequest || action.Request == nil {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
	if action.Request.RequestRevision != 7 {
		t.Fatalf("expected revision 7, got %+v", action.Request)
	}
	answers := action.Request.Answers
	if len(answers["model"]) != 1 || answers["model"][0] != "gpt-5.3" {
		t.Fatalf("unexpected dropdown answers: %+v", answers)
	}
}

func TestMapCardEventRequestControlCarriesQuestionAndRevision(t *testing.T) {
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   "req-11",
		EventKey: composeEncodedKey(keyPrefixRequestControl, "9", "skip_optional", "notes"),
	})
	if !ok || action.Kind != control.ActionControlRequest || action.RequestControl == nil {
		t.Fatalf("unexpected result: ok=%v action=%+v", ok, action)
	}
	if action.RequestControl.RequestID != "req-11" || action.RequestControl.Control != "skip_optional" || action.RequestControl.QuestionID != "notes" || action.RequestControl.RequestRevision != 9 {
		t.Fatalf("unexpected request control payload: %+v", action.RequestControl)
	}
}

func TestMapCardEventUnknownKeyRejected(t *testing.T) {
	if _, ok := MapCardEventToAction(InboundCardEvent{TaskID: "x", EventKey: "bogus::v"}); ok {
		t.Fatal("expected unknown key to be rejected")
	}
	if _, ok := MapCardEventToAction(InboundCardEvent{EventKey: keyTargetConfirm}); ok {
		t.Fatal("expected confirm without task id to be rejected")
	}
	if _, ok := MapCardEventToAction(InboundCardEvent{TaskID: "x", EventKey: keyPrefixTargetWorkspace + keyValueSep}); ok {
		t.Fatal("expected empty workspace value to be rejected")
	}
}

// TestProjectorCallbackRoundTrip verifies the outbound button key minted by the
// Projector is exactly what the callback mapper reconstructs, locking the two
// halves of the rendering/mapping contract together.
func TestProjectorCallbackRoundTrip(t *testing.T) {
	view := control.FeishuTargetPickerView{
		PickerID:            "picker-rt",
		ShowWorkspaceSelect: true,
		WorkspaceOptions: []control.FeishuTargetPickerWorkspaceOption{
			{Value: "/data/web", Label: "web"},
		},
	}
	frames := NewProjector().projectTargetPicker(view)
	card := frames[len(frames)-1].TemplateCard
	if card.CardType != cardTypeButtonInteraction || len(card.ButtonList) != 1 {
		t.Fatalf("unexpected card: %+v", card)
	}
	action, ok := MapCardEventToAction(InboundCardEvent{
		TaskID:   card.TaskID,
		EventKey: card.ButtonList[0].Key,
	})
	if !ok || action.Kind != control.ActionTargetPickerSelectWorkspace {
		t.Fatalf("round trip failed: ok=%v kind=%q", ok, action.Kind)
	}
	if action.PickerID != "picker-rt" || action.WorkspaceKey != "/data/web" {
		t.Fatalf("round trip payload mismatch: %+v", action)
	}
}
