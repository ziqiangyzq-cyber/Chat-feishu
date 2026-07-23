package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/gitmeta"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

const workspaceSurfaceContextDir = ".codex-remote"
const workspaceSurfaceContextFile = "surface-context.json"

type workspaceSurfaceContextPayload struct {
	SurfaceSessionID string    `json:"surface_session_id"`
	GatewayID        string    `json:"gateway_id,omitempty"`
	ChatID           string    `json:"chat_id,omitempty"`
	ActorUserID      string    `json:"actor_user_id,omitempty"`
	WorkspaceKey     string    `json:"workspace_key,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type workspaceSurfaceContextWriteRequest struct {
	desired     map[string]workspaceSurfaceContextPayload
	removeRoots map[string]struct{}
	sequence    uint64
}

type workspaceSurfaceContextWriter struct {
	mu                sync.Mutex
	pending           *workspaceSurfaceContextWriteRequest
	wake              chan struct{}
	progress          chan struct{}
	nextSequence      uint64
	completedSequence uint64
	apply             func(workspaceSurfaceContextWriteRequest)
}

func newWorkspaceSurfaceContextWriter() *workspaceSurfaceContextWriter {
	writer := &workspaceSurfaceContextWriter{
		wake:     make(chan struct{}, 1),
		progress: make(chan struct{}),
	}
	writer.apply = applyWorkspaceSurfaceContextWrite
	go writer.run()
	return writer
}

// enqueue replaces queued work with the latest desired snapshot. It never waits
// for filesystem I/O: a blocked workspace filesystem must not block App.mu,
// ingress handling, or the daemon heartbeat.
func (w *workspaceSurfaceContextWriter) enqueue(request workspaceSurfaceContextWriteRequest) {
	if w == nil {
		return
	}
	request = cloneWorkspaceSurfaceContextWriteRequest(request)

	w.mu.Lock()
	w.nextSequence++
	request.sequence = w.nextSequence
	if w.pending != nil {
		for workspaceRoot := range w.pending.removeRoots {
			request.removeRoots[workspaceRoot] = struct{}{}
		}
	}
	for workspaceRoot := range request.desired {
		delete(request.removeRoots, workspaceRoot)
	}
	w.pending = &request
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// flush waits outside App.mu for writes already queued at the time of the call.
// Its timeout keeps a wedged filesystem isolated from request processing.
func (w *workspaceSurfaceContextWriter) flush(timeout time.Duration) {
	if w == nil || timeout <= 0 {
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	w.mu.Lock()
	target := w.nextSequence
	for w.completedSequence < target {
		progress := w.progress
		w.mu.Unlock()
		select {
		case <-progress:
		case <-deadline.C:
			return
		}
		w.mu.Lock()
	}
	w.mu.Unlock()
}

func (w *workspaceSurfaceContextWriter) run() {
	for range w.wake {
		for {
			w.mu.Lock()
			request := w.pending
			w.pending = nil
			w.mu.Unlock()
			if request == nil {
				break
			}
			w.apply(*request)
			w.mu.Lock()
			if request.sequence > w.completedSequence {
				w.completedSequence = request.sequence
			}
			close(w.progress)
			w.progress = make(chan struct{})
			w.mu.Unlock()
		}
	}
}

func cloneWorkspaceSurfaceContextWriteRequest(request workspaceSurfaceContextWriteRequest) workspaceSurfaceContextWriteRequest {
	cloned := workspaceSurfaceContextWriteRequest{
		desired:     make(map[string]workspaceSurfaceContextPayload, len(request.desired)),
		removeRoots: make(map[string]struct{}, len(request.removeRoots)),
	}
	for workspaceRoot, payload := range request.desired {
		cloned.desired[workspaceRoot] = payload
	}
	for workspaceRoot := range request.removeRoots {
		cloned.removeRoots[workspaceRoot] = struct{}{}
	}
	return cloned
}

func applyWorkspaceSurfaceContextWrite(request workspaceSurfaceContextWriteRequest) {
	for workspaceRoot, payload := range request.desired {
		if err := writeWorkspaceSurfaceContext(workspaceRoot, payload); err != nil {
			log.Printf("write workspace surface context failed: workspace=%s err=%v", workspaceRoot, err)
			continue
		}
		if err := ensureWorkspaceContextGitExclude(workspaceRoot); err != nil {
			log.Printf("ensure workspace context git exclude failed: workspace=%s err=%v", workspaceRoot, err)
		}
	}
	for workspaceRoot := range request.removeRoots {
		if err := removeWorkspaceSurfaceContext(workspaceRoot); err != nil {
			log.Printf("remove workspace surface context failed: workspace=%s err=%v", workspaceRoot, err)
		}
	}
}

func (a *App) syncWorkspaceSurfaceContextFilesLocked() {
	desired := map[string]workspaceSurfaceContextPayload{}
	for _, surface := range a.service.Surfaces() {
		if surface == nil || state.NormalizeProductMode(surface.ProductMode) != state.ProductModeNormal {
			continue
		}
		inst := a.service.Instance(strings.TrimSpace(surface.AttachedInstanceID))
		if inst == nil {
			continue
		}
		workspaceRoot := state.NormalizeWorkspaceKey(inst.WorkspaceRoot)
		if workspaceRoot == "" {
			continue
		}
		if _, exists := desired[workspaceRoot]; exists {
			log.Printf("workspace surface context conflict: workspace=%s existing=%s ignored=%s", workspaceRoot, desired[workspaceRoot].SurfaceSessionID, surface.SurfaceSessionID)
			continue
		}
		desired[workspaceRoot] = workspaceSurfaceContextPayload{
			SurfaceSessionID: strings.TrimSpace(surface.SurfaceSessionID),
			GatewayID:        strings.TrimSpace(surface.GatewayID),
			ChatID:           strings.TrimSpace(surface.ChatID),
			ActorUserID:      strings.TrimSpace(surface.ActorUserID),
			WorkspaceKey:     state.ResolveWorkspaceKey(inst.WorkspaceKey, workspaceRoot),
			UpdatedAt:        time.Now().UTC(),
		}
	}

	removeRoots := map[string]struct{}{}
	for workspaceRoot := range a.surfaceResumeRuntime.workspaceContextRoots {
		if _, ok := desired[workspaceRoot]; ok {
			continue
		}
		removeRoots[workspaceRoot] = struct{}{}
	}

	a.surfaceResumeRuntime.workspaceContextRoots = map[string]string{}
	for workspaceRoot, payload := range desired {
		a.surfaceResumeRuntime.workspaceContextRoots[workspaceRoot] = payload.SurfaceSessionID
	}
	a.workspaceContextWriter.enqueue(workspaceSurfaceContextWriteRequest{
		desired:     desired,
		removeRoots: removeRoots,
	})
}

func (a *App) clearWorkspaceSurfaceContextFilesLocked() {
	removeRoots := make(map[string]struct{}, len(a.surfaceResumeRuntime.workspaceContextRoots))
	for workspaceRoot := range a.surfaceResumeRuntime.workspaceContextRoots {
		removeRoots[workspaceRoot] = struct{}{}
	}
	a.surfaceResumeRuntime.workspaceContextRoots = map[string]string{}
	a.workspaceContextWriter.enqueue(workspaceSurfaceContextWriteRequest{
		desired:     map[string]workspaceSurfaceContextPayload{},
		removeRoots: removeRoots,
	})
}

func workspaceSurfaceContextPath(workspaceRoot string) string {
	return filepath.Join(strings.TrimSpace(workspaceRoot), workspaceSurfaceContextDir, workspaceSurfaceContextFile)
}

func writeWorkspaceSurfaceContext(workspaceRoot string, payload workspaceSurfaceContextPayload) error {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return writeJSONFileAtomic(workspaceSurfaceContextPath(workspaceRoot), payload, 0o600)
}

func removeWorkspaceSurfaceContext(workspaceRoot string) error {
	path := workspaceSurfaceContextPath(workspaceRoot)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ensureWorkspaceContextGitExclude(workspaceRoot string) error {
	info, err := gitmeta.LocateWorkspace(workspaceRoot)
	if err != nil || !info.InRepo() {
		return err
	}
	rel, err := filepath.Rel(info.RepoRoot, workspaceRoot)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	pattern := "/" + workspaceSurfaceContextDir + "/"
	if rel != "." && rel != "" {
		pattern = "/" + rel + "/" + workspaceSurfaceContextDir + "/"
	}
	excludePath := filepath.Join(info.GitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}
	if gitmeta.FileHasExactTrimmedLine(excludePath, pattern) {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(pattern + "\n")
	return err
}

func readWorkspaceSurfaceContext(path string) (workspaceSurfaceContextPayload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return workspaceSurfaceContextPayload{}, err
	}
	var payload workspaceSurfaceContextPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return workspaceSurfaceContextPayload{}, err
	}
	return payload, nil
}
