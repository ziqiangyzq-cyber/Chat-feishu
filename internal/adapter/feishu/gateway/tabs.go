package gateway

import (
	"strconv"
	"strings"
)

// TabCommandRequest carries a parsed /tab command from the inbound lane to the
// gateway runtime. BaseSurfaceID is the slot-less surface identity for the
// chat; the gateway resolves and mutates the active tab slot for it.
type TabCommandRequest struct {
	GatewayID     string
	BaseSurfaceID string
	ChatID        string
	ActorUserID   string
	MessageID     string
	// Arg is the raw argument after the command name, e.g. "2", "new", "".
	Arg string
}

// ParseTabCommandText recognizes the gateway-local /tab command family. It is
// intentionally checked before the shared command catalog so tabs never reach
// the daemon core, which is unaware of virtual tab surfaces.
func ParseTabCommandText(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	fields := strings.Fields(trimmed)
	head := strings.ToLower(fields[0])
	switch head {
	case "/tab", "/tabs", "/标签", "/标签页":
	default:
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0])), true
}

// ApplySurfaceTabSlot maps a base surface ID to the currently active virtual
// tab surface via the env callback. Identity when unset.
func applySurfaceTabSlot(env InboundEnv, surfaceSessionID string) string {
	if env.ApplySurfaceSlot == nil {
		return surfaceSessionID
	}
	if mapped := strings.TrimSpace(env.ApplySurfaceSlot(surfaceSessionID)); mapped != "" {
		return mapped
	}
	return surfaceSessionID
}

// SurfaceIDWithTab renders the virtual surface ID for a tab slot. Slot 1 is
// the base surface itself so existing sessions survive upgrades.
func SurfaceIDWithTab(baseSurfaceID string, slot int) string {
	baseSurfaceID = strings.TrimSpace(baseSurfaceID)
	if slot <= 1 {
		return baseSurfaceID
	}
	return baseSurfaceID + "#tab" + strconv.Itoa(slot)
}
