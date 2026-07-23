package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
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
	original := state.NormalizeWorkspaceKey(linkDir)
	resolved, err := filepath.EvalSymlinks(linkDir)
	if err != nil {
		t.Fatal(err)
	}
	resolved = state.NormalizeWorkspaceKey(resolved)
	foundOriginal := false
	foundResolved := false
	for _, root := range roots {
		if root == original {
			foundOriginal = true
		}
		if root == resolved {
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
	if len(roots) != 1 || roots[0] != state.NormalizeWorkspaceKey(missing) {
		t.Fatalf("expected unresolvable root kept as-is, got %#v", roots)
	}
}

func TestGatewaySurfacePoliciesDefaultWorkspaceAndConcurrentSurfaces(t *testing.T) {
	root := t.TempDir()
	policies := gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{{
		ID:                               "app-default",
		WorkspaceRoots:                   []string{root},
		DefaultWorkspaceRoot:             root,
		AllowConcurrentWorkspaceSurfaces: true,
	}})

	policy := policies["app-default"]
	if policy.DefaultWorkspaceRoot != state.NormalizeWorkspaceKey(root) {
		t.Fatalf("expected default workspace %q, got %#v", root, policy)
	}
	if !policy.AllowConcurrentWorkspaceSurfaces {
		t.Fatalf("expected concurrent workspace surfaces to be enabled, got %#v", policy)
	}
}

func TestGatewaySurfacePoliciesRejectDefaultOutsideWorkspaceRoots(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	policies := gatewaySurfacePoliciesFromFeishuApps([]config.FeishuAppConfig{{
		ID:                               "app-invalid-default",
		WorkspaceRoots:                   []string{allowed},
		DefaultWorkspaceRoot:             outside,
		AllowConcurrentWorkspaceSurfaces: true,
	}})

	policy := policies["app-invalid-default"]
	if policy.DefaultWorkspaceRoot != "" {
		t.Fatalf("expected out-of-policy default workspace to fail closed, got %#v", policy)
	}
	if policy.AllowConcurrentWorkspaceSurfaces {
		t.Fatalf("expected concurrency to stay disabled without a valid default workspace, got %#v", policy)
	}
}
