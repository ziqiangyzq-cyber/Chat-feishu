package config

import (
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// CodexMCPServerDeclared reports whether a Codex MCP server is present in
// either launch overrides or the effective Codex config file.
func CodexMCPServerDeclared(args, env []string, serverID string) (bool, error) {
	prefix := "mcp_servers." + strings.TrimSpace(serverID)
	if prefix == "mcp_servers." {
		return false, nil
	}
	if codexOverrideKeyDeclared(args, prefix) {
		return true, nil
	}

	configPath, err := resolveCodexConfigPath(env)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var decoded map[string]any
	metadata, err := toml.Decode(string(raw), &decoded)
	if err != nil {
		return false, err
	}
	return metadata.IsDefined("mcp_servers", strings.TrimSpace(serverID)), nil
}

func codexOverrideKeyDeclared(args []string, prefix string) bool {
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		var raw string
		switch {
		case (arg == "-c" || arg == "--config") && index+1 < len(args):
			raw = args[index+1]
			index++
		case strings.HasPrefix(arg, "--config="):
			raw = strings.TrimPrefix(arg, "--config=")
		case len(arg) > 2 && strings.HasPrefix(arg, "-c"):
			raw = arg[2:]
		default:
			continue
		}
		key, _, ok := strings.Cut(strings.TrimSpace(raw), "=")
		key = strings.TrimSpace(key)
		if ok && (key == prefix || strings.HasPrefix(key, prefix+".")) {
			return true
		}
	}
	return false
}
