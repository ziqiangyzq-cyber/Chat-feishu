package wecom

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

// ----------------------------------------------------------------------------
// Inbound interaction model
//
// A WeCom aibot_event_callback frame reports a user interacting with a
// template_card: clicking a button (button_interaction) or submitting selected
// dropdown options (multiple_interaction). InboundCardEvent is the decoded,
// adapter-local representation; MapCardEventToAction reconstructs the
// channel-neutral control.Action that is SEMANTICALLY EQUIVALENT to what the
// Feishu inbound lane produces for the same interaction (see
// internal/adapter/feishu/gateway/routing_target_picker.go and routing.go).
//
// Keys/question keys are the stable identifiers minted by the Projector; see
// the key namespace constants in projector.go.
// ----------------------------------------------------------------------------

// InboundCardEvent is the decoded shape of an aibot_event_callback carrying a
// template_card interaction.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type InboundCardEvent struct {
	Cmd     string       `json:"cmd,omitempty"`
	Headers frameHeaders `json:"headers,omitempty"`
	// BotID identifies the receiving aibot.
	BotID string `json:"aibotid"`
	// ChatID is the conversation the interaction happened in.
	ChatID string `json:"chatid"`
	// EventType is the callback sub-discriminator (e.g. a template_card event).
	EventType string `json:"event_type"`
	// OperatorUserID is the user who interacted with the card.
	OperatorUserID string `json:"operator_userid"`
	// MessageID identifies the card message that was interacted with.
	MessageID string `json:"msgid"`
	// TaskID echoes the card's task_id; the Projector stores the picker_id /
	// request_id there so the flow identity round-trips.
	TaskID string `json:"task_id"`
	// EventKey is the clicked button's key (button_interaction).
	EventKey string `json:"event_key"`
	// Selections carries the submitted dropdown values (multiple_interaction).
	Selections []InboundSelection `json:"selections,omitempty"`
}

// InboundSelection is one dropdown's submitted value(s), keyed by the
// QuestionKey the Projector assigned.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type InboundSelection struct {
	QuestionKey string   `json:"question_key"`
	OptionIDs   []string `json:"option_ids"`
}

type inboundEventCallbackWire struct {
	Cmd     string          `json:"cmd"`
	Headers frameHeaders    `json:"headers,omitempty"`
	Body    json.RawMessage `json:"body"`
}

type inboundEventCallbackBody struct {
	BotID          string `json:"aibotid"`
	ChatID         string `json:"chatid"`
	ChatType       string `json:"chattype"`
	MessageID      string `json:"msgid"`
	MsgType        string `json:"msgtype"`
	OperatorUserID string `json:"operator_userid"`
	FromUserID     string `json:"from_userid"`
	UserID         string `json:"userid"`
	From           struct {
		UserID string `json:"userid"`
	} `json:"from"`
	EventType         string                     `json:"eventtype"`
	TemplateCardEvent inboundTemplateCardEvent   `json:"template_card_event"`
	Event             inboundTemplateCardWrapper `json:"event"`
}

type inboundTemplateCardWrapper struct {
	EventType         string                   `json:"eventtype"`
	TemplateCardEvent inboundTemplateCardEvent `json:"template_card_event"`
}

type inboundTemplateCardEvent struct {
	CardType      string               `json:"card_type"`
	EventKey      string               `json:"event_key"`
	TaskID        string               `json:"task_id"`
	SelectedItems inboundSelectedItems `json:"selected_items"`
}

type inboundSelectedItems struct {
	SelectedItem inboundSelectedItemList `json:"selected_item"`
}

type inboundSelectedItemList []inboundSelectedItem

func (l *inboundSelectedItemList) UnmarshalJSON(data []byte) error {
	var items []inboundSelectedItem
	if err := json.Unmarshal(data, &items); err == nil {
		*l = items
		return nil
	}
	var item inboundSelectedItem
	if err := json.Unmarshal(data, &item); err != nil {
		return err
	}
	*l = []inboundSelectedItem{item}
	return nil
}

type inboundSelectedItem struct {
	QuestionKey string          `json:"question_key"`
	OptionIDs   inboundOptionID `json:"option_ids"`
}

type inboundOptionID []string

func (ids *inboundOptionID) UnmarshalJSON(data []byte) error {
	var object struct {
		OptionID json.RawMessage `json:"option_id"`
	}
	if err := json.Unmarshal(data, &object); err == nil && len(object.OptionID) > 0 {
		return ids.UnmarshalJSON(object.OptionID)
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*ids = list
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	*ids = []string{single}
	return nil
}

func decodeInboundCardEvent(raw []byte) (InboundCardEvent, error) {
	var wire inboundEventCallbackWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return InboundCardEvent{}, err
	}

	var flat InboundCardEvent
	_ = json.Unmarshal(wire.Body, &flat)

	var body inboundEventCallbackBody
	_ = json.Unmarshal(wire.Body, &body)

	nested := body.Event.TemplateCardEvent
	direct := body.TemplateCardEvent

	event := flat
	event.Cmd = wire.Cmd
	event.Headers = wire.Headers
	event.BotID = firstNonEmpty(event.BotID, body.BotID)
	event.ChatID = firstNonEmpty(event.ChatID, body.ChatID, body.From.UserID, body.FromUserID, body.UserID)
	event.EventType = firstNonEmpty(event.EventType, body.Event.EventType, body.EventType)
	event.OperatorUserID = firstNonEmpty(event.OperatorUserID, body.OperatorUserID, body.From.UserID, body.FromUserID, body.UserID)
	event.MessageID = firstNonEmpty(event.MessageID, body.MessageID)
	event.TaskID = firstNonEmpty(event.TaskID, nested.TaskID, direct.TaskID)
	event.EventKey = firstNonEmpty(event.EventKey, nested.EventKey, direct.EventKey)
	event.Selections = append(event.Selections, nested.SelectedItems.toSelections()...)
	event.Selections = append(event.Selections, direct.SelectedItems.toSelections()...)
	return event, nil
}

func (items inboundSelectedItems) toSelections() []InboundSelection {
	out := make([]InboundSelection, 0, len(items.SelectedItem))
	for _, item := range items.SelectedItem {
		questionKey := strings.TrimSpace(item.QuestionKey)
		if questionKey == "" {
			continue
		}
		optionIDs := make([]string, 0, len(item.OptionIDs))
		for _, optionID := range item.OptionIDs {
			if optionID = strings.TrimSpace(optionID); optionID != "" {
				optionIDs = append(optionIDs, optionID)
			}
		}
		out = append(out, InboundSelection{QuestionKey: questionKey, OptionIDs: optionIDs})
	}
	return out
}

// selectedOption returns the first selected option id for the given question
// key, or "" when absent.
func (e InboundCardEvent) selectedOption(questionKey string) string {
	for _, id := range e.selectedOptions(questionKey) {
		if v := strings.TrimSpace(id); v != "" {
			return v
		}
	}
	return ""
}

func (e InboundCardEvent) selectedOptions(questionKey string) []string {
	out := []string(nil)
	for _, sel := range e.Selections {
		if sel.QuestionKey != questionKey {
			continue
		}
		for _, id := range sel.OptionIDs {
			if v := strings.TrimSpace(id); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// MapCardEventToAction reconstructs a control.Action from a decoded card
// interaction. The bool result is false when the event does not map to any
// known action (caller should ignore it).
func MapCardEventToAction(event InboundCardEvent) (control.Action, bool) {
	key := strings.TrimSpace(event.EventKey)
	taskID := strings.TrimSpace(event.TaskID)

	base := control.Action{
		ChatID:      strings.TrimSpace(event.ChatID),
		ActorUserID: strings.TrimSpace(event.OperatorUserID),
		MessageID:   strings.TrimSpace(event.MessageID),
	}

	prefix, value := splitKey(key)
	switch prefix {
	case keyPrefixTargetWorkspace:
		if taskID == "" || value == "" {
			return control.Action{}, false
		}
		base.Kind = control.ActionTargetPickerSelectWorkspace
		base.PickerID = taskID
		base.WorkspaceKey = value
		return base, true

	case keyPrefixTargetSession:
		if taskID == "" || value == "" {
			return control.Action{}, false
		}
		base.Kind = control.ActionTargetPickerSelectSession
		base.PickerID = taskID
		base.TargetPickerValue = value
		return base, true

	case keyTargetCancel:
		if taskID == "" {
			return control.Action{}, false
		}
		base.Kind = control.ActionTargetPickerCancel
		base.PickerID = taskID
		return base, true

	case keyTargetConfirm:
		pickerID := firstNonEmpty(value, taskID)
		if pickerID == "" {
			return control.Action{}, false
		}
		// A multiple_interaction submit carries the chosen dropdown values
		// alongside the confirm button, mirroring the Feishu confirm path which
		// recovers both the workspace key and the session value.
		base.Kind = control.ActionTargetPickerConfirm
		base.PickerID = pickerID
		base.WorkspaceKey = event.selectedOption(questionKeyWorkspace)
		base.TargetPickerValue = event.selectedOption(questionKeySession)
		return base, true

	case keyPrefixRequestRespond:
		requestRevision, optionID, ok := decodeRequestRespondKey(value)
		if taskID == "" || !ok {
			return control.Action{}, false
		}
		base.Kind = control.ActionRespondRequest
		base.Request = &control.ActionRequestResponse{
			RequestID:       taskID,
			RequestOptionID: optionID,
			RequestRevision: requestRevision,
		}
		return base, true

	case keyPrefixRequestSubmit:
		if taskID == "" {
			return control.Action{}, false
		}
		revision, _ := strconv.Atoi(strings.TrimSpace(firstRequestKeyPart(value)))
		base.Kind = control.ActionRespondRequest
		base.Request = &control.ActionRequestResponse{
			RequestID:       taskID,
			RequestRevision: revision,
		}
		return base, true

	case keyPrefixRequestAnswer:
		revision, questionID, answer, ok := decodeRequestAnswerKey(value)
		if taskID == "" || !ok {
			return control.Action{}, false
		}
		base.Kind = control.ActionRespondRequest
		base.Request = &control.ActionRequestResponse{
			RequestID:       taskID,
			RequestRevision: revision,
			Answers: map[string][]string{
				questionID: {answer},
			},
		}
		return base, true

	case keyPrefixRequestAnswerSubmit:
		revision, questionID, ok := decodeRequestAnswerSubmitKey(value)
		if taskID == "" || !ok {
			return control.Action{}, false
		}
		answers := event.selectedOptions(requestStructuredFormQuestionKey(questionID))
		if len(answers) == 0 {
			answers = event.selectedOptions(requestAnswerQuestionKey(questionID))
		}
		if len(answers) == 0 {
			return control.Action{}, false
		}
		base.Kind = control.ActionRespondRequest
		base.Request = &control.ActionRequestResponse{
			RequestID:       taskID,
			RequestRevision: revision,
			Answers: map[string][]string{
				questionID: answers,
			},
		}
		return base, true

	case keyPrefixRequestControl:
		revision, controlName, questionID, ok := decodeRequestControlKey(value)
		if taskID == "" || !ok {
			return control.Action{}, false
		}
		base.Kind = control.ActionControlRequest
		base.RequestControl = &control.ActionRequestControl{
			RequestID:       taskID,
			Control:         controlName,
			QuestionID:      questionID,
			RequestRevision: revision,
		}
		return base, true

	default:
		return control.Action{}, false
	}
}

// splitKey splits a callback key of the form "<prefix>::<value>" into its parts.
// Keys without a separator (e.g. "tp.confirm") return the whole key as prefix
// and an empty value.
func splitKey(key string) (prefix, value string) {
	if idx := strings.Index(key, keyValueSep); idx >= 0 {
		return key[:idx], key[idx+len(keyValueSep):]
	}
	return key, ""
}

func firstRequestKeyPart(value string) string {
	parts := splitEncodedKeyParts(value)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func decodeRequestRespondKey(value string) (revision int, optionID string, ok bool) {
	parts := splitEncodedKeyParts(value)
	switch len(parts) {
	case 1:
		return 0, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[0]) != ""
	default:
		revision, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
		optionID = strings.TrimSpace(parts[1])
		return revision, optionID, optionID != ""
	}
}

func decodeRequestAnswerKey(value string) (revision int, questionID, answer string, ok bool) {
	parts := splitEncodedKeyParts(value)
	if len(parts) < 3 {
		return 0, "", "", false
	}
	revision, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	questionID = strings.TrimSpace(parts[1])
	answer = strings.TrimSpace(parts[2])
	return revision, questionID, answer, questionID != "" && answer != ""
}

func decodeRequestAnswerSubmitKey(value string) (revision int, questionID string, ok bool) {
	parts := splitEncodedKeyParts(value)
	if len(parts) < 2 {
		return 0, "", false
	}
	revision, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	questionID = strings.TrimSpace(parts[1])
	return revision, questionID, questionID != ""
}

func decodeRequestControlKey(value string) (revision int, controlName, questionID string, ok bool) {
	parts := splitEncodedKeyParts(value)
	if len(parts) == 0 {
		return 0, "", "", false
	}
	if len(parts) == 1 {
		return 0, strings.TrimSpace(parts[0]), "", strings.TrimSpace(parts[0]) != ""
	}
	revision, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	controlName = strings.TrimSpace(parts[1])
	if len(parts) > 2 {
		questionID = strings.TrimSpace(parts[2])
	}
	return revision, controlName, questionID, controlName != ""
}
