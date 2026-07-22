package orchestrator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func requestActionFromAction(action control.Action) *control.ActionRequestResponse {
	return action.Request
}

func requestControlFromAction(action control.Action) *control.ActionRequestControl {
	if action.RequestControl != nil {
		return action.RequestControl
	}
	return nil
}

func buildRequestUserInputResponse(request *state.RequestPromptRecord, rawAnswers map[string][]string) (map[string]any, bool, string) {
	if request == nil || len(request.Questions) == 0 {
		return nil, false, "这个问题请求缺少有效的问题定义，当前无法提交。"
	}
	if request.DraftAnswers == nil {
		request.DraftAnswers = map[string]string{}
	}
	if request.SkippedQuestionIDs == nil {
		request.SkippedQuestionIDs = map[string]bool{}
	}
	for _, question := range request.Questions {
		questionID := strings.TrimSpace(question.ID)
		if questionID == "" {
			continue
		}
		answerText := firstTrimmedAnswer(rawAnswers[questionID])
		if answerText == "" {
			continue
		}
		if canonical, ok := canonicalQuestionOptionAnswer(question, answerText); ok {
			answerText = canonical
		} else if len(question.Options) != 0 && !question.AllowOther {
			label := firstNonEmpty(strings.TrimSpace(question.Header), strings.TrimSpace(question.Question), questionID)
			return nil, false, fmt.Sprintf("问题“%s”的答案不在可选项中。", label)
		}
		request.DraftAnswers[questionID] = answerText
		delete(request.SkippedQuestionIDs, questionID)
	}
	answers := map[string]any{}
	complete := true
	for _, question := range request.Questions {
		questionID := strings.TrimSpace(question.ID)
		if questionID == "" {
			continue
		}
		answerText := strings.TrimSpace(request.DraftAnswers[questionID])
		if answerText == "" {
			if question.Optional && requestQuestionSkipped(request, question) {
				continue
			}
			complete = false
			continue
		}
		if canonical, ok := canonicalQuestionOptionAnswer(question, answerText); ok {
			answerText = canonical
		} else if len(question.Options) != 0 && !question.AllowOther {
			label := firstNonEmpty(strings.TrimSpace(question.Header), strings.TrimSpace(question.Question), questionID)
			return nil, false, fmt.Sprintf("问题“%s”的答案不在可选项中。", label)
		}
		answers[questionID] = map[string]any{"answers": []string{answerText}}
	}
	if !complete {
		return nil, false, ""
	}
	return map[string]any{"answers": answers}, true, ""
}

func requestPromptStepPrevious(optionID string) bool {
	return normalizedRequestControl(optionID) == normalizedRequestControl(frontstagecontract.RequestPromptOptionStepPrevious)
}

func requestPromptStepNext(optionID string) bool {
	return normalizedRequestControl(optionID) == normalizedRequestControl(frontstagecontract.RequestPromptOptionStepNext)
}

func normalizedRequestControl(optionID string) string {
	return frontstagecontract.NormalizeRequestControlToken(optionID)
}

func bumpRequestCardRevision(request *state.RequestPromptRecord) {
	if request == nil {
		return
	}
	request.CardRevision++
	if request.CardRevision <= 0 {
		request.CardRevision = 1
	}
}

func (s *Service) nextRequestDispatchCommandID() string {
	s.nextRequestCommandID++
	return "reqcmd-" + strconv.Itoa(s.nextRequestCommandID)
}

func requestPromptQuestionCount(request *state.RequestPromptRecord) int {
	if request == nil {
		return 0
	}
	return len(request.Questions)
}

func requestPromptEditableItemCount(request *state.RequestPromptRecord) int {
	if request == nil {
		return 0
	}
	if len(request.Questions) != 0 {
		return len(request.Questions)
	}
	if request.StructuredForm != nil {
		return len(request.StructuredForm.Fields)
	}
	return 0
}

func normalizedRequestPromptCurrentQuestionIndex(request *state.RequestPromptRecord) int {
	count := requestPromptEditableItemCount(request)
	if count == 0 {
		return 0
	}
	if request.CurrentQuestionIndex < 0 {
		return 0
	}
	if request.CurrentQuestionIndex >= count {
		return count - 1
	}
	return request.CurrentQuestionIndex
}

func setRequestPromptCurrentQuestionIndex(request *state.RequestPromptRecord, index int) {
	count := requestPromptEditableItemCount(request)
	if count == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= count {
		index = count - 1
	}
	request.CurrentQuestionIndex = index
}

func moveRequestPromptCurrentQuestion(request *state.RequestPromptRecord, delta int) {
	setRequestPromptCurrentQuestionIndex(request, normalizedRequestPromptCurrentQuestionIndex(request)+delta)
}

func requestQuestionAnswered(request *state.RequestPromptRecord, question state.RequestPromptQuestionRecord) bool {
	if request == nil {
		return false
	}
	return strings.TrimSpace(request.DraftAnswers[strings.TrimSpace(question.ID)]) != ""
}

func requestQuestionSkipped(request *state.RequestPromptRecord, question state.RequestPromptQuestionRecord) bool {
	if request == nil || request.SkippedQuestionIDs == nil {
		return false
	}
	return request.SkippedQuestionIDs[strings.TrimSpace(question.ID)]
}

func requestQuestionCompleted(request *state.RequestPromptRecord, question state.RequestPromptQuestionRecord) bool {
	if requestQuestionAnswered(request, question) {
		return true
	}
	return question.Optional && requestQuestionSkipped(request, question)
}

func firstIncompleteRequestQuestionIndex(request *state.RequestPromptRecord) int {
	if request == nil {
		return 0
	}
	for index, question := range request.Questions {
		if !requestQuestionCompleted(request, question) {
			return index
		}
	}
	return normalizedRequestPromptCurrentQuestionIndex(request)
}

func requestPromptQuestionsComplete(request *state.RequestPromptRecord) bool {
	if request == nil || len(request.Questions) == 0 {
		return false
	}
	for _, question := range request.Questions {
		if !requestQuestionCompleted(request, question) {
			return false
		}
	}
	return true
}

func requestCurrentQuestionPendingText(request *state.RequestPromptRecord) string {
	question, _, ok := requestPromptCurrentQuestionRecord(request)
	if !ok {
		return "当前没有可提交的答案。"
	}
	label := firstNonEmpty(strings.TrimSpace(question.Header), strings.TrimSpace(question.Question), strings.TrimSpace(question.ID))
	if question.Optional {
		return fmt.Sprintf("问题“%s”还没有处理。你可以先填写答案，或直接跳过。", label)
	}
	return fmt.Sprintf("问题“%s”还没有填写答案。", label)
}

func requestPromptCurrentQuestionRecord(request *state.RequestPromptRecord) (state.RequestPromptQuestionRecord, int, bool) {
	if request == nil || len(request.Questions) == 0 {
		return state.RequestPromptQuestionRecord{}, 0, false
	}
	index := normalizedRequestPromptCurrentQuestionIndex(request)
	return request.Questions[index], index, true
}

func requestPromptCurrentStructuredFormField(request *state.RequestPromptRecord) (state.RequestPromptFormFieldRecord, int, bool) {
	if request == nil || request.StructuredForm == nil || len(request.StructuredForm.Fields) == 0 {
		return state.RequestPromptFormFieldRecord{}, 0, false
	}
	index := normalizedRequestPromptCurrentQuestionIndex(request)
	return request.StructuredForm.Fields[index], index, true
}

func requestPromptStructuredFormFieldByName(form *state.RequestPromptStructuredFormRecord, fieldName string) *state.RequestPromptFormFieldRecord {
	if form == nil {
		return nil
	}
	fieldName = strings.TrimSpace(fieldName)
	for i := range form.Fields {
		if strings.TrimSpace(form.Fields[i].Name) == fieldName {
			return &form.Fields[i]
		}
	}
	return nil
}

func normalizeStructuredFormInputValues(field state.RequestPromptFormFieldRecord, raw []string) []string {
	switch field.Kind {
	case state.RequestPromptFormFieldText, state.RequestPromptFormFieldSelectStatic:
		if value := firstTrimmedAnswer(raw); value != "" {
			return []string{value}
		}
		return nil
	default:
		return normalizedStructuredDraftValues(raw)
	}
}

func validateStructuredFormFieldValues(field state.RequestPromptFormFieldRecord, values []string) string {
	label := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.Name))
	if label == "" {
		label = "当前字段"
	}
	switch field.Kind {
	case state.RequestPromptFormFieldText:
		if len(values) > 1 {
			return fmt.Sprintf("“%s”一次只能填写一条文本。", label)
		}
	case state.RequestPromptFormFieldSelectStatic:
		if len(values) > 1 {
			return fmt.Sprintf("“%s”只能选择一项。", label)
		}
	}
	if len(values) == 0 {
		return ""
	}
	if len(field.Options) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(field.Options))
	for _, option := range field.Options {
		allowed[strings.TrimSpace(option.Value)] = true
	}
	for _, value := range values {
		if !allowed[strings.TrimSpace(value)] {
			return fmt.Sprintf("“%s”的选择项无效。", label)
		}
	}
	return ""
}

func structuredFormFieldCurrentValues(request *state.RequestPromptRecord, field state.RequestPromptFormFieldRecord) []string {
	if request == nil {
		return nil
	}
	fieldName := strings.TrimSpace(field.Name)
	if fieldName == "" {
		return nil
	}
	if values := normalizedStructuredDraftValues(request.StructuredDraftAnswers[fieldName]); len(values) != 0 {
		return values
	}
	if values := normalizedStructuredDraftValues(field.DefaultValues); len(values) != 0 {
		return values
	}
	if value := strings.TrimSpace(field.DefaultValue); value != "" {
		return []string{value}
	}
	return nil
}

func requestPromptStructuredFormFieldAnswered(request *state.RequestPromptRecord, field state.RequestPromptFormFieldRecord) bool {
	return len(structuredFormFieldCurrentValues(request, field)) != 0
}

func requestPromptStructuredFormComplete(request *state.RequestPromptRecord) bool {
	if request == nil || request.StructuredForm == nil || len(request.StructuredForm.Fields) == 0 {
		return false
	}
	for _, field := range request.StructuredForm.Fields {
		if !requestPromptStructuredFormFieldAnswered(request, field) {
			return false
		}
	}
	return true
}

func firstIncompleteStructuredFormFieldIndex(request *state.RequestPromptRecord) int {
	if request == nil || request.StructuredForm == nil || len(request.StructuredForm.Fields) == 0 {
		return 0
	}
	for index, field := range request.StructuredForm.Fields {
		if !requestPromptStructuredFormFieldAnswered(request, field) {
			return index
		}
	}
	return normalizedRequestPromptCurrentQuestionIndex(request)
}

func currentStructuredFormFieldPendingText(request *state.RequestPromptRecord) string {
	field, _, ok := requestPromptCurrentStructuredFormField(request)
	if !ok {
		return "当前没有可处理的表单字段。"
	}
	label := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.Name))
	if label == "" {
		label = "当前字段"
	}
	switch field.Kind {
	case state.RequestPromptFormFieldMultiSelectStatic:
		return fmt.Sprintf("字段“%s”还没有完成选择。请先选择至少一项，再继续。", label)
	default:
		return fmt.Sprintf("字段“%s”还没有填写完成。", label)
	}
}

func applyRequestStructuredFormAnswers(request *state.RequestPromptRecord, rawAnswers map[string][]string) ([]string, string) {
	if request == nil || request.StructuredForm == nil {
		return nil, "当前结构化表单已经失效，请重新打开后再试。"
	}
	if request.StructuredDraftAnswers == nil {
		request.StructuredDraftAnswers = map[string][]string{}
	}
	updated := make([]string, 0, len(rawAnswers))
	for fieldName, rawValues := range rawAnswers {
		fieldName = strings.TrimSpace(fieldName)
		if fieldName == "" {
			continue
		}
		field := requestPromptStructuredFormFieldByName(request.StructuredForm, fieldName)
		if field == nil {
			return nil, "当前表单字段已变化，请使用最新卡片继续。"
		}
		values := normalizeStructuredFormInputValues(*field, rawValues)
		if errText := validateStructuredFormFieldValues(*field, values); errText != "" {
			return nil, errText
		}
		if len(values) == 0 {
			delete(request.StructuredDraftAnswers, fieldName)
		} else {
			request.StructuredDraftAnswers[fieldName] = values
		}
		updated = append(updated, fieldName)
	}
	return updated, ""
}

func markRequestQuestionSkipped(request *state.RequestPromptRecord, questionID string) {
	if request == nil {
		return
	}
	if request.SkippedQuestionIDs == nil {
		request.SkippedQuestionIDs = map[string]bool{}
	}
	questionID = strings.TrimSpace(questionID)
	if questionID == "" {
		return
	}
	request.SkippedQuestionIDs[questionID] = true
	delete(request.DraftAnswers, questionID)
}
