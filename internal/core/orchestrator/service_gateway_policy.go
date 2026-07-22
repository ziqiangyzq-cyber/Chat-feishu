package orchestrator

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// GatewaySurfacePolicy 描述某个 gateway（飞书 app）下所有 surface 的访问策略。
// 全部字段为空时等价于"无策略"，行为与未配置完全一致。
type GatewaySurfacePolicy struct {
	// WorkspaceRoots 非空时：该 gateway 的 surface 只能看到/使用这些根目录
	// （含子目录）下的工作区。
	WorkspaceRoots []string
	// DefaultWorkspaceRoot 非空时，detached headless surface 的首条输入会自动进入
	// 该工作区。
	DefaultWorkspaceRoot string
	// AllowConcurrentWorkspaceSurfaces 允许同一 gateway 的多个 surface 共享默认
	// 工作区 claim；instance 与 thread claim 不受此字段影响，仍保持独占。
	AllowConcurrentWorkspaceSurfaces bool
	// MaxAccessMode 非空时：生效执行权限不得超过此级别
	// （权限强弱：full_access > accept_edits > confirm）。
	MaxAccessMode string
	// ApproverOpenID 非空时：该 gateway 上的越权审批只允许此 open_id 的用户处理，
	// 其他用户的越权请求会被自动拒绝并通知审批人。
	ApproverOpenID string
}

func (p GatewaySurfacePolicy) Normalized() GatewaySurfacePolicy {
	roots := make([]string, 0, len(p.WorkspaceRoots))
	seen := map[string]bool{}
	for _, root := range p.WorkspaceRoots {
		normalized := state.NormalizeWorkspaceKey(root)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		roots = append(roots, normalized)
	}
	p.WorkspaceRoots = roots
	p.DefaultWorkspaceRoot = state.NormalizeWorkspaceKey(p.DefaultWorkspaceRoot)
	if p.DefaultWorkspaceRoot != "" && len(p.WorkspaceRoots) != 0 && !workspaceKeyWithinPolicyRoots(p.DefaultWorkspaceRoot, p.WorkspaceRoots) {
		p.DefaultWorkspaceRoot = ""
	}
	if p.DefaultWorkspaceRoot == "" {
		p.AllowConcurrentWorkspaceSurfaces = false
	}
	p.MaxAccessMode = agentproto.NormalizeAccessMode(p.MaxAccessMode)
	p.ApproverOpenID = strings.TrimSpace(p.ApproverOpenID)
	return p
}

func (p GatewaySurfacePolicy) isZero() bool {
	return len(p.WorkspaceRoots) == 0 && p.DefaultWorkspaceRoot == "" && !p.AllowConcurrentWorkspaceSurfaces && p.MaxAccessMode == "" && p.ApproverOpenID == ""
}

// SetGatewaySurfacePolicies 注入按 gatewayID 索引的 surface 策略。
// 在 daemon 启动时调用一次；未出现在 map 里的 gateway 不受任何限制。
func (s *Service) SetGatewaySurfacePolicies(policies map[string]GatewaySurfacePolicy) {
	if s == nil {
		return
	}
	normalized := map[string]GatewaySurfacePolicy{}
	for gatewayID, policy := range policies {
		gatewayID = strings.TrimSpace(gatewayID)
		policy = policy.Normalized()
		if gatewayID == "" || policy.isZero() {
			continue
		}
		normalized[gatewayID] = policy
	}
	if len(normalized) == 0 {
		s.gatewayPolicies = nil
		return
	}
	s.gatewayPolicies = normalized
}

func (s *Service) surfaceGatewayPolicy(surface *state.SurfaceConsoleRecord) (GatewaySurfacePolicy, bool) {
	if s == nil || surface == nil || len(s.gatewayPolicies) == 0 {
		return GatewaySurfacePolicy{}, false
	}
	policy, ok := s.gatewayPolicies[strings.TrimSpace(surface.GatewayID)]
	return policy, ok
}

func (s *Service) surfaceDefaultWorkspaceRoot(surface *state.SurfaceConsoleRecord) string {
	policy, ok := s.surfaceGatewayPolicy(surface)
	if !ok {
		return ""
	}
	workspaceKey := state.NormalizeWorkspaceKey(policy.DefaultWorkspaceRoot)
	if workspaceKey == "" || !s.surfaceWorkspaceAllowedByPolicy(surface, workspaceKey) {
		return ""
	}
	return workspaceKey
}

func (s *Service) surfacesMayShareDefaultWorkspaceClaim(surface, owner *state.SurfaceConsoleRecord, workspaceKey string) bool {
	if surface == nil || owner == nil || surface.SurfaceSessionID == owner.SurfaceSessionID {
		return false
	}
	if strings.TrimSpace(surface.GatewayID) == "" || strings.TrimSpace(surface.GatewayID) != strings.TrimSpace(owner.GatewayID) {
		return false
	}
	policy, surfaceOK := s.surfaceGatewayPolicy(surface)
	ownerPolicy, ownerOK := s.surfaceGatewayPolicy(owner)
	if !surfaceOK || !ownerOK || !policy.AllowConcurrentWorkspaceSurfaces || !ownerPolicy.AllowConcurrentWorkspaceSurfaces {
		return false
	}
	workspaceKey = state.NormalizeWorkspaceKey(workspaceKey)
	return workspaceKey != "" &&
		workspaceKey == state.NormalizeWorkspaceKey(policy.DefaultWorkspaceRoot) &&
		workspaceKey == state.NormalizeWorkspaceKey(ownerPolicy.DefaultWorkspaceRoot)
}

// accessModeRank 给权限模式排序：full_access(3) > accept_edits(2) > confirm(1)。
func accessModeRank(mode string) int {
	switch agentproto.NormalizeAccessMode(mode) {
	case agentproto.AccessModeFullAccess:
		return 3
	case agentproto.AccessModeAcceptEdits:
		return 2
	case agentproto.AccessModeConfirm:
		return 1
	default:
		return 0
	}
}

// clampAccessModeToMax 把 mode clamp 到不超过 max 的级别（取较低者）。
// mode 或 max 无法归一化时原样返回 mode。
func clampAccessModeToMax(mode, max string) string {
	normalizedMode := agentproto.NormalizeAccessMode(mode)
	normalizedMax := agentproto.NormalizeAccessMode(max)
	if normalizedMode == "" || normalizedMax == "" {
		return mode
	}
	if accessModeRank(normalizedMode) > accessModeRank(normalizedMax) {
		return normalizedMax
	}
	return normalizedMode
}

// clampSurfaceAccessMode 按 surface 所属 gateway 的策略 clamp 权限模式。
// 空 mode 保持为空（表示"未覆盖"），无策略时原样返回。
func (s *Service) clampSurfaceAccessMode(surface *state.SurfaceConsoleRecord, mode string) string {
	if strings.TrimSpace(mode) == "" {
		return mode
	}
	policy, ok := s.surfaceGatewayPolicy(surface)
	if !ok || policy.MaxAccessMode == "" {
		return mode
	}
	return clampAccessModeToMax(mode, policy.MaxAccessMode)
}

// surfaceWorkspaceAllowedByPolicy 判断 workspaceKey 是否在策略允许的根目录内。
// 无策略或策略未配置 WorkspaceRoots 时恒为 true；空 workspaceKey 视为允许
// （由后续流程自行报"工作区不存在"类错误）。
func (s *Service) surfaceWorkspaceAllowedByPolicy(surface *state.SurfaceConsoleRecord, workspaceKey string) bool {
	policy, ok := s.surfaceGatewayPolicy(surface)
	if !ok || len(policy.WorkspaceRoots) == 0 {
		return true
	}
	workspaceKey = state.NormalizeWorkspaceKey(workspaceKey)
	if workspaceKey == "" {
		return true
	}
	return workspaceKeyWithinPolicyRoots(workspaceKey, policy.WorkspaceRoots)
}

// workspaceKeyWithinPolicyRoots 按路径段做前缀判断：
// /srv/proj/site 允许 /srv/proj/site 与 /srv/proj/site/sub，
// 但不允许 /srv/proj/site-evil（不是路径段边界）。
func workspaceKeyWithinPolicyRoots(workspaceKey string, roots []string) bool {
	path := state.NormalizeWorkspaceKey(workspaceKey)
	if path == "" {
		return false
	}
	path = filepath.ToSlash(filepath.Clean(path))
	for _, root := range roots {
		root = state.NormalizeWorkspaceKey(root)
		if root == "" {
			continue
		}
		root = filepath.ToSlash(filepath.Clean(root))
		if path == root {
			return true
		}
		prefix := root
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

const (
	workspacePolicyDeniedNoticeCode = "workspace_policy_denied"
	workspacePolicyDeniedNoticeText = "该机器人仅允许使用指定工作区，目标目录不在允许范围内。"
)

func (s *Service) workspacePolicyDeniedNotice(surface *state.SurfaceConsoleRecord) []eventcontract.Event {
	return notice(surface, workspacePolicyDeniedNoticeCode, workspacePolicyDeniedNoticeText)
}

// blockTargetPickerPathByWorkspacePolicy 在目标选择器确认阶段（clone / mkdir /
// worktree 创建等落盘动作发生之前）校验最终目录是否在策略允许范围内。
// 允许时返回 nil；被拒时把危险提示写回卡片并返回重建后的卡片事件。
func (s *Service) blockTargetPickerPathByWorkspacePolicy(surface *state.SurfaceConsoleRecord, record *activeTargetPickerRecord, finalPath string) []eventcontract.Event {
	if surface == nil || record == nil {
		return nil
	}
	if s.surfaceWorkspaceAllowedByPolicy(surface, finalPath) {
		return nil
	}
	setTargetPickerMessages(record, control.FeishuTargetPickerMessage{
		Level: control.FeishuTargetPickerMessageDanger,
		Text:  workspacePolicyDeniedNoticeText,
	})
	updatedView, err := s.buildTargetPickerView(surface, record)
	if err != nil {
		return notice(surface, "target_picker_unavailable", err.Error())
	}
	return []eventcontract.Event{s.targetPickerViewEvent(surface, updatedView, false)}
}

// findGatewayUserSurface 定位同一 gateway 下指定用户的单聊 surface。
// 优先精确匹配 p2p surface id（feishu:<gatewayID>:user:<openID>），
// 找不到时只在同一 base id 的 tab 变体（feishu:<gw>:user:<openID>#tabN）里
// 按字典序取第一个。绝不回落到 chat-scope surface——群聊 surface 的
// ActorUserID 会被覆写成"最近发言者"，把越权详情投进群聊会造成外泄；
// 定位不到时由调用方按语义跳过推送。
func (s *Service) findGatewayUserSurface(gatewayID, openID string) *state.SurfaceConsoleRecord {
	gatewayID = strings.TrimSpace(gatewayID)
	openID = strings.TrimSpace(openID)
	if s == nil || gatewayID == "" || openID == "" {
		return nil
	}
	exactID := "feishu:" + gatewayID + ":user:" + openID
	if surface := s.root.Surfaces[exactID]; surface != nil {
		return surface
	}
	matchedIDs := make([]string, 0, 2)
	for surfaceID, surface := range s.root.Surfaces {
		if surface == nil {
			continue
		}
		if !strings.HasPrefix(surfaceID, exactID+"#") {
			continue
		}
		matchedIDs = append(matchedIDs, surfaceID)
	}
	if len(matchedIDs) == 0 {
		return nil
	}
	sort.Strings(matchedIDs)
	return s.root.Surfaces[matchedIDs[0]]
}
