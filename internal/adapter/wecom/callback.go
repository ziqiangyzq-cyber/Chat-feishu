package wecom

import (
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
	Type string `json:"type"`
	// BotID identifies the receiving aibot.
	BotID string `json:"botid"`
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

// selectedOption returns the first selected option id for the given question
// key, or "" when absent.
func (e InboundCardEvent) selectedOption(questionKey string) string {
	for _, sel := range e.Selections {
		if sel.QuestionKey != questionKey {
			continue
		}
		for _, id := range sel.OptionIDs {
			if v := strings.TrimSpace(id); v != "" {
				return v
			}
		}
	}
	return ""
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
		if taskID == "" {
			return control.Action{}, false
		}
		// A multiple_interaction submit carries the chosen dropdown values
		// alongside the confirm button, mirroring the Feishu confirm path which
		// recovers both the workspace key and the session value.
		base.Kind = control.ActionTargetPickerConfirm
		base.PickerID = taskID
		base.WorkspaceKey = event.selectedOption(questionKeyWorkspace)
		base.TargetPickerValue = event.selectedOption(questionKeySession)
		return base, true

	case keyPrefixRequestRespond:
		if taskID == "" || value == "" {
			return control.Action{}, false
		}
		base.Kind = control.ActionRespondRequest
		base.Request = &control.ActionRequestResponse{
			RequestID:       taskID,
			RequestOptionID: value,
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
