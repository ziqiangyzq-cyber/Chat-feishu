package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

// maxSurfaceTabs caps how many virtual tabs a single chat can open.
const maxSurfaceTabs = 9

// surfaceTabRecord tracks the virtual tab state of one base surface (one
// Feishu chat). Slot 1 is the base surface itself; slots 2..N map to
// "<base>#tabN" virtual surfaces. Every slot is an independent daemon-side
// surface with its own attached workspace/thread/queue, so tasks in other
// tabs keep running when the user switches.
type surfaceTabRecord struct {
	Active int   `json:"active"`
	Known  []int `json:"known"`
}

func (r *surfaceTabRecord) ensureKnown(slot int) {
	for _, known := range r.Known {
		if known == slot {
			return
		}
	}
	r.Known = append(r.Known, slot)
	sort.Ints(r.Known)
}

func (g *LiveGateway) tabStatePath() string {
	return strings.TrimSpace(g.config.TabStatePath)
}

// loadSurfaceTabsLocked lazily loads persisted tab state. Caller holds g.mu.
func (g *LiveGateway) loadSurfaceTabsLocked() {
	if g.tabs != nil {
		return
	}
	g.tabs = map[string]*surfaceTabRecord{}
	path := g.tabStatePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("feishu tabs: load state failed: path=%s err=%v", path, err)
		}
		return
	}
	loaded := map[string]*surfaceTabRecord{}
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("feishu tabs: parse state failed: path=%s err=%v", path, err)
		return
	}
	g.tabs = loaded
}

// persistSurfaceTabsLocked writes tab state to disk. Caller holds g.mu.
func (g *LiveGateway) persistSurfaceTabsLocked() {
	path := g.tabStatePath()
	if path == "" || g.tabs == nil {
		return
	}
	data, err := json.MarshalIndent(g.tabs, "", "  ")
	if err != nil {
		log.Printf("feishu tabs: encode state failed: err=%v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("feishu tabs: create state dir failed: path=%s err=%v", path, err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("feishu tabs: write state failed: path=%s err=%v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("feishu tabs: replace state failed: path=%s err=%v", path, err)
	}
}

// applySurfaceSlot maps a base surface ID to the active virtual tab surface.
func (g *LiveGateway) applySurfaceSlot(baseSurfaceID string) string {
	if g == nil {
		return baseSurfaceID
	}
	baseSurfaceID = strings.TrimSpace(baseSurfaceID)
	if baseSurfaceID == "" {
		return baseSurfaceID
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadSurfaceTabsLocked()
	record := g.tabs[baseSurfaceID]
	if record == nil || record.Active <= 1 {
		return baseSurfaceID
	}
	return gatewaypkg.SurfaceIDWithTab(baseSurfaceID, record.Active)
}

// handleTabCommand executes /tab commands entirely inside the gateway.
func (g *LiveGateway) handleTabCommand(ctx context.Context, req gatewaypkg.TabCommandRequest) {
	if g == nil {
		return
	}
	base := strings.TrimSpace(req.BaseSurfaceID)
	if base == "" {
		return
	}
	arg := strings.ToLower(strings.TrimSpace(req.Arg))

	g.mu.Lock()
	g.loadSurfaceTabsLocked()
	record := g.tabs[base]
	if record == nil {
		record = &surfaceTabRecord{Active: 1, Known: []int{1}}
		g.tabs[base] = record
	}
	record.ensureKnown(1)
	if record.Active <= 0 {
		record.Active = 1
	}

	var title, body, theme string
	autoListSlot := 0
	switch {
	case arg == "" || arg == "list" || arg == "ls":
		title = "标签页"
		body = renderTabListBody(record)
		theme = cardThemeInfo
	case arg == "new" || arg == "add" || arg == "+" || arg == "新建":
		slot := nextFreeTabSlot(record)
		if slot < 0 {
			title = "标签页已达上限"
			body = fmt.Sprintf("最多支持 %d 个标签页。可以用 `/tab <编号>` 切换到已有标签页。", maxSurfaceTabs)
			theme = cardThemeError
		} else {
			record.ensureKnown(slot)
			record.Active = slot
			g.persistSurfaceTabsLocked()
			autoListSlot = slot
			title = fmt.Sprintf("已新建并切换到标签页 %d", slot)
			body = tabSwitchHint(slot, true) + "\n" + renderTabListBody(record)
			theme = cardThemeSuccess
		}
	default:
		slot, err := strconv.Atoi(arg)
		if err != nil || slot < 1 || slot > maxSurfaceTabs {
			title = "标签页"
			body = fmt.Sprintf("无法识别 `%s`。用法：`/tab` 查看标签页，`/tab new` 新建，`/tab <1-%d>` 切换。", strings.TrimSpace(req.Arg), maxSurfaceTabs)
			theme = cardThemeError
		} else {
			isNew := !containsInt(record.Known, slot)
			record.ensureKnown(slot)
			if record.Active == slot {
				title = fmt.Sprintf("已在标签页 %d", slot)
				body = renderTabListBody(record)
				theme = cardThemeInfo
			} else {
				record.Active = slot
				g.persistSurfaceTabsLocked()
				if isNew {
					autoListSlot = slot
				}
				title = fmt.Sprintf("已切换到标签页 %d", slot)
				body = tabSwitchHint(slot, isNew) + "\n" + renderTabListBody(record)
				theme = cardThemeSuccess
			}
		}
	}
	handler := g.actionHandler
	g.mu.Unlock()

	g.deliverTabNotice(ctx, req, title, body, theme)
	if autoListSlot > 0 {
		g.autoOpenWorkspacePicker(ctx, req, autoListSlot, handler)
	}
}

// autoOpenWorkspacePicker synthesizes a /list command on the freshly created
// tab surface so the user immediately gets the workspace selection card
// (which also carries the "add workspace / import git repo" entry points).
func (g *LiveGateway) autoOpenWorkspacePicker(ctx context.Context, req gatewaypkg.TabCommandRequest, slot int, handler ActionHandler) {
	if handler == nil {
		return
	}
	action, ok := control.ParseFeishuTextActionWithoutCatalog("/list")
	if !ok {
		return
	}
	action.GatewayID = strings.TrimSpace(req.GatewayID)
	if action.GatewayID == "" {
		action.GatewayID = g.config.GatewayID
	}
	action.SurfaceSessionID = gatewaypkg.SurfaceIDWithTab(req.BaseSurfaceID, slot)
	action.ChatID = strings.TrimSpace(req.ChatID)
	action.ActorUserID = strings.TrimSpace(req.ActorUserID)
	action.MessageID = strings.TrimSpace(req.MessageID)
	if err := handleGatewayEventAction(ctx, action, handler); err != nil {
		log.Printf("feishu tabs: auto /list dispatch failed: surface=%s err=%v", action.SurfaceSessionID, err)
	}
}

func containsInt(values []int, v int) bool {
	for _, value := range values {
		if value == v {
			return true
		}
	}
	return false
}

func nextFreeTabSlot(record *surfaceTabRecord) int {
	for slot := 1; slot <= maxSurfaceTabs; slot++ {
		if !containsInt(record.Known, slot) {
			return slot
		}
	}
	return -1
}

func tabSwitchHint(slot int, isNew bool) string {
	if isNew {
		return "这是一个全新的标签页：正在其他标签页执行的任务不受影响，会继续在后台运行并把结果回复到原消息下。马上会弹出工作区选择卡片（也可以在卡片里添加新工作区或导入 Git 仓库）。"
	}
	return "已回到该标签页的会话上下文：其他标签页的任务继续在后台运行。后续消息会路由到这个标签页。"
}

func renderTabListBody(record *surfaceTabRecord) string {
	if record == nil {
		return ""
	}
	lines := make([]string, 0, len(record.Known)+2)
	for _, slot := range record.Known {
		marker := "  "
		if slot == record.Active {
			marker = "▸ "
		}
		label := fmt.Sprintf("%s标签页 %d", marker, slot)
		if slot == record.Active {
			label += "（当前）"
		}
		lines = append(lines, label)
	}
	lines = append(lines, "", "`/tab <编号>` 切换 · `/tab new` 新建 · 每个标签页是独立会话，互不打断")
	return strings.Join(lines, "\n")
}

func (g *LiveGateway) deliverTabNotice(ctx context.Context, req gatewaypkg.TabCommandRequest, title, body, theme string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	receiveID, receiveIDType := gatewaypkg.ResolveReceiveTarget(req.ChatID, req.ActorUserID)
	if receiveID == "" || receiveIDType == "" {
		return
	}
	op := Operation{
		Kind:             OperationSendCard,
		GatewayID:        g.config.GatewayID,
		SurfaceSessionID: strings.TrimSpace(req.BaseSurfaceID),
		ReceiveID:        receiveID,
		ReceiveIDType:    receiveIDType,
		ChatID:           strings.TrimSpace(req.ChatID),
		ReplyToMessageID: strings.TrimSpace(req.MessageID),
		CardTitle:        title,
		CardBody:         body,
		CardThemeKey:     theme,
		cardEnvelope:     cardEnvelopeV2,
		card:             rawCardDocument(title, body, theme, nil),
	}
	applyCtx, cancel := newFeishuTimeoutContext(ctx, asyncInboundFailureNoticeTimeout)
	defer cancel()
	if err := g.Apply(applyCtx, []Operation{op}); err != nil {
		log.Printf("feishu tabs: notice delivery failed: surface=%s err=%v", req.BaseSurfaceID, err)
	}
}
