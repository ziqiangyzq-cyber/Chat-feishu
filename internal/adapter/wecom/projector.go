package wecom

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
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

// ---- request (approve / reject / request_user_input) -----------------------

// projectRequest renders approvals, request_user_input flows, and request
// status summaries. When the current prompt can be answered by choosing one of
// the surfaced options, WeCom receives either a button_interaction card or a
// single-dropdown multiple_interaction card. Prompts that still need text /
// structured-form input are rendered with an explicit degradation note so the
// user understands they must continue in Feishu.
func (p *Projector) projectRequest(view control.FeishuRequestView) []Frame {
	view = control.NormalizeFeishuRequestView(view)
	title := strings.TrimSpace(view.Title)
	if title == "" {
		title = "需要确认"
	}
	requestID := strings.TrimSpace(view.RequestID)

	var frames []Frame
	if body := requestBody(view); body != "" {
		frames = append(frames, streamMarkdownFrame(body))
	}
	if view.Sealed {
		if len(frames) == 0 {
			return []Frame{markdownFrame("**" + title + "**")}
		}
		return frames
	}

	if view.StructuredForm != nil {
		frames = append(frames, p.projectRequestStructuredFormFrames(view, requestID)...)
		if len(frames) == 0 {
			return []Frame{markdownFrame("**" + title + "**")}
		}
		return frames
	}

	if len(view.Questions) != 0 {
		frames = append(frames, p.projectRequestQuestionFrames(view, requestID)...)
		if len(frames) == 0 {
			return []Frame{markdownFrame("**" + title + "**")}
		}
		return frames
	}

	if card := p.projectRequestOptionCard(title, requestID, view.Options, view.RequestRevision, ""); card != nil {
		return append(frames, cardFrame(card))
	}
	if len(frames) == 0 {
		return []Frame{markdownFrame("**" + title + "**")}
	}
	return frames
}

func requestBody(view control.FeishuRequestView) string {
	parts := make([]string, 0, 8)
	for _, section := range requestBodySections(view) {
		if body := requestSectionMarkdown(section); body != "" {
			parts = append(parts, body)
		}
	}
	if progress := requestProgressMarkdown(view); progress != "" {
		parts = append(parts, progress)
	}
	if question, index, ok := requestCurrentQuestion(view); ok {
		if body := requestQuestionMarkdown(question, index, len(view.Questions)); body != "" {
			parts = append(parts, body)
		}
		if note := requestQuestionSupportNote(view, question); note != "" {
			parts = append(parts, note)
		}
	} else if field, index, ok := requestCurrentStructuredFormField(view); ok {
		if body := requestStructuredFormFieldMarkdown(view.StructuredForm, field, index); body != "" {
			parts = append(parts, body)
		}
		if note := requestStructuredFormFieldSupportNote(field); note != "" {
			parts = append(parts, note)
		}
	}
	if body := requestStructuredFormMarkdown(view.StructuredForm); body != "" {
		parts = append(parts, body)
	}
	if requestQuestionsComplete(view) && frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) && !view.Sealed {
		parts = append(parts, "所有题目已处理完成，可直接点击“重新提交”。")
	} else if requestStructuredFormComplete(view) && frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) && !view.Sealed {
		parts = append(parts, "当前结构化表单已填写完成，可直接提交。")
	}
	if hint := strings.TrimSpace(view.HintText); hint != "" {
		parts = append(parts, "提示："+hint)
	}
	if status := strings.TrimSpace(view.StatusText); status != "" {
		parts = append(parts, "状态："+status)
	}
	return strings.Join(parts, "\n")
}

func requestButtonStyle(style string) int {
	if strings.EqualFold(strings.TrimSpace(style), "primary") {
		return 1
	}
	return 0
}

func requestBodySections(view control.FeishuRequestView) []control.FeishuCardTextSection {
	sections := make([]control.FeishuCardTextSection, 0, len(view.Sections)+1)
	if threadTitle := strings.TrimSpace(view.ThreadTitle); threadTitle != "" {
		sections = append(sections, control.FeishuCardTextSection{
			Lines: []string{"当前会话：" + threadTitle},
		})
	}
	for _, section := range view.Sections {
		if normalized := section.Normalized(); normalized.Label != "" || len(normalized.Lines) != 0 {
			sections = append(sections, normalized)
		}
	}
	return sections
}

func requestSectionMarkdown(section control.FeishuCardTextSection) string {
	section = section.Normalized()
	if section.Label == "" && len(section.Lines) == 0 {
		return ""
	}
	lines := make([]string, 0, len(section.Lines)+1)
	if section.Label != "" {
		lines = append(lines, "**"+section.Label+"**")
	}
	lines = append(lines, section.Lines...)
	return strings.Join(lines, "\n")
}

func requestProgressMarkdown(view control.FeishuRequestView) string {
	if len(view.Questions) != 0 {
		completed := 0
		for _, question := range view.Questions {
			if question.Answered || question.Skipped {
				completed++
			}
		}
		return "**回答进度** " + strconv.Itoa(completed) + "/" + strconv.Itoa(len(view.Questions)) + " · 当前第 " + strconv.Itoa(normalizedRequestCurrentQuestionIndex(view)+1) + " 题"
	}
	if view.StructuredForm == nil || len(view.StructuredForm.Fields) == 0 {
		return ""
	}
	completed := 0
	for _, field := range view.StructuredForm.Fields {
		if structuredFormFieldAnswered(field) {
			completed++
		}
	}
	return "**填写进度** " + strconv.Itoa(completed) + "/" + strconv.Itoa(len(view.StructuredForm.Fields)) + " · 当前第 " + strconv.Itoa(normalizedRequestCurrentQuestionIndex(view)+1) + " 项"
}

func normalizedRequestCurrentQuestionIndex(view control.FeishuRequestView) int {
	total := len(view.Questions)
	if total == 0 && view.StructuredForm != nil {
		total = len(view.StructuredForm.Fields)
	}
	if total == 0 {
		return 0
	}
	if view.CurrentQuestionIndex < 0 {
		return 0
	}
	if view.CurrentQuestionIndex >= total {
		return total - 1
	}
	return view.CurrentQuestionIndex
}

func requestCurrentQuestion(view control.FeishuRequestView) (control.RequestPromptQuestion, int, bool) {
	if len(view.Questions) == 0 {
		return control.RequestPromptQuestion{}, 0, false
	}
	index := normalizedRequestCurrentQuestionIndex(view)
	return view.Questions[index], index, true
}

func requestQuestionMarkdown(question control.RequestPromptQuestion, index, total int) string {
	lines := make([]string, 0, 12)
	lines = append(lines, "**"+requestQuestionLabel(index, total)+"**")
	title := firstNonEmpty(strings.TrimSpace(question.Header), strings.TrimSpace(question.Question), strings.TrimSpace(question.ID))
	if title != "" {
		lines = append(lines, "标题："+title)
	}
	switch {
	case question.Answered:
		if question.Secret {
			lines = append(lines, "状态：已回答（私密）")
		} else {
			lines = append(lines, "状态：已回答")
		}
	case question.Skipped:
		lines = append(lines, "状态：已跳过")
	default:
		lines = append(lines, "状态：待回答")
	}
	if question.Optional {
		lines = append(lines, "该题可跳过。")
	}
	if prompt := strings.TrimSpace(question.Question); prompt != "" && prompt != title {
		lines = append(lines, prompt)
	}
	if value := strings.TrimSpace(question.DefaultValue); value != "" {
		if question.Secret {
			lines = append(lines, "当前答案：已填写（私密）")
		} else {
			lines = append(lines, "当前答案："+value)
		}
	}
	if len(question.Options) != 0 {
		lines = append(lines, "可选项：")
		for _, option := range question.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" {
				continue
			}
			line := "- " + label
			if description := strings.TrimSpace(option.Description); description != "" {
				line += "：" + description
			}
			lines = append(lines, line)
		}
	}
	if question.AllowOther {
		lines = append(lines, "也可以填写其他答案。")
	}
	return strings.Join(lines, "\n")
}

func requestQuestionLabel(index, total int) string {
	if total <= 0 {
		return "问题 " + strconv.Itoa(index+1)
	}
	return "问题 " + strconv.Itoa(index+1) + "/" + strconv.Itoa(total)
}

func requestQuestionSupportNote(view control.FeishuRequestView, question control.RequestPromptQuestion) string {
	if view.Sealed || !frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) {
		return ""
	}
	switch {
	case len(question.Options) == 0:
		if question.Secret {
			return "当前题目支持直接发送一条企微文本继续回答；如果答案涉及敏感信息，仍建议切换到飞书处理。"
		}
		return "当前题目支持直接发送一条企微文本继续回答，无需切换到飞书。"
	case question.AllowOther || !question.DirectResponse:
		return "当前企微可直接选择已提供的选项；如果要补充自定义文本，也可以直接回复一条企微消息继续回答。涉及 JSON、结构化表单或更复杂内容时，建议切换到飞书。"
	default:
		return ""
	}
}

func requestStructuredFormMarkdown(form *control.RequestPromptStructuredForm) string {
	if form == nil {
		return ""
	}
	lines := []string{"**结构化表单**", "当前企微已支持按字段逐步填写当前结构化表单。"}
	for _, field := range form.Fields {
		label := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.Name))
		if label == "" {
			continue
		}
		lines = append(lines, "- "+label+"（"+requestStructuredFormFieldKindLabel(field.Kind)+"）")
	}
	return strings.Join(lines, "\n")
}

func requestStructuredFormFieldKindLabel(kind control.RequestPromptFormFieldKind) string {
	switch kind {
	case control.RequestPromptFormFieldSelectStatic:
		return "单选"
	case control.RequestPromptFormFieldMultiSelectStatic:
		return "多选"
	default:
		return "文本"
	}
}

func requestQuestionsComplete(view control.FeishuRequestView) bool {
	if len(view.Questions) == 0 {
		return false
	}
	for _, question := range view.Questions {
		if !question.Answered && !question.Skipped {
			return false
		}
	}
	return true
}

func requestStructuredFormComplete(view control.FeishuRequestView) bool {
	if view.StructuredForm == nil || len(view.StructuredForm.Fields) == 0 {
		return false
	}
	for _, field := range view.StructuredForm.Fields {
		if !structuredFormFieldAnswered(field) {
			return false
		}
	}
	return true
}

func requestQuestionNeedsInteractiveChoice(question control.RequestPromptQuestion) bool {
	return len(question.Options) != 0
}

func requestCurrentStructuredFormField(view control.FeishuRequestView) (control.RequestPromptFormField, int, bool) {
	if view.StructuredForm == nil || len(view.StructuredForm.Fields) == 0 {
		return control.RequestPromptFormField{}, 0, false
	}
	index := normalizedRequestCurrentQuestionIndex(view)
	return view.StructuredForm.Fields[index], index, true
}

func structuredFormFieldAnswered(field control.RequestPromptFormField) bool {
	if len(normalizedStructuredFormFieldValues(field)) != 0 {
		return true
	}
	return false
}

func normalizedStructuredFormFieldValues(field control.RequestPromptFormField) []string {
	if len(field.DefaultValues) != 0 {
		out := make([]string, 0, len(field.DefaultValues))
		seen := map[string]bool{}
		for _, value := range field.DefaultValues {
			value = strings.TrimSpace(value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
		return out
	}
	if value := strings.TrimSpace(field.DefaultValue); value != "" {
		return []string{value}
	}
	return nil
}

func requestStructuredFormFieldMarkdown(form *control.RequestPromptStructuredForm, field control.RequestPromptFormField, index int) string {
	total := 0
	if form != nil {
		total = len(form.Fields)
	}
	values := normalizedStructuredFormFieldValues(field)
	lines := []string{"**字段 " + strconv.Itoa(index+1) + "/" + strconv.Itoa(max(1, total)) + "**"}
	title := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.Name))
	if title != "" {
		lines = append(lines, "标题："+title)
	}
	if len(values) == 0 {
		lines = append(lines, "状态：待填写")
	} else {
		lines = append(lines, "状态：已填写")
	}
	switch field.Kind {
	case control.RequestPromptFormFieldSelectStatic:
		lines = append(lines, "类型：单选")
	case control.RequestPromptFormFieldMultiSelectStatic:
		lines = append(lines, "类型：多选")
	default:
		lines = append(lines, "类型：文本")
	}
	if placeholder := strings.TrimSpace(field.Placeholder); placeholder != "" {
		lines = append(lines, "提示："+placeholder)
	}
	if len(values) != 0 {
		lines = append(lines, "当前答案："+strings.Join(values, "、"))
	}
	if len(field.Options) != 0 {
		lines = append(lines, "可选项：")
		for _, option := range field.Options {
			label := firstNonEmpty(strings.TrimSpace(option.Label), strings.TrimSpace(option.Value))
			if label == "" {
				continue
			}
			lines = append(lines, "- "+label)
		}
	}
	return strings.Join(lines, "\n")
}

func requestStructuredFormFieldSupportNote(field control.RequestPromptFormField) string {
	switch field.Kind {
	case control.RequestPromptFormFieldSelectStatic:
		return "当前字段可直接在企微卡片中选择并提交。"
	case control.RequestPromptFormFieldMultiSelectStatic:
		return "当前字段可在企微下拉中多选后提交。提交后会自动跳到下一个未完成字段。"
	default:
		return "当前字段支持直接发送一条企微文本继续填写。"
	}
}

func (p *Projector) projectRequestQuestionFrames(view control.FeishuRequestView, requestID string) []Frame {
	question, index, ok := requestCurrentQuestion(view)
	if !ok {
		return nil
	}
	frames := make([]Frame, 0, 2)
	if card := p.projectRequestQuestionChoiceCard(view, requestID, question, index); card != nil {
		frames = append(frames, cardFrame(card))
	}
	if card := p.projectRequestQuestionControlCard(view, requestID, question, index); card != nil {
		frames = append(frames, cardFrame(card))
	}
	return frames
}

func (p *Projector) projectRequestStructuredFormFrames(view control.FeishuRequestView, requestID string) []Frame {
	field, index, ok := requestCurrentStructuredFormField(view)
	if !ok {
		if card := p.projectRequestOptionCard(firstNonEmpty(strings.TrimSpace(view.Title), "需要确认"), requestID, view.Options, view.RequestRevision, ""); card != nil {
			return []Frame{cardFrame(card)}
		}
		return nil
	}
	frames := make([]Frame, 0, 2)
	if card := p.projectRequestStructuredFormInputCard(view, requestID, field, index); card != nil {
		frames = append(frames, cardFrame(card))
	}
	if card := p.projectRequestStructuredFormControlCard(view, requestID, field, index); card != nil {
		frames = append(frames, cardFrame(card))
	}
	return frames
}

func (p *Projector) projectRequestStructuredFormInputCard(view control.FeishuRequestView, requestID string, field control.RequestPromptFormField, index int) *templateCard {
	if view.Sealed || !frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) {
		return nil
	}
	title := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.Name), "结构化字段")
	desc := "字段 " + strconv.Itoa(index+1)
	switch field.Kind {
	case control.RequestPromptFormFieldSelectStatic, control.RequestPromptFormFieldMultiSelectStatic:
		options := requestStructuredFormNamedOptions(field)
		selectList := selectOptions(options)
		if len(selectList) == 0 {
			return nil
		}
		selectedValues := normalizedStructuredFormFieldValues(field)
		selectedID := ""
		if len(selectedValues) != 0 {
			selectedID = selectedOptionID(selectedValues[0], selectList)
		}
		return &templateCard{
			CardType:  cardTypeMultipleInteraction,
			TaskID:    requestID,
			MainTitle: &cardMainTitle{Title: title, Desc: desc},
			SelectList: []cardSelect{{
				QuestionKey: requestStructuredFormQuestionKey(field.Name),
				Title:       firstNonEmpty(strings.TrimSpace(field.Placeholder), "请选择"),
				SelectedID:  selectedID,
				Multi:       field.Kind == control.RequestPromptFormFieldMultiSelectStatic,
				OptionList:  selectList,
			}},
			SubmitButton: &cardSubmitButton{
				Text: firstNonEmpty(strings.TrimSpace(view.StructuredForm.SubmitLabel), "保存当前字段"),
				Key:  composeEncodedKey(keyPrefixRequestAnswerSubmit, requestRevisionPart(view.RequestRevision), field.Name),
			},
		}
	default:
		return nil
	}
}

func requestStructuredFormNamedOptions(field control.RequestPromptFormField) []namedOption {
	out := make([]namedOption, 0, len(field.Options))
	for _, option := range field.Options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, namedOption{
			Value: value,
			Label: firstNonEmpty(strings.TrimSpace(option.Label), value),
		})
	}
	return out
}

func requestStructuredFormQuestionKey(fieldName string) string {
	fieldName = strings.TrimSpace(fieldName)
	if fieldName == "" {
		return keyPrefixRequestAnswer
	}
	return keyPrefixRequestAnswer + keyValueSep + "form" + keyValueSep + url.QueryEscape(fieldName)
}

func (p *Projector) projectRequestStructuredFormControlCard(view control.FeishuRequestView, requestID string, field control.RequestPromptFormField, index int) *templateCard {
	buttons := make([]cardButton, 0, p.maxButtons)
	if frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) {
		if index > 0 {
			buttons = append(buttons, cardButton{
				Text: "上一个字段",
				Key:  composeEncodedKey(keyPrefixRequestControl, requestRevisionPart(view.RequestRevision), "structured_previous"),
			})
		}
		if structuredFormFieldAnswered(field) && index < len(view.StructuredForm.Fields)-1 {
			buttons = append(buttons, cardButton{
				Text: "下一个字段",
				Key:  composeEncodedKey(keyPrefixRequestControl, requestRevisionPart(view.RequestRevision), "structured_next"),
			})
		}
		if requestStructuredFormComplete(view) {
			buttons = append(buttons, cardButton{
				Text:  firstNonEmpty(strings.TrimSpace(view.StructuredForm.SubmitLabel), "提交"),
				Key:   composeEncodedKey(keyPrefixRequestSubmit, requestRevisionPart(view.RequestRevision)),
				Style: 1,
			})
		}
	}
	buttons = append(buttons, requestOptionButtons(view.Options, view.RequestRevision)...)
	buttons = dedupeCardButtons(buttons)
	if len(buttons) == 0 {
		return nil
	}
	if len(buttons) > p.maxButtons {
		buttons = buttons[:p.maxButtons]
	}
	return &templateCard{
		CardType:   cardTypeButtonInteraction,
		TaskID:     requestID,
		MainTitle:  &cardMainTitle{Title: "可选操作"},
		ButtonList: buttons,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (p *Projector) projectRequestQuestionChoiceCard(view control.FeishuRequestView, requestID string, question control.RequestPromptQuestion, index int) *templateCard {
	if view.Sealed || !frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) || !requestQuestionNeedsInteractiveChoice(question) {
		return nil
	}
	title := firstNonEmpty(strings.TrimSpace(question.Header), strings.TrimSpace(question.Question), requestQuestionLabel(index, len(view.Questions)))
	options := requestQuestionNamedOptions(question)
	if len(options) == 0 {
		return nil
	}
	if len(options) <= p.maxButtons {
		return &templateCard{
			CardType:  cardTypeButtonInteraction,
			TaskID:    requestID,
			MainTitle: &cardMainTitle{Title: title, Desc: requestQuestionLabel(index, len(view.Questions))},
			ButtonList: requestQuestionOptionButtons(
				question,
				options,
				view.RequestRevision,
			),
		}
	}
	selectOptions := selectOptions(options)
	if len(selectOptions) == 0 {
		return nil
	}
	return &templateCard{
		CardType:  cardTypeMultipleInteraction,
		TaskID:    requestID,
		MainTitle: &cardMainTitle{Title: title, Desc: requestQuestionLabel(index, len(view.Questions))},
		SelectList: []cardSelect{{
			QuestionKey: requestAnswerQuestionKey(question.ID),
			Title:       firstNonEmpty(strings.TrimSpace(question.Placeholder), "请选择答案"),
			SelectedID:  selectedOptionID(strings.TrimSpace(question.DefaultValue), selectOptions),
			OptionList:  selectOptions,
		}},
		SubmitButton: &cardSubmitButton{
			Text: "提交答案",
			Key:  composeEncodedKey(keyPrefixRequestAnswerSubmit, requestRevisionPart(view.RequestRevision), question.ID),
		},
	}
}

func requestQuestionNamedOptions(question control.RequestPromptQuestion) []namedOption {
	out := make([]namedOption, 0, len(question.Options))
	for _, option := range question.Options {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			continue
		}
		out = append(out, namedOption{Value: label, Label: label})
	}
	return out
}

func requestQuestionOptionButtons(question control.RequestPromptQuestion, options []namedOption, revision int) []cardButton {
	selectedAnswer := strings.TrimSpace(question.DefaultValue)
	buttons := make([]cardButton, 0, len(options))
	for _, option := range options {
		style := 1
		if selectedAnswer != "" && !strings.EqualFold(selectedAnswer, option.Value) {
			style = 0
		}
		buttons = append(buttons, cardButton{
			Text:  option.Label,
			Key:   composeEncodedKey(keyPrefixRequestAnswer, requestRevisionPart(revision), question.ID, option.Value),
			Style: style,
		})
	}
	return buttons
}

func (p *Projector) projectRequestQuestionControlCard(view control.FeishuRequestView, requestID string, question control.RequestPromptQuestion, index int) *templateCard {
	buttons := make([]cardButton, 0, p.maxButtons)
	if frontstagecontract.AllowsPrimaryInput(view.ActionPolicy) {
		if index > 0 {
			buttons = append(buttons, cardButton{
				Text: "上一题",
				Key:  composeEncodedKey(keyPrefixRequestRespond, requestRevisionPart(view.RequestRevision), frontstagecontract.RequestPromptOptionStepPrevious),
			})
		}
		if index < len(view.Questions)-1 && (question.Answered || question.Skipped) {
			buttons = append(buttons, cardButton{
				Text: "下一题",
				Key:  composeEncodedKey(keyPrefixRequestRespond, requestRevisionPart(view.RequestRevision), frontstagecontract.RequestPromptOptionStepNext),
			})
		}
		if requestQuestionsComplete(view) {
			buttons = append(buttons, cardButton{
				Text:  "重新提交",
				Key:   composeEncodedKey(keyPrefixRequestSubmit, requestRevisionPart(view.RequestRevision)),
				Style: 1,
			})
		}
		if question.Optional && !question.Answered && !question.Skipped {
			buttons = append(buttons, cardButton{
				Text: "跳过",
				Key:  composeEncodedKey(keyPrefixRequestControl, requestRevisionPart(view.RequestRevision), frontstagecontract.RequestControlSkipOptional, question.ID),
			})
		}
	}
	buttons = append(buttons, requestOptionButtons(view.Options, view.RequestRevision)...)
	if frontstagecontract.AllowsCancelAction(view.ActionPolicy) {
		if cancelButton := requestCancelButton(view); cancelButton.Key != "" {
			buttons = append(buttons, cancelButton)
		}
	}
	buttons = dedupeCardButtons(buttons)
	if len(buttons) == 0 {
		return nil
	}
	if len(buttons) > p.maxButtons {
		buttons = buttons[:p.maxButtons]
	}
	return &templateCard{
		CardType:   cardTypeButtonInteraction,
		TaskID:     requestID,
		MainTitle:  &cardMainTitle{Title: "可选操作"},
		ButtonList: buttons,
	}
}

func (p *Projector) projectRequestOptionCard(title, requestID string, options []control.RequestPromptOption, revision int, subtitle string) *templateCard {
	buttons := requestOptionButtons(options, revision)
	if len(buttons) == 0 {
		return nil
	}
	if len(buttons) > p.maxButtons {
		buttons = buttons[:p.maxButtons]
	}
	card := &templateCard{
		CardType:   cardTypeButtonInteraction,
		TaskID:     requestID,
		MainTitle:  &cardMainTitle{Title: title},
		ButtonList: buttons,
	}
	card.SubTitle = strings.TrimSpace(subtitle)
	return card
}

func requestOptionButtons(options []control.RequestPromptOption, revision int) []cardButton {
	buttons := make([]cardButton, 0, len(options))
	for _, option := range options {
		optionID := strings.TrimSpace(option.OptionID)
		if optionID == "" {
			continue
		}
		buttons = append(buttons, cardButton{
			Text:  firstNonEmpty(strings.TrimSpace(option.Label), optionID),
			Key:   composeEncodedKey(keyPrefixRequestRespond, requestRevisionPart(revision), optionID),
			Style: requestButtonStyle(option.Style),
		})
	}
	return buttons
}

func requestCancelButton(view control.FeishuRequestView) cardButton {
	requestType := normalizeRequestTypeToken(view.RequestType)
	switch requestType {
	case "request_user_input":
		return cardButton{
			Text: "取消",
			Key:  composeEncodedKey(keyPrefixRequestControl, requestRevisionPart(view.RequestRevision), frontstagecontract.RequestControlCancelTurn),
		}
	case "mcp_server_elicitation":
		if view.ActionPolicy == frontstagecontract.ActionPolicyCancelOnly {
			return cardButton{
				Text: "取消",
				Key:  composeEncodedKey(keyPrefixRequestControl, requestRevisionPart(view.RequestRevision), frontstagecontract.RequestControlCancelRequest),
			}
		}
	}
	return cardButton{}
}

func normalizeRequestTypeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func dedupeCardButtons(buttons []cardButton) []cardButton {
	out := make([]cardButton, 0, len(buttons))
	seen := map[string]bool{}
	for _, button := range buttons {
		key := strings.TrimSpace(button.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, button)
	}
	return out
}

func requestRevisionPart(revision int) string {
	if revision <= 0 {
		return ""
	}
	return strconv.Itoa(revision)
}

func requestAnswerQuestionKey(questionID string) string {
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return keyPrefixRequestAnswer
	}
	return keyPrefixRequestAnswer + keyValueSep + url.QueryEscape(questionID)
}

func composeEncodedKey(prefix string, parts ...string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(prefix)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b.WriteString(keyValueSep)
		b.WriteString(url.QueryEscape(part))
	}
	return b.String()
}

func splitEncodedKeyParts(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	rawParts := strings.Split(value, keyValueSep)
	out := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		decoded, err := url.QueryUnescape(part)
		if err != nil {
			out = append(out, part)
			continue
		}
		out = append(out, strings.TrimSpace(decoded))
	}
	return out
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
