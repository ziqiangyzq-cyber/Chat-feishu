package feishu

import (
	"path/filepath"
	"testing"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
)

func newTabTestGateway(t *testing.T) *LiveGateway {
	t.Helper()
	return &LiveGateway{
		config: LiveGatewayConfig{
			GatewayID:    "app-1",
			TabStatePath: filepath.Join(t.TempDir(), "tabs.json"),
		},
		reactions: map[string]string{},
		messages:  map[string]string{},
	}
}

func TestApplySurfaceSlotDefaultsToBase(t *testing.T) {
	g := newTabTestGateway(t)
	base := "feishu:app-1:user:u1"
	if got := g.applySurfaceSlot(base); got != base {
		t.Fatalf("expected identity mapping, got %q", got)
	}
}

func TestTabSwitchRoutesSubsequentMessages(t *testing.T) {
	g := newTabTestGateway(t)
	base := "feishu:app-1:user:u1"

	g.mu.Lock()
	g.loadSurfaceTabsLocked()
	record := &surfaceTabRecord{Active: 2, Known: []int{1, 2}}
	g.tabs[base] = record
	g.persistSurfaceTabsLocked()
	g.mu.Unlock()

	if got, want := g.applySurfaceSlot(base), base+"#tab2"; got != want {
		t.Fatalf("applySurfaceSlot = %q, want %q", got, want)
	}

	// A fresh gateway instance must reload persisted state.
	g2 := newTabTestGateway(t)
	g2.config.TabStatePath = g.config.TabStatePath
	if got, want := g2.applySurfaceSlot(base), base+"#tab2"; got != want {
		t.Fatalf("persisted applySurfaceSlot = %q, want %q", got, want)
	}
}

func TestSurfaceIDWithTabSlotOneIsBase(t *testing.T) {
	base := "feishu:app-1:user:u1"
	if got := gatewaypkg.SurfaceIDWithTab(base, 1); got != base {
		t.Fatalf("slot 1 should map to base, got %q", got)
	}
	if got, want := gatewaypkg.SurfaceIDWithTab(base, 3), base+"#tab3"; got != want {
		t.Fatalf("slot 3 = %q, want %q", got, want)
	}
}

func TestParseSurfaceRefStripsTabSuffix(t *testing.T) {
	ref, ok := ParseSurfaceRef("feishu:app-1:user:u1#tab2")
	if !ok {
		t.Fatal("expected tab-suffixed surface to parse")
	}
	if ref.ScopeID != "u1" {
		t.Fatalf("ScopeID = %q, want u1", ref.ScopeID)
	}
	base, tab := SplitSurfaceTab("feishu:app-1:user:u1#tab2")
	if base != "feishu:app-1:user:u1" || tab != "tab2" {
		t.Fatalf("SplitSurfaceTab = %q, %q", base, tab)
	}
}

func TestParseTabCommandText(t *testing.T) {
	cases := []struct {
		text    string
		wantArg string
		wantOK  bool
	}{
		{"/tab", "", true},
		{"/tabs", "", true},
		{"/tab 2", "2", true},
		{"/tab new", "new", true},
		{"/TAB 3", "3", true},
		{"/table", "", false},
		{"tab 2", "", false},
		{"/use abc", "", false},
	}
	for _, tc := range cases {
		arg, ok := gatewaypkg.ParseTabCommandText(tc.text)
		if ok != tc.wantOK || arg != tc.wantArg {
			t.Fatalf("ParseTabCommandText(%q) = %q,%v want %q,%v", tc.text, arg, ok, tc.wantArg, tc.wantOK)
		}
	}
}

func TestNextFreeTabSlot(t *testing.T) {
	record := &surfaceTabRecord{Active: 1, Known: []int{1, 2, 3}}
	if slot := nextFreeTabSlot(record); slot != 4 {
		t.Fatalf("nextFreeTabSlot = %d, want 4", slot)
	}
	full := &surfaceTabRecord{Active: 1}
	for i := 1; i <= maxSurfaceTabs; i++ {
		full.ensureKnown(i)
	}
	if slot := nextFreeTabSlot(full); slot != -1 {
		t.Fatalf("nextFreeTabSlot full = %d, want -1", slot)
	}
}
