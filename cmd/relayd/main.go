package main

import (
	"os"

	"github.com/kxn/codex-remote-feishu/internal/app/launcher"
	"github.com/kxn/codex-remote-feishu/internal/buildinfo"
)

var version, branch string

func main() {
	info := buildinfo.CurrentWithLegacy(version, branch)
	os.Exit(launcher.Main(launcher.Options{
		Args:            []string{"daemon"},
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		Version:         info.Version,
		DetailedVersion: info.DetailedVersion(),
		Branch:          info.Branch,
	}))
}
