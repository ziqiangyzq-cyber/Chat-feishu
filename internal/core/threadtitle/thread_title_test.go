package threadtitle

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestDisplayTitle(t *testing.T) {
	t.Parallel()

	inst := &state.InstanceRecord{
		DisplayName:   "atlas",
		WorkspaceKey:  "/data/dl/atlas",
		WorkspaceRoot: "/data/dl/atlas",
		ShortName:     "atlas",
	}

	t.Run("uses raw renamed title with workspace prefix", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "修复登录流程", CWD: "/data/dl/atlas"}
		if got := DisplayTitle(inst, thread, DefaultDisplayLimit); got != "atlas · 修复登录流程" {
			t.Fatalf("DisplayTitle() = %q, want %q", got, "atlas · 修复登录流程")
		}
	})

	t.Run("falls back to unnamed placeholder without preview naming", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{
			ThreadID: "thread-1",
			Name:     "新会话",
			Preview:  "我先按 atlas 这个工程统计入口文件。",
			CWD:      "/data/dl/atlas",
		}
		if got := DisplayTitle(inst, thread, DefaultDisplayLimit); got != "atlas · 未命名会话" {
			t.Fatalf("DisplayTitle() = %q, want %q", got, "atlas · 未命名会话")
		}
	})

	t.Run("uses thread cwd workspace ahead of instance short name", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "修复登录流程", CWD: "/data/dl/alt"}
		if got := DisplayTitle(inst, thread, DefaultDisplayLimit); got != "alt · 修复登录流程" {
			t.Fatalf("DisplayTitle() = %q, want %q", got, "alt · 修复登录流程")
		}
	})

	t.Run("uses configured workspace alias for prefix", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "修复登录流程", CWD: "/data/dl/site"}
		if got := WorkspaceLabel(inst, thread, map[string]string{"/data/dl/site": "claude-remote-workspace"}); got != "claude-remote-workspace" {
			t.Fatalf("WorkspaceLabel() = %q, want %q", got, "claude-remote-workspace")
		}
	})
}

func TestRawSemanticTitle(t *testing.T) {
	t.Parallel()

	t.Run("uses last user snippet when name is placeholder", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{
			ThreadID:        "thread-1",
			Name:            "新会话",
			LastUserMessage: "  我需要把登录回调重试逻辑补齐，并清掉旧兼容。  ",
		}
		if got := RawSemanticTitle(thread); got != "我需要把登录回调重试逻辑补齐，并清掉旧兼容。" {
			t.Fatalf("RawSemanticTitle() = %q", got)
		}
	})

	t.Run("returns empty for unnamed thread", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "新会话"}
		if got := RawSemanticTitle(thread); got != "" {
			t.Fatalf("RawSemanticTitle() = %q, want empty", got)
		}
	})
}

func TestNormalizeStoredInput(t *testing.T) {
	t.Parallel()

	threadID := "019d56f0-de5e-7943-bc9a-18c42ef11acb"
	shortID := control.ShortenThreadID(threadID)
	ctx := Context{
		ThreadID:     threadID,
		ThreadCWD:    "/data/dl/droid",
		WorkspaceKey: "/data/dl/droid",
	}

	cases := []struct {
		name  string
		title string
		want  string
	}{
		{name: "keeps raw title", title: "修复登录流程", want: "修复登录流程"},
		{name: "strips repeated display prefix and short id", title: "droid · droid · 修复登录流程 · " + shortID, want: "修复登录流程"},
		{name: "clears unnamed display title", title: "droid · 未命名会话", want: ""},
		{name: "clears legacy short id only title", title: "droid · " + shortID, want: ""},
		{name: "clears placeholder raw title", title: "新会话", want: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeStoredInput(tc.title, ctx); got != tc.want {
				t.Fatalf("NormalizeStoredInput() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStoredTitle(t *testing.T) {
	t.Parallel()

	ctx := Context{
		ThreadID:     "thread-1",
		ThreadCWD:    "/data/dl/droid",
		WorkspaceKey: "/data/dl/droid",
	}

	t.Run("prefers semantic title from thread record", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{
			ThreadID:        "thread-1",
			Name:            "新会话",
			LastUserMessage: "把登录流程和恢复链路的 contract 收成一个 owner",
		}
		if got := StoredTitle("droid · 旧标题", ctx, thread); got != "把登录流程和恢复链路的 contract 收成一个 owner" {
			t.Fatalf("StoredTitle() = %q", got)
		}
	})

	t.Run("falls back to legacy snapshot title when thread has no reusable semantic title", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "新会话"}
		if got := StoredTitle("droid · 修复登录流程", ctx, thread); got != "修复登录流程" {
			t.Fatalf("StoredTitle() = %q, want %q", got, "修复登录流程")
		}
	})

	t.Run("does not keep unnamed placeholder as stored raw title", func(t *testing.T) {
		t.Parallel()
		thread := &state.ThreadRecord{ThreadID: "thread-1", Name: "新会话"}
		if got := StoredTitle("droid · 未命名会话", ctx, thread); got != "" {
			t.Fatalf("StoredTitle() = %q, want empty", got)
		}
	})

	t.Run("strips configured alias prefix", func(t *testing.T) {
		t.Parallel()
		ctx := Context{
			ThreadID:     "thread-1",
			ThreadCWD:    "/data/dl/site",
			WorkspaceKey: "/data/dl/site",
			DisplayNames: map[string]string{"/data/dl/site": "claude-remote-workspace"},
		}
		if got := NormalizeStoredInput("claude-remote-workspace · 修复登录流程", ctx); got != "修复登录流程" {
			t.Fatalf("NormalizeStoredInput() = %q, want %q", got, "修复登录流程")
		}
	})
}
