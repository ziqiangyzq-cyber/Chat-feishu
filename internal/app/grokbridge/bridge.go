package grokbridge

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
	"time"

	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

type bridge struct {
	out            io.Writer
	errOut         io.Writer
	writeMu        sync.Mutex
	cwd            string
	sessionID      string
	permissionMode string
	model          string
	effort         string
	turnCancel     context.CancelFunc
}

type turnResult struct {
	sessionID string
	err       error
}

func RunMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := &bridge{out: stdout, errOut: stderr, permissionMode: "bypassPermissions"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			if i+1 < len(args) {
				i++
				b.sessionID = strings.TrimSpace(args[i])
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
			if result.sessionID != "" {
				b.sessionID = result.sessionID
			}
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				_, _ = fmt.Fprintf(b.errOut, "grok turn failed: %v\n", result.err)
			}
		case line := <-lines:
			var frame map[string]any
			if json.Unmarshal(line, &frame) != nil {
				continue
			}
			switch stringValue(frame["type"]) {
			case "control_request":
				b.handleControlRequest(frame)
			case "user":
				if busy {
					b.emitResult("error_during_execution", "Grok 当前仍在执行，暂不支持 turn steer。")
					continue
				}
				prompt := promptFromClaudeFrame(frame)
				if prompt == "" {
					b.emitResult("error_during_execution", "Grok prompt 为空。")
					continue
				}
				turnCtx, cancel := context.WithCancel(ctx)
				b.turnCancel = cancel
				busy = true
				sessionID, permissionMode, model, effort := b.sessionID, b.permissionMode, b.model, b.effort
				go func() {
					id, err := b.runTurn(turnCtx, prompt, sessionID, permissionMode, model, effort)
					turnDone <- turnResult{sessionID: id, err: err}
				}()
			}
		}
	}
}

func scanLines(r io.Reader, lines chan<- []byte, errs chan<- error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines <- append([]byte(nil), scanner.Bytes()...)
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
	switch stringValue(request["subtype"]) {
	case "set_permission_mode":
		if mode := stringValue(request["mode"]); mode != "" {
			b.permissionMode = mode
		}
	case "agy_config", "grok_config":
		b.model = stringValue(request["model"])
		b.effort = stringValue(request["effort"])
	case "interrupt":
		if b.turnCancel != nil {
			b.turnCancel()
		}
	}
	b.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": requestID, "response": map[string]any{}}})
}

func (b *bridge) runTurn(ctx context.Context, prompt, sessionID, permissionMode, model, effort string) (string, error) {
	promptPath, err := writePromptFile(prompt)
	if err != nil {
		b.emitResult("error_during_execution", "无法准备 Grok prompt："+err.Error())
		return sessionID, err
	}
	defer os.Remove(promptPath)

	args := []string{"--prompt-file", promptPath, "--output-format", "streaming-messages-json", "--max-turns", "1", "--cwd", b.cwd}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}
	switch permissionMode {
	case "plan":
		args = append(args, "--permission-mode", "plan")
	case "default":
		args = append(args, "--permission-mode", "acceptEdits")
	default:
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	cmd := execlaunch.CommandContext(ctx, resolveGrokBinary(), args...)
	relayruntime.PrepareManagedProcess(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return relayruntime.TerminateManagedProcess(cmd.Process.Pid, 2*time.Second)
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = b.cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return sessionID, err
	}
	cmd.Stderr = b.errOut
	if err := cmd.Start(); err != nil {
		b.emitResult("error_during_execution", "无法启动 Grok CLI："+err.Error())
		return sessionID, err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	latestID := sessionID
	sawResult := false
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var event map[string]any
		if json.Unmarshal(line, &event) == nil {
			if id := stringValue(event["session_id"]); id != "" {
				latestID = id
			}
			if stringValue(event["type"]) == "result" {
				sawResult = true
			}
		}
		b.writeRaw(line)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		b.emitResult("error_during_execution", "Grok turn 已中断。")
		return latestID, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		b.emitResult("error_during_execution", err.Error())
		return latestID, err
	}
	if waitErr != nil && !sawResult {
		b.emitResult("error_during_execution", "Grok turn 执行失败："+waitErr.Error())
	}
	return latestID, waitErr
}

func writePromptFile(prompt string) (string, error) {
	file, err := os.CreateTemp("", "codex-remote-grok-prompt-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(prompt); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (b *bridge) emitResult(subtype, result string) {
	b.emit(map[string]any{"type": "result", "subtype": subtype, "is_error": subtype != "success", "result": result})
}

func (b *bridge) emit(value map[string]any) {
	data, err := json.Marshal(value)
	if err == nil {
		b.writeRaw(data)
	}
}

func (b *bridge) writeRaw(data []byte) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, _ = b.out.Write(append(data, '\n'))
}

func resolveGrokBinary() string {
	if value := strings.TrimSpace(os.Getenv("GROK_BINARY")); value != "" {
		return value
	}
	if value, err := exec.LookPath("grok"); err == nil {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "bin", "grok")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return "grok"
}

func promptFromClaudeFrame(frame map[string]any) string {
	message := mapValue(frame["message"])
	if text, ok := message["content"].(string); ok {
		return strings.TrimSpace(text)
	}
	items, _ := message["content"].([]any)
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
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func mapValue(value any) map[string]any { result, _ := value.(map[string]any); return result }
func stringValue(value any) string      { text, _ := value.(string); return strings.TrimSpace(text) }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
