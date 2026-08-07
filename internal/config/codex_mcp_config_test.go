package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexMCPServerDeclaredFromConfigFile(t *testing.T) {
	codexHome := t.TempDir()
	writeCodexConfigForTest(t, codexHome, `
[mcp_servers.node_repl]
command = "/Applications/ChatGPT.app/Contents/Resources/node"
args = ["node_repl.mjs"]
`)

	declared, err := CodexMCPServerDeclared(
		[]string{"app-server"},
		[]string{"CODEX_HOME=" + codexHome},
		"node_repl",
	)
	if err != nil {
		t.Fatalf("CodexMCPServerDeclared: %v", err)
	}
	if !declared {
		t.Fatal("expected node_repl declared in config file")
	}
}

func TestCodexMCPServerDeclaredFromLaunchOverride(t *testing.T) {
	tests := [][]string{
		{"app-server", "-c", `mcp_servers.node_repl.url="http://127.0.0.1:9800"`},
		{"app-server", `--config=mcp_servers.node_repl.url="http://127.0.0.1:9800"`},
		{"app-server", `-cmcp_servers.node_repl.url="http://127.0.0.1:9800"`},
	}
	for _, args := range tests {
		declared, err := CodexMCPServerDeclared(args, []string{"CODEX_HOME=" + t.TempDir()}, "node_repl")
		if err != nil {
			t.Fatalf("CodexMCPServerDeclared(%#v): %v", args, err)
		}
		if !declared {
			t.Fatalf("expected node_repl declared by args %#v", args)
		}
	}
}

func TestCodexMCPServerDeclaredReturnsFalseWhenAbsent(t *testing.T) {
	codexHome := t.TempDir()
	writeCodexConfigForTest(t, codexHome, `model = "gpt-5"`)

	declared, err := CodexMCPServerDeclared(
		[]string{"app-server", "-c", `model="gpt-5"`},
		[]string{"CODEX_HOME=" + codexHome},
		"node_repl",
	)
	if err != nil {
		t.Fatalf("CodexMCPServerDeclared: %v", err)
	}
	if declared {
		t.Fatal("did not expect absent node_repl to be declared")
	}
}

func TestCodexMCPServerDeclaredRejectsMalformedConfig(t *testing.T) {
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, codexConfigFileName), []byte("[mcp_servers.node_repl\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := CodexMCPServerDeclared([]string{"app-server"}, []string{"CODEX_HOME=" + codexHome}, "node_repl"); err == nil {
		t.Fatal("expected malformed config error")
	}
}
