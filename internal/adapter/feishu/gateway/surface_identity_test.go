package gateway

import "testing"

func TestSurfaceIDForInboundKeepsP2PUsersIndependent(t *testing.T) {
	first := SurfaceIDForInbound("efc-site", "oc-p2p", "p2p", "ou-user-1")
	second := SurfaceIDForInbound("efc-site", "oc-p2p", "p2p", "ou-user-2")

	if first != "feishu:efc-site:user:ou-user-1" {
		t.Fatalf("unexpected first P2P surface id: %q", first)
	}
	if second != "feishu:efc-site:user:ou-user-2" {
		t.Fatalf("unexpected second P2P surface id: %q", second)
	}
	if first == second {
		t.Fatalf("expected different users on one bot to keep independent surfaces, got %q", first)
	}
}

func TestSurfaceIDForInboundKeepsGroupChatScoped(t *testing.T) {
	first := SurfaceIDForInbound("efc-site", "oc-group", "group", "ou-user-1")
	second := SurfaceIDForInbound("efc-site", "oc-group", "group", "ou-user-2")

	if first != "feishu:efc-site:chat:oc-group" || second != first {
		t.Fatalf("expected existing group chat scope to remain shared, got first=%q second=%q", first, second)
	}
}
