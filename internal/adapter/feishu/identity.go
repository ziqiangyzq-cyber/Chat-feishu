package feishu

import "strings"

const (
	PlatformFeishu = "feishu"
	ScopeKindUser  = "user"
	ScopeKindChat  = "chat"
)

type SurfaceRef struct {
	Platform  string
	GatewayID string
	ScopeKind string
	ScopeID   string
}

// SplitSurfaceTab separates an optional "#tabN" suffix from a surface session
// ID. Tab-suffixed surfaces are virtual sub-surfaces of the same chat used to
// run multiple concurrent sessions from one Feishu conversation.
func SplitSurfaceTab(surfaceID string) (string, string) {
	surfaceID = strings.TrimSpace(surfaceID)
	if idx := strings.Index(surfaceID, "#"); idx >= 0 {
		return surfaceID[:idx], surfaceID[idx+1:]
	}
	return surfaceID, ""
}

func ParseSurfaceRef(surfaceID string) (SurfaceRef, bool) {
	surfaceID, _ = SplitSurfaceTab(surfaceID)
	parts := strings.Split(strings.TrimSpace(surfaceID), ":")
	if len(parts) != 4 {
		return SurfaceRef{}, false
	}
	if parts[0] != PlatformFeishu {
		return SurfaceRef{}, false
	}
	ref := SurfaceRef{
		Platform:  parts[0],
		GatewayID: normalizeGatewayID(parts[1]),
		ScopeKind: strings.TrimSpace(parts[2]),
		ScopeID:   strings.TrimSpace(parts[3]),
	}
	if !ref.valid() {
		return SurfaceRef{}, false
	}
	return ref, true
}

func (r SurfaceRef) SurfaceID() string {
	if !r.valid() {
		return ""
	}
	return strings.Join([]string{
		PlatformFeishu,
		normalizeGatewayID(r.GatewayID),
		r.ScopeKind,
		r.ScopeID,
	}, ":")
}

func (r SurfaceRef) valid() bool {
	if strings.TrimSpace(r.Platform) != PlatformFeishu {
		return false
	}
	if strings.TrimSpace(r.GatewayID) == "" {
		return false
	}
	if strings.TrimSpace(r.ScopeID) == "" {
		return false
	}
	switch strings.TrimSpace(r.ScopeKind) {
	case ScopeKindUser, ScopeKindChat:
		return true
	default:
		return false
	}
}

func normalizeGatewayID(gatewayID string) string {
	return strings.TrimSpace(gatewayID)
}
