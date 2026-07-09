package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/cronrepo"
	cronrt "github.com/kxn/codex-remote-feishu/internal/app/cronruntime"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/claudeworkspaceprofile"
	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
)

type surfaceResumeRecoveryState struct {
	Entry             surfaceresume.Entry
	NextAttemptAt     time.Time
	LastAttemptAt     time.Time
	LastFailureCode   string
	StickyFailureCode string
	LastNoticeCode    string
}

type vscodeMigrationFlowRecord struct {
	FlowID           string
	SurfaceSessionID string
	OwnerUserID      string
	MessageID        string
	IssueKey         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        time.Time
}

type surfaceResumeRuntimeState struct {
	store                  *surfaceresume.Store
	recovery               map[string]*surfaceResumeRecoveryState
	vscodeMigrationFlows   map[string]*vscodeMigrationFlowRecord
	vscodeMigrationNextSeq int64
	vscodeResumeNotices    map[string]bool
	vscodeStartupCheckDue  bool
	startupRefreshPending  map[string]bool
	startupRefreshSeen     bool
	workspaceContextRoots  map[string]string
}

type claudeWorkspaceProfileRuntimeState struct {
	store *claudeworkspaceprofile.Store
}

type cronRuntimeState struct {
	stateIOMu             sync.Mutex
	loaded                bool
	syncInFlight          bool
	state                 *cronrt.StateFile
	runs                  map[string]*cronrt.RunState
	jobActiveRuns         map[string]map[string]struct{}
	exitTargets           map[string]*cronrt.ExitTarget
	bitableFactory        func(string) (feishu.BitableAPI, error)
	gatewayIdentityLookup func(string) (cronrt.GatewayIdentity, bool, error)
	nextScheduleScan      time.Time
	repoManager           *cronrepo.Manager
}

type feishuRuntimeState struct {
	mu                        sync.RWMutex
	permissionMu              sync.RWMutex
	runtimeApply              map[string]feishuRuntimeApplyPendingState
	timeSensitive             map[string]feishuTimeSensitiveState
	attentionRequests         map[string]time.Time
	permissionGaps            map[string]map[string]*feishuPermissionGapRecord
	permissionRefreshEvery    time.Duration
	permissionNextRefresh     time.Time
	permissionRefreshInFlight bool
	onboarding                map[string]*feishuOnboardingSession
	setup                     feishuSetupClient
}

type opsRuntimeState struct {
	delivery deliveryRuntimeState
	wecom    map[string]*wecomRuntimeState
}

type deliveryRuntimeState struct {
	successCount int
	failureCount int
	recent       []deliveryFailureRecord
}

type deliveryFailureRecord struct {
	OccurredAt       time.Time
	Channel          string
	GatewayID        string
	SurfaceSessionID string
	EventKind        string
	Reason           string
}

type wecomRuntimeState struct {
	connected         bool
	state             string
	lastError         string
	lastConnectedAt   time.Time
	lastStateChangeAt time.Time
	reconnectAttempts int
	nextRetryAt       time.Time
	lastRetryDelay    time.Duration
}

const opsRecentFailureLimit = 8

func newSurfaceResumeRuntimeState() surfaceResumeRuntimeState {
	return surfaceResumeRuntimeState{
		recovery:              map[string]*surfaceResumeRecoveryState{},
		vscodeMigrationFlows:  map[string]*vscodeMigrationFlowRecord{},
		vscodeResumeNotices:   map[string]bool{},
		startupRefreshPending: map[string]bool{},
		workspaceContextRoots: map[string]string{},
	}
}

func newCronRuntimeState() cronRuntimeState {
	return cronRuntimeState{
		runs:          map[string]*cronrt.RunState{},
		jobActiveRuns: map[string]map[string]struct{}{},
		exitTargets:   map[string]*cronrt.ExitTarget{},
	}
}

func newFeishuRuntimeState() feishuRuntimeState {
	return feishuRuntimeState{
		runtimeApply:           map[string]feishuRuntimeApplyPendingState{},
		timeSensitive:          map[string]feishuTimeSensitiveState{},
		attentionRequests:      map[string]time.Time{},
		permissionGaps:         map[string]map[string]*feishuPermissionGapRecord{},
		permissionRefreshEvery: defaultFeishuPermissionRefreshEvery,
		onboarding:             map[string]*feishuOnboardingSession{},
	}
}

func newOpsRuntimeState() opsRuntimeState {
	return opsRuntimeState{
		delivery: deliveryRuntimeState{
			recent: make([]deliveryFailureRecord, 0, opsRecentFailureLimit),
		},
		wecom: map[string]*wecomRuntimeState{},
	}
}

func (a *App) recordDeliverySuccessLocked(channel, gatewayID string) {
	if a == nil {
		return
	}
	a.opsRuntime.delivery.successCount++
}

func (a *App) recordDeliveryFailureLocked(channel, gatewayID, surfaceSessionID, eventKind string, err error) {
	if a == nil {
		return
	}
	a.opsRuntime.delivery.failureCount++
	record := deliveryFailureRecord{
		OccurredAt:       time.Now().UTC(),
		Channel:          normalizeOpsChannel(channel),
		GatewayID:        strings.TrimSpace(gatewayID),
		SurfaceSessionID: strings.TrimSpace(surfaceSessionID),
		EventKind:        strings.TrimSpace(eventKind),
		Reason:           strings.TrimSpace(daemonErrString(err)),
	}
	if record.Channel == "" {
		record.Channel = inferOpsChannel(record.GatewayID)
	}
	if record.Reason == "" {
		record.Reason = "unknown delivery failure"
	}
	a.opsRuntime.delivery.recent = append([]deliveryFailureRecord{record}, a.opsRuntime.delivery.recent...)
	if len(a.opsRuntime.delivery.recent) > opsRecentFailureLimit {
		a.opsRuntime.delivery.recent = a.opsRuntime.delivery.recent[:opsRecentFailureLimit]
	}
}

func (a *App) wecomRuntimeStateLocked(gatewayID string) *wecomRuntimeState {
	if a == nil {
		return nil
	}
	if a.opsRuntime.wecom == nil {
		a.opsRuntime.wecom = map[string]*wecomRuntimeState{}
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	runtimeState := a.opsRuntime.wecom[gatewayID]
	if runtimeState == nil {
		runtimeState = &wecomRuntimeState{state: "disabled"}
		a.opsRuntime.wecom[gatewayID] = runtimeState
	}
	return runtimeState
}

func (a *App) setWeComStateLocked(gatewayID, state string, at time.Time) {
	if a == nil {
		return
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.state = state
	runtimeState.lastStateChangeAt = at
}

func (a *App) markWeComConnectingLocked(gatewayID string) {
	if a == nil {
		return
	}
	now := time.Now().UTC()
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.connected = false
	runtimeState.nextRetryAt = time.Time{}
	runtimeState.lastRetryDelay = 0
	a.setWeComStateLocked(gatewayID, "connecting", now)
}

func (a *App) markWeComConnectedLocked(gatewayID string, at time.Time) {
	if a == nil {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.connected = true
	runtimeState.lastConnectedAt = at
	runtimeState.lastError = ""
	runtimeState.nextRetryAt = time.Time{}
	runtimeState.lastRetryDelay = 0
	runtimeState.reconnectAttempts = 0
	a.setWeComStateLocked(gatewayID, "connected", at)
}

func (a *App) markWeComDegradedLocked(gatewayID, reason string) {
	if a == nil {
		return
	}
	now := time.Now().UTC()
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.connected = false
	runtimeState.lastError = strings.TrimSpace(reason)
	a.setWeComStateLocked(gatewayID, "degraded", now)
}

func (a *App) markWeComReconnectWaitingLocked(gatewayID, reason string, delay time.Duration, nextRetryAt time.Time) {
	if a == nil {
		return
	}
	now := time.Now().UTC()
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.connected = false
	runtimeState.lastError = strings.TrimSpace(reason)
	runtimeState.lastRetryDelay = delay
	runtimeState.reconnectAttempts++
	if !nextRetryAt.IsZero() {
		runtimeState.nextRetryAt = nextRetryAt.UTC()
	} else {
		runtimeState.nextRetryAt = time.Time{}
	}
	a.setWeComStateLocked(gatewayID, "reconnect_wait", now)
}

func (a *App) markWeComStoppedLocked(gatewayID, reason string) {
	if a == nil {
		return
	}
	runtimeState := a.wecomRuntimeStateLocked(gatewayID)
	runtimeState.connected = false
	if strings.TrimSpace(reason) != "" {
		runtimeState.lastError = strings.TrimSpace(reason)
	}
	runtimeState.nextRetryAt = time.Time{}
	runtimeState.lastRetryDelay = 0
	a.setWeComStateLocked(gatewayID, "stopped", time.Now().UTC())
}

func (a *App) setWeComDisabledLocked(gatewayID string) {
	if a == nil {
		return
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	if a.opsRuntime.wecom == nil {
		a.opsRuntime.wecom = map[string]*wecomRuntimeState{}
	}
	a.opsRuntime.wecom[gatewayID] = &wecomRuntimeState{state: "disabled"}
}

func normalizeOpsChannel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "feishu", "wecom":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func inferOpsChannel(gatewayID string) string {
	if strings.HasPrefix(strings.TrimSpace(gatewayID), wecomNamespacePrefix) {
		return "wecom"
	}
	return "feishu"
}
