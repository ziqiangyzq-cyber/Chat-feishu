package wecom

import "time"

// Config holds the credentials required to authenticate a WeCom aibot long
// connection. Both values come from the WeCom admin console for the aibot.
type Config struct {
	// BotID identifies the aibot.
	BotID string
	// Secret authenticates the aibot subscription.
	Secret string
	// CallbackAESKey decrypts inbound image/file payloads when the callback
	// frame omits its per-message aeskey.
	CallbackAESKey string
	// TempDir is the shared inbound-media staging directory. Empty uses the
	// operating system temp directory.
	TempDir string
	// SessionIdle is how long a chat may stay quiet before ephemeral state
	// (callback req_id bindings, open stream ids) is dropped. Zero disables
	// idle reaping. Default applied by NewChannel is 30 minutes.
	SessionIdle time.Duration
	// MaxTurn is a soft wall-clock budget for a single streaming turn. When
	// exceeded, the channel finalizes the open stream with a timeout notice
	// so the user is not left staring at a half-finished reply. Zero disables.
	MaxTurn time.Duration
}
