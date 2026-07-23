package relayruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartDetachedDaemonFailsWithoutBinary(t *testing.T) {
	paths := testPaths(t)
	_, err := StartDetachedDaemon(LaunchOptions{
		BinaryPath: "",
		Paths:      paths,
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("StartDetachedDaemon error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(paths.DaemonLogFile); err != nil {
		t.Fatalf("expected daemon log file to be created before failure, stat err=%v", err)
	}
}

func TestStartDetachedWrapperFailsForMissingBinary(t *testing.T) {
	paths := testPaths(t)
	_, err := StartDetachedWrapper(HeadlessLaunchOptions{
		BinaryPath: filepath.Join(t.TempDir(), "missing-wrapper"),
		Paths:      paths,
		InstanceID: "chat/abc:1",
	})
	if err == nil {
		t.Fatal("expected StartDetachedWrapper to fail for missing binary")
	}
	logPath := filepath.Join(paths.LogsDir, "codex-remote-headless-chat_abc_1.log")
	if _, statErr := os.Stat(logPath); statErr != nil {
		t.Fatalf("expected sanitized wrapper log file, stat err=%v", statErr)
	}
}

func TestBuildHeadlessWrapperArgsUsesExplicitLaunchMode(t *testing.T) {
	if got := buildHeadlessWrapperArgs(HeadlessLaunchOptions{
		LaunchMode: HeadlessLaunchModeClaudeAppServer,
		Args:       []string{"--flag"},
	}); strings.Join(got, "\x00") != strings.Join([]string{HeadlessLaunchModeClaudeAppServer, "--flag"}, "\x00") {
		t.Fatalf("buildHeadlessWrapperArgs claude = %#v", got)
	}
	if got := buildHeadlessWrapperArgs(HeadlessLaunchOptions{
		LaunchMode: "",
		Args:       []string{"--flag"},
	}); strings.Join(got, "\x00") != strings.Join([]string{HeadlessLaunchModeAppServer, "--flag"}, "\x00") {
		t.Fatalf("buildHeadlessWrapperArgs default = %#v", got)
	}
}

func TestWithWorkingDirectoryEnvReplacesPWD(t *testing.T) {
	env := withWorkingDirectoryEnv([]string{
		"PATH=/usr/bin",
		"PWD=/old/path",
		"HOME=/tmp/home",
	}, "/tmp/中文工作区")
	want := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"PWD=/tmp/中文工作区",
	}
	if strings.Join(env, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("working directory env = %#v, want %#v", env, want)
	}
}

func TestSanitizeFilenameAndTerminateProcessWrapper(t *testing.T) {
	if got := sanitizeFilename(""); got != "unknown" {
		t.Fatalf("sanitizeFilename empty = %q", got)
	}
	if got := sanitizeFilename("Abc-123_.:/\\x"); got != "Abc-123_.___x" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
	if err := TerminateProcess(0, 10*time.Millisecond); err != nil {
		t.Fatalf("TerminateProcess: %v", err)
	}
}
