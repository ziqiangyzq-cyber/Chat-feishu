package wecom

import (
	"strconv"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

const (
	// defaultMaxButtons mirrors surface.Capabilities.MaxButtons for WeCom: a single
	// template_card of type button_interaction may host at most this many buttons.
	defaultMaxButtons = 6
	// maxSelectOptions mirrors WeCom's multiple_interaction select_list limit.
	maxSelectOptions = 10
)

// ----------------------------------------------------------------------------
// Outbound frame model
//
// The Projector renders a channel-neutral eventcontract.Event into one or more
// WeCom outbound frames. Frame is the adapter's concrete surface.Operation and
// is intentionally self-contained so it is easy to correct once the real WeCom
// aibot wire shapes are validated. Channel.Deliver serialises each Frame into
// the on-the-wire respondMsgFrame (see client.go).
// ----------------------------------------------------------------------------

// Frame is a single outbound WeCom message. Exactly one of Text / Markdown /
// TemplateCard is populated, selected by MsgType.
type Frame struct {
	// MsgType is the WeCom message discriminator: "text", "markdown" or
	// "template_card".
	MsgType string
	// Text carries a plain-text body (MsgType == "text").
	Text *textBody
	// Markdown carries a markdown body (MsgType == "markdown").
	Markdown *markdownBody
	// TemplateCard carries an interactive card (MsgType == "template_card").
	TemplateCard *templateCard
	// File carries a media payload for file/image messages.
	File *mediaBody
	// Image carries a media payload for file/image messages.
	Image *mediaBody
	// LocalPath is used by file/image lanes that still need a media upload.
	LocalPath string
	// Stream marks this frame as a streaming text body that MUST be sent before
	// any interactive TemplateCard frame produced for the same event. WeCom
	// cannot combine streaming text and interactive buttons in one message
	// (surface.Capabilities.InteractiveSameFrame == false), so an event that
	// needs both is rendered as TWO frames: the stream body first, then the
	// interactive card.
	Stream bool
}

// IsSurfaceOperation marks *Frame as a surface.Operation so core code can carry
// it opaquely.
func (*Frame) IsSurfaceOperation() {}

// markdownBody is the payload for a markdown message.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type markdownBody struct {
	Content string `json:"content"`
}

type mediaBody struct {
	MediaID string `json:"media_id"`
}

// WeCom template_card types used by this adapter.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
const (
	cardTypeButtonInteraction   = "button_interaction"
	cardTypeMultipleInteraction = "multiple_interaction"
)

// templateCard is a WeCom interactive card.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type templateCard struct {
	CardType string `json:"card_type"`
	// TaskID echoes back on interaction callbacks. We stash the picker_id /
	// request_id here so the inbound mapper can recover the flow identity.
	TaskID    string         `json:"task_id,omitempty"`
	MainTitle *cardMainTitle `json:"main_title,omitempty"`
	SubTitle  string         `json:"sub_title_text,omitempty"`
	// SelectList holds dropdowns for a multiple_interaction card.
	SelectList []cardSelect `json:"select_list,omitempty"`
	// SubmitButton is the submit control for a multiple_interaction card.
	SubmitButton *cardSubmitButton `json:"submit_button,omitempty"`
	// ButtonList holds buttons for a button_interaction card.
	ButtonList []cardButton `json:"button_list,omitempty"`
}

// cardMainTitle is the card header.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type cardMainTitle struct {
	Title string `json:"title,omitempty"`
	Desc  string `json:"desc,omitempty"`
}

// cardButton is a single interactive button. Key is the stable identifier
// echoed back on click.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type cardButton struct {
	Text  string `json:"text"`
	Key   string `json:"key"`
	Style int    `json:"style,omitempty"`
}

// cardSubmitButton is the submit control required by WeCom
// multiple_interaction cards.
type cardSubmitButton struct {
	Text string `json:"text"`
	Key  string `json:"key"`
}

// cardSelect is a dropdown in a multiple_interaction card. QuestionKey is
// echoed back with the SelectedID(s) on submit.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type cardSelect struct {
	QuestionKey string             `json:"question_key"`
	Title       string             `json:"title,omitempty"`
	SelectedID  string             `json:"selected_id,omitempty"`
	Multi       bool               `json:"multi,omitempty"`
	OptionList  []cardSelectOption `json:"option_list"`
}

// cardSelectOption is a single option inside a dropdown.
//
// PROVISIONAL: validate against real WeCom aibot traffic.
type cardSelectOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ----------------------------------------------------------------------------
// Stable callback key namespace
//
// Button keys and dropdown question keys encode enough for callback.go to
// reconstruct a semantically-equivalent control.Action. Single-select buttons
// carry their value inline as "<prefix><keyValueSep><value>"; dropdowns carry
// the value out-of-band as the selected option id keyed by QuestionKey.
// ----------------------------------------------------------------------------

const (
	keyValueSep = "::"

	keyPrefixTargetWorkspace = "tp.ws"
	keyPrefixTargetSession   = "tp.sess"
	keyTargetConfirm         = "tp.confirm"
	keyTargetCancel          = "tp.cancel"

	questionKeyWorkspace = "tp.ws"
	questionKeySession   = "tp.sess"

	keyPrefixRequestRespond      = "req.respond"
	keyPrefixRequestAnswer       = "req.answer"
	keyPrefixRequestAnswerSubmit = "req.answer_submit"
	keyPrefixRequestControl      = "req.control"
	keyPrefixRequestSubmit       = "req.submit"
)

// Projector renders channel-neutral events into WeCom outbound frames.
type Projector struct {
	maxButtons int
}

// NewProjector constructs a Projector with the default WeCom button budget.
func NewProjector() *Projector {
	return &Projector{maxButtons: defaultMaxButtons}
}

// ProjectEvent renders an event into an ordered slice of outbound frames. When
// an event carries both a text body and interactive controls, the stream body
// frame is emitted first, followed by the interactive template_card frame (see
// the Frame.Stream doc comment for the WeCom same-frame constraint).
func (p *Projector) ProjectEvent(event eventcontract.Event) []Frame {
	event = event.Normalized()
	switch payload := event.CanonicalPayload().(type) {
	case eventcontract.TimelineTextPayload:
		text := strings.TrimSpace(payload.TimelineText.Text)
		if text == "" {
			return nil
		}
		return []Frame{textFrame(text)}
	case eventcontract.BlockCommittedPayload:
		return projectBlock(payload.Block)
	case eventcontract.NoticePayload:
		return projectNotice(payload.Notice)
	case eventcontract.PlanUpdatePayload:
		body := planUpdateMarkdown(payload.PlanUpdate)
		if body == "" {
			return nil
		}
		return []Frame{markdownFrame(body)}
	case eventcontract.TargetPickerPayload:
		return p.projectTargetPicker(payload.View)
	case eventcontract.RequestPayload:
		return p.projectRequest(payload.View)
	case eventcontract.PagePayload:
		return projectPageView(payload.View)
	case eventcontract.ImageOutputPayload:
		return p.projectImageOutput(payload.ImageOutput)
	default:
		return nil
	}
}

// ---- text / markdown lanes -------------------------------------------------

func textFrame(content string) Frame {
	return Frame{MsgType: "text", Text: &textBody{Content: content}}
}

func markdownFrame(content string) Frame {
	return Frame{MsgType: "markdown", Markdown: &markdownBody{Content: content}}
}

func imageMediaFrame(mediaID string) Frame {
	return Frame{MsgType: "image", Image: &mediaBody{MediaID: strings.TrimSpace(mediaID)}}
}

func fileMediaFrame(mediaID string) Frame {
	return Frame{MsgType: "file", File: &mediaBody{MediaID: strings.TrimSpace(mediaID)}}
}

func projectBlock(block render.Block) []Frame {
	text := strings.TrimSpace(block.Text)
	if text == "" {
		return nil
	}
	if block.Final {
		body := text
		if block.Kind == render.BlockAssistantCode {
			body = fenced(block.Language, block.Text)
		}
		return []Frame{markdownFrame(body)}
	}
	return []Frame{textFrame(text)}
}

func fenced(language, text string) string {
	if language == "" {
		language = "text"
	}
	return "```" + language + "\n" + text + "\n```"
}

func projectNotice(notice control.Notice) []Frame {
	title := strings.TrimSpace(notice.Title)
	body := strings.TrimSpace(notice.Text)
	var b strings.Builder
	if title != "" {
		b.WriteString("**")
		b.WriteString(title)
		b.WriteString("**")
	}
	if body != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(body)
	}
	if b.Len() == 0 {
		return nil
	}
	return []Frame{markdownFrame(b.String())}
}

// projectPageView renders a generic page/config card (e.g. /mode, /model, menu
// pages) as markdown. WeCom cannot render the interactive Feishu card, so the
// available actions are surfaced as slash commands the user can type instead.
func projectPageView(view control.FeishuPageView) []Frame {
	view = control.NormalizeFeishuPageView(view)
	var parts []string
	if title := strings.TrimSpace(view.Title); title != "" {
		parts = append(parts, "**"+title+"**")
	}
	appendTextSections := func(sections []control.FeishuCardTextSection) {
		for _, section := range sections {
			if label := strings.TrimSpace(section.Label); label != "" {
				parts = append(parts, "**"+label+"**")
			}
			for _, line := range section.Lines {
				if line = strings.TrimSpace(line); line != "" {
					parts = append(parts, line)
				}
			}
		}
	}
	appendTextSections(view.BodySections)
	appendTextSections(view.NoticeSections)

	var actionLines []string
	seenAction := map[string]bool{}
	addAction := func(label, command, openURL string) {
		label = strings.TrimSpace(label)
		command = strings.TrimSpace(command)
		openURL = strings.TrimSpace(openURL)
		var line string
		switch {
		case command != "" && label != "":
			line = "- " + label + "：`" + command + "`"
		case command != "":
			line = "- `" + command + "`"
		case openURL != "" && label != "":
			line = "- " + label + "：" + openURL
		default:
			return
		}
		if seenAction[line] {
			return
		}
		seenAction[line] = true
		actionLines = append(actionLines, line)
	}
	for _, section := range view.Sections {
		for _, entry := range section.Entries {
			if len(entry.Buttons) > 0 {
				for _, btn := range entry.Buttons {
					addAction(btn.Label, btn.CommandText, btn.OpenURL)
				}
				continue
			}
			for _, command := range entry.Commands {
				addAction(entry.Title, command, "")
			}
		}
	}
	for _, btn := range view.RelatedButtons {
		addAction(btn.Label, btn.CommandText, btn.OpenURL)
	}
	if len(actionLines) > 0 {
		parts = append(parts, "**可用操作**")
		parts = append(parts, actionLines...)
	}

	body := strings.TrimSpace(strings.Join(parts, "\n"))
	if body == "" {
		return nil
	}
	return []Frame{markdownFrame(body)}
}

func (p *Projector) projectImageOutput(image control.ImageOutput) []Frame {
	if strings.TrimSpace(image.SavedPath) == "" && strings.TrimSpace(image.ImageBase64) == "" {
		return nil
	}
	if strings.TrimSpace(image.SavedPath) == "" {
		return []Frame{markdownFrame("已生成图片，但当前企微链路暂不支持直接发送内存图片，请改为落盘后重试。")}
	}
	return []Frame{{
		MsgType:   "image",
		LocalPath: strings.TrimSpace(image.SavedPath),
	}}
}

func planUpdateMarkdown(plan control.PlanUpdate) string {
	var b strings.Builder
	b.WriteString("**当前计划**")
	for _, step := range plan.Steps {
		label := strings.TrimSpace(step.Step)
		if label == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(planStepMarker(step.Status))
		b.WriteString(" ")
		b.WriteString(label)
	}
	return b.String()
}

func planStepMarker(status agentproto.TurnPlanStepStatus) string {
	switch status {
	case agentproto.TurnPlanStepStatusCompleted:
		return "[x]"
	case agentproto.TurnPlanStepStatusInProgress:
		return "[~]"
	default:
		return "[ ]"
	}
}

// ---- target picker ---------------------------------------------------------

// projectTargetPicker renders a workspace/session target picker.
//
// Strategy: when exactly one selection dimension is shown and it fits inside the
// button budget, render a button_interaction card (one button per option). This
// is the fast path for e.g. a short workspace list. Otherwise render a
// multiple_interaction card with a dropdown per shown dimension plus
// confirm/cancel buttons.
//
// When the view carries a question / body, it is emitted as a separate stream
// frame first (WeCom cannot combine text and interactive controls in one
// message).
func (p *Projector) projectTargetPicker(view control.FeishuTargetPickerView) []Frame {
	view = control.NormalizeFeishuTargetPickerView(view)
	if view.Stage != "" && view.Stage != control.FeishuTargetPickerStageEditing {
		if body := targetPickerStatusMarkdown(view); body != "" {
			return []Frame{markdownFrame(body)}
		}
	}
	title := strings.TrimSpace(view.Title)
	if title == "" {
		title = "选择工作区与会话"
	}
	pickerID := strings.TrimSpace(view.PickerID)

	wsOptions := targetWorkspaceOptions(view)
	sessOptions := targetSessionOptions(view)

	var frames []Frame
	if body := targetPickerBody(view); body != "" {
		frames = append(frames, streamMarkdownFrame(body))
	}

	singleDimension := (view.ShowWorkspaceSelect && !view.ShowSessionSelect) ||
		(!view.ShowWorkspaceSelect && view.ShowSessionSelect)

	if singleDimension {
		options := wsOptions
		prefix := keyPrefixTargetWorkspace
		if view.ShowSessionSelect {
			options = sessOptions
			prefix = keyPrefixTargetSession
		}
		if len(options) > 0 && len(options) <= p.maxButtons {
			card := &templateCard{
				CardType:   cardTypeButtonInteraction,
				TaskID:     pickerID,
				MainTitle:  &cardMainTitle{Title: title},
				ButtonList: optionButtons(prefix, options),
			}
			return append(frames, cardFrame(card))
		}
	}

	// Multiple-interaction fallback: dropdowns + confirm/cancel.
	card := &templateCard{
		CardType:  cardTypeMultipleInteraction,
		TaskID:    uniqueTaskID("tp"),
		MainTitle: &cardMainTitle{Title: title},
	}
	if view.ShowWorkspaceSelect {
		options := selectOptions(wsOptions)
		card.SelectList = append(card.SelectList, cardSelect{
			QuestionKey: questionKeyWorkspace,
			Title:       firstNonEmpty(view.WorkspacePlaceholder, "工作区"),
			SelectedID:  selectedOptionID(strings.TrimSpace(view.SelectedWorkspaceKey), options),
			OptionList:  options,
		})
	}
	if view.ShowSessionSelect {
		options := selectOptions(sessOptions)
		card.SelectList = append(card.SelectList, cardSelect{
			QuestionKey: questionKeySession,
			Title:       firstNonEmpty(view.SessionPlaceholder, "会话"),
			SelectedID:  selectedOptionID(strings.TrimSpace(view.SelectedSessionValue), options),
			OptionList:  options,
		})
	}
	confirmLabel := firstNonEmpty(strings.TrimSpace(view.ConfirmLabel), "确认")
	card.SubmitButton = &cardSubmitButton{Text: confirmLabel, Key: keyTargetConfirm + keyValueSep + pickerID}
	return append(frames, cardFrame(card))
}

type namedOption struct {
	Value string
	Label string
}

func targetWorkspaceOptions(view control.FeishuTargetPickerView) []namedOption {
	out := make([]namedOption, 0, len(view.WorkspaceOptions))
	for _, opt := range view.WorkspaceOptions {
		value := strings.TrimSpace(opt.Value)
		if value == "" {
			continue
		}
		out = append(out, namedOption{Value: value, Label: firstNonEmpty(strings.TrimSpace(opt.Label), value)})
	}
	return out
}

func targetSessionOptions(view control.FeishuTargetPickerView) []namedOption {
	out := make([]namedOption, 0, len(view.SessionOptions))
	for _, opt := range view.SessionOptions {
		value := strings.TrimSpace(opt.Value)
		if value == "" {
			continue
		}
		out = append(out, namedOption{Value: value, Label: firstNonEmpty(strings.TrimSpace(opt.Label), value)})
	}
	return out
}

func optionButtons(prefix string, options []namedOption) []cardButton {
	buttons := make([]cardButton, 0, len(options))
	for _, opt := range options {
		buttons = append(buttons, cardButton{
			Text: opt.Label,
			Key:  prefix + keyValueSep + opt.Value,
		})
	}
	return buttons
}

func selectOptions(options []namedOption) []cardSelectOption {
	if len(options) > maxSelectOptions {
		options = options[:maxSelectOptions]
	}
	out := make([]cardSelectOption, 0, len(options))
	for _, opt := range options {
		out = append(out, cardSelectOption{ID: opt.Value, Text: opt.Label})
	}
	return out
}

func selectedOptionID(selected string, options []cardSelectOption) string {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return ""
	}
	for _, opt := range options {
		if opt.ID == selected {
			return selected
		}
	}
	return ""
}

func uniqueTaskID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "card"
	}
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func targetPickerBody(view control.FeishuTargetPickerView) string {
	var parts []string
	if q := strings.TrimSpace(view.Question); q != "" {
		parts = append(parts, q)
	}
	if h := strings.TrimSpace(view.Hint); h != "" {
		parts = append(parts, h)
	}
	return strings.Join(parts, "\n")
}

func targetPickerStatusMarkdown(view control.FeishuTargetPickerView) string {
	title := strings.TrimSpace(view.StatusTitle)
	if title == "" {
		title = targetPickerStageTitle(view.Stage)
	}
	var parts []string
	if title != "" {
		parts = append(parts, "**"+title+"**")
	}
	if text := strings.TrimSpace(view.StatusText); text != "" {
		parts = append(parts, text)
	}
	for _, section := range view.StatusSections {
		label := strings.TrimSpace(section.Label)
		if label != "" {
			parts = append(parts, "**"+label+"**")
		}
		for _, line := range section.Lines {
			if line = strings.TrimSpace(line); line != "" {
				parts = append(parts, line)
			}
		}
	}
	if footer := strings.TrimSpace(view.StatusFooter); footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
}

func targetPickerStageTitle(stage control.FeishuTargetPickerStage) string {
	switch stage {
	case control.FeishuTargetPickerStageProcessing:
		return "正在处理"
	case control.FeishuTargetPickerStageSucceeded:
		return "已完成"
	case control.FeishuTargetPickerStageFailed:
		return "处理失败"
	case control.FeishuTargetPickerStageCancelled:
		return "已取消"
	default:
		return ""
	}
}

// ---- frame builders --------------------------------------------------------

func streamMarkdownFrame(content string) Frame {
	f := markdownFrame(content)
	f.Stream = true
	return f
}

func cardFrame(card *templateCard) Frame {
	return Frame{MsgType: "template_card", TemplateCard: card}
}

type mediaType string

const (
	mediaTypeFile  mediaType = "file"
	mediaTypeImage mediaType = "image"
)

func mediaFrame(kind mediaType, mediaID string) Frame {
	switch kind {
	case mediaTypeImage:
		return imageMediaFrame(mediaID)
	default:
		return fileMediaFrame(mediaID)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
