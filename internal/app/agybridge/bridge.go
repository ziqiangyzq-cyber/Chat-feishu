package agybridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
)

type bridge struct {
	out            io.Writer
	errOut         io.Writer
	writeMu        sync.Mutex
	cwd            string
	conversationID string
	permissionMode string
	model          string
	effort         string
	turnCancel     context.CancelFunc
}

type turnResult struct {
	conversationID string
	err            error
}

func RunMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := &bridge{out: stdout, errOut: stderr, permissionMode: "bypassPermissions"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--conversation":
			if i+1 < len(args) {
				i++
				b.conversationID = strings.TrimSpace(args[i])
			}
		case "--cwd":
			if i+1 < len(args) {
				i++
				b.cwd = strings.TrimSpace(args[i])
			}
		}
	}
	if b.cwd == "" {
		b.cwd, _ = os.Getwd()
	}

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go scanLines(stdin, lines, readErr)
	turnDone := make(chan turnResult, 1)
	busy := false

	for {
		select {
		case <-ctx.Done():
			if b.turnCancel != nil {
				b.turnCancel()
			}
			return ctx.Err()
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				if b.turnCancel != nil {
					b.turnCancel()
				}
				return nil
			}
			return err
		case result := <-turnDone:
			busy = false
			b.turnCancel = nil
			if result.conversationID != "" {
				b.conversationID = result.conversationID
			}
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				_, _ = fmt.Fprintf(b.errOut, "agy turn failed: %v\n", result.err)
			}
		case line := <-lines:
			var frame map[string]any
			if err := json.Unmarshal(line, &frame); err != nil {
				continue
			}
			typeName := stringValue(frame["type"])
			switch typeName {
			case "control_request":
				b.handleControlRequest(frame)
			case "user":
				if busy {
					b.emitResult("error_during_execution", "Antigravity 当前仍在执行，暂不支持 turn steer。", nil)
					continue
				}
				prompt := promptFromClaudeFrame(frame)
				if strings.TrimSpace(prompt) == "" {
					b.emitResult("error_during_execution", "Antigravity prompt 为空。", nil)
					continue
				}
				turnCtx, cancel := context.WithCancel(ctx)
				b.turnCancel = cancel
				busy = true
				conversationID := b.conversationID
				permissionMode := b.permissionMode
				model := b.model
				effort := b.effort
				go func() {
					conversation, err := b.runTurn(turnCtx, prompt, conversationID, permissionMode, model, effort)
					turnDone <- turnResult{conversationID: conversation, err: err}
				}()
			}
		}
	}
}

func scanLines(r io.Reader, lines chan<- []byte, errs chan<- error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lines <- line
	}
	if err := scanner.Err(); err != nil {
		errs <- err
		return
	}
	errs <- io.EOF
}

func (b *bridge) handleControlRequest(frame map[string]any) {
	requestID := stringValue(frame["request_id"])
	request := mapValue(frame["request"])
	subtype := stringValue(request["subtype"])
	if subtype == "set_permission_mode" {
		if mode := stringValue(request["mode"]); mode != "" {
			b.permissionMode = mode
		}
	}
	if subtype == "agy_config" {
		b.model = stringValue(request["model"])
		b.effort = stringValue(request["effort"])
	}
	if subtype == "interrupt" && b.turnCancel != nil {
		b.turnCancel()
	}
	b.emit(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{},
		},
	})
}

func (b *bridge) runTurn(ctx context.Context, prompt, conversationID, permissionMode, model, effort string) (string, error) {
	binary := resolveAgyBinary()
	args := []string{"--print", prompt, "--output-format", "stream-json", "--print-timeout", "5m"}
	if conversationID != "" {
		args = append(args, "--conversation", conversationID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	switch permissionMode {
	case "plan":
		args = append(args, "--mode", "plan")
	case "default":
		args = append(args, "--mode", "accept-edits")
	default:
		args = append(args, "--mode", "accept-edits", "--dangerously-skip-permissions")
	}
	cmd := execlaunch.CommandContext(ctx, binary, args...)
	cmd.Dir = b.cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return conversationID, err
	}
	cmd.Stderr = b.errOut
	if err := cmd.Start(); err != nil {
		b.emitResult("error_during_execution", "无法启动 Antigravity CLI："+err.Error(), nil)
		return conversationID, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	started := false
	blockStarted := false
	latestConversation := conversationID
	finalResponse := ""
	var finalUsage map[string]any
	status := "SUCCESS"
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch stringValue(event["event"]) {
		case "init":
			if id := stringValue(event["conversation_id"]); id != "" {
				latestConversation = id
			}
			b.emitSystemInit(latestConversation, permissionMode)
		case "step_update":
			update := mapValue(event["step_update"])
			if stringValue(update["step_type"]) != "agent_response" {
				continue
			}
			delta := stringValue(update["text_delta"])
			if delta == "" {
				continue
			}
			if !started {
				b.emitMessageStart(latestConversation)
				started = true
			}
			if !blockStarted {
				b.emitStreamEvent(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
				blockStarted = true
			}
			b.emitStreamEvent(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": delta}})
		case "result":
			result := mapValue(event["result"])
			if id := stringValue(result["conversation_id"]); id != "" {
				latestConversation = id
			}
			status = stringValue(result["status"])
			finalResponse = stringValue(result["response"])
			finalUsage = mapValue(result["usage"])
		}
	}
	waitErr := cmd.Wait()
	if blockStarted {
		b.emitStreamEvent(map[string]any{"type": "content_block_stop", "index": 0})
	}
	if ctx.Err() != nil {
		b.emitResult("error_during_execution", "Antigravity turn 已中断。", nil)
		return latestConversation, ctx.Err()
	}
	if scanErr := scanner.Err(); scanErr != nil {
		b.emitResult("error_during_execution", scanErr.Error(), nil)
		return latestConversation, scanErr
	}
	if waitErr != nil || !strings.EqualFold(status, "SUCCESS") {
		message := firstNonEmpty(finalResponse, errorText(waitErr), "Antigravity turn 执行失败。")
		b.emitResult("error_during_execution", message, finalUsage)
		return latestConversation, waitErr
	}
	b.emitResult("success", finalResponse, finalUsage)
	return latestConversation, nil
}

func (b *bridge) emitSystemInit(conversationID, permissionMode string) {
	b.emit(map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     conversationID,
		"cwd":            b.cwd,
		"model":          b.model,
		"permissionMode": permissionMode,
	})
}

func (b *bridge) emitMessageStart(conversationID string) {
	b.emitStreamEvent(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "agy-" + conversationID, "type": "message", "role": "assistant", "model": b.model, "content": []any{},
		},
	})
}

func (b *bridge) emitStreamEvent(event map[string]any) {
	b.emit(map[string]any{"type": "stream_event", "event": event})
}

func (b *bridge) emitResult(subtype, result string, usage map[string]any) {
	convertedUsage := map[string]any{}
	if len(usage) != 0 {
		convertedUsage["input_tokens"] = usage["input_tokens"]
		convertedUsage["output_tokens"] = usage["output_tokens"]
		convertedUsage["cache_read_input_tokens"] = usage["cache_read_tokens"]
	}
	b.emit(map[string]any{
		"type": "result", "subtype": subtype, "is_error": subtype != "success", "result": result, "usage": convertedUsage,
	})
}

func (b *bridge) emit(value map[string]any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, _ = b.out.Write(append(data, '\n'))
}

func resolveAgyBinary() string {
	if value := strings.TrimSpace(os.Getenv("AGY_BINARY")); value != "" {
		return value
	}
	if value, err := exec.LookPath("agy"); err == nil {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "bin", "agy")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return "agy"
}

func promptFromClaudeFrame(frame map[string]any) string {
	message := mapValue(frame["message"])
	content := message["content"]
	if text, ok := content.(string); ok {
		return text
	}
	items, _ := content.([]any)
	parts := make([]string, 0, len(items))
	for _, raw := range items {
		item := mapValue(raw)
		switch stringValue(item["type"]) {
		case "text":
			if text := stringValue(item["text"]); text != "" {
				parts = append(parts, text)
			}
		case "image":
			source := mapValue(item["source"])
			if path := firstNonEmpty(stringValue(source["path"]), stringValue(item["path"])); path != "" {
				parts = append(parts, "[Local image: "+path+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
