package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestGatewaySurfacePoliciesInvalidMaxAccessModeFailsClosed(t *testing.T) {
	policies := gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{
		{ID: "app-bad", MaxAccessMode: "totally-invalid"},
		{ID: "app-good", MaxAccessMode: "accept_edits"},
	})
	if got := policies["app-bad"].MaxAccessMode; got != agentproto.AccessModeConfirm {
		t.Fatalf("expected invalid maxAccessMode to fail closed to confirm, got %q", got)
	}
	if got := policies["app-good"].MaxAccessMode; got != "accept_edits" {
		t.Fatalf("expected valid maxAccessMode preserved, got %q", got)
	}
}

func TestGatewaySurfacePoliciesInvalidRootsFailClosed(t *testing.T) {
	policies := gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{
		{ID: "app-empty-roots", WorkspaceRoots: []string{"", "   ", "."}},
	})
	roots := policies["app-empty-roots"].WorkspaceRoots
	if len(roots) != 1 || roots[0] != "/dev/null/workspace-roots-invalid" {
		t.Fatalf("expected all-invalid roots to fail closed to sentinel, got %#v", roots)
	}
}

func TestGatewaySurfacePoliciesResolveSymlinkRoots(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	policies := gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{
		{ID: "app-link", WorkspaceRoots: []string{linkDir}},
	})
	roots := policies["app-link"].WorkspaceRoots
	foundOriginal := false
	foundResolved := false
	for _, root := range roots {
		if root == linkDir {
			foundOriginal = true
		}
		if resolved, err := filepath.EvalSymlinks(linkDir); err == nil && root == resolved {
			foundResolved = true
		}
	}
	if !foundOriginal || !foundResolved {
		t.Fatalf("expected both symlink root and resolved target in roots, got %#v", roots)
	}
	// 不存在的 root：解析失败但保留原值。
	missing := filepath.Join(base, "missing")
	policies = gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{
		{ID: "app-missing", WorkspaceRoots: []string{missing}},
	})
	roots = policies["app-missing"].WorkspaceRoots
	if len(roots) != 1 || roots[0] != missing {
		t.Fatalf("expected unresolvable root kept as-is, got %#v", roots)
	}
}
