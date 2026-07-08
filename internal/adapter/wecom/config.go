package wecom

// Config holds the credentials required to authenticate a WeCom aibot long
// connection. Both values come from the WeCom admin console for the aibot.
type Config struct {
	// BotID identifies the aibot.
	BotID string
	// Secret authenticates the aibot subscription.
	Secret string
}
