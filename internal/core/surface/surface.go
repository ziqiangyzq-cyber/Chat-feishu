// Package surface defines the channel-neutral contract that decouples the
// daemon's control/event core from any specific messaging backend.
//
// Historically the daemon was wired directly to Feishu (see
// internal/adapter/feishu). This package captures the minimal, backend-agnostic
// surface that both Feishu and WeCom (企业微信 / WeChat Work aibot) implement, so
// that additional channels can be added without touching core control flow.
//
// Layering rule: this package lives in internal/core and MUST NOT import any
// adapter package (e.g. internal/adapter/feishu). Adapters depend on core, never
// the other way around. It may import only stdlib, internal/core/control and
// internal/core/eventcontract.
//
// The Operation type is a marker interface: each adapter defines its own
// concrete operation representation (Feishu cards, WeCom template_cards, ...)
// and tags it as a surface.Operation. Core code that needs to pass operations
// around treats them opaquely; only the owning adapter interprets them.
package surface

import (
	"context"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

// Operation is a marker interface implemented by each adapter's concrete
// rendering operation type. Core code carries these opaquely; the owning
// adapter is the only thing that interprets them.
//
// The marker method is exported (IsSurfaceOperation) rather than unexported
// because Go binds unexported method identity to the declaring package: an
// unexported marker could only ever be satisfied from within this package,
// making it impossible for adapter packages (feishu, wecom) to implement it.
type Operation interface {
	IsSurfaceOperation()
}

// ActionHandler processes an inbound, channel-neutral control.Action and may
// return an ActionResult describing an immediate in-place response.
type ActionHandler func(context.Context, control.Action) *ActionResult

// ActionResult carries the optional immediate response to an inbound action.
// ReplaceCurrentCard, when non-nil, asks the channel to replace the card/message
// the action originated from with the provided operation.
type ActionResult struct {
	ReplaceCurrentCard Operation
}

// Capabilities describes what a channel can and cannot do, so callers can adapt
// their rendering strategy without hardcoding per-channel branches.
type Capabilities struct {
	// Streaming reports whether the channel supports incremental (streamed)
	// text updates to a single logical message.
	Streaming bool
	// InteractiveSameFrame reports whether streaming text and interactive
	// buttons can coexist in a single message.
	//
	// Feishu = true (a card can stream markdown and host action buttons at
	// once). WeCom = false (aibot streaming text and template_card interactive
	// buttons cannot be combined in the same message; they must be sent as
	// separate messages).
	InteractiveSameFrame bool
	// FileSend reports whether the channel can send file attachments.
	FileSend bool
	// MaxButtons is the maximum number of interactive buttons the channel
	// allows in a single interactive message. Zero means unspecified.
	MaxButtons int
}

// Channel is the channel-neutral contract that a messaging backend implements.
// Both Feishu and WeCom satisfy this interface.
type Channel interface {
	// Name returns a stable channel identifier (e.g. "feishu", "wecom").
	Name() string
	// Start begins receiving inbound actions, dispatching them to the handler.
	// It should block until the context is cancelled or a fatal error occurs.
	Start(context.Context, ActionHandler) error
	// Deliver renders and sends an outbound event to the given chat.
	Deliver(ctx context.Context, chatID string, event eventcontract.Event) error
	// Stop releases any resources held by the channel.
	Stop(context.Context) error
	// Capabilities reports the channel's feature matrix.
	Capabilities() Capabilities
}

// StreamingRenderer is an optional capability, probed by type-assertion, for
// channels that can push incremental markdown updates to a single message.
type StreamingRenderer interface {
	// RenderStream sends (or updates) streamed markdown for chatID. When finish
	// is true, the stream is finalized and no further updates will follow.
	RenderStream(ctx context.Context, chatID string, markdown string, finish bool) error
}

// InteractiveCard is an optional capability, probed by type-assertion, for
// channels that can render an interactive target-picker view. It returns the
// adapter's concrete operation so the caller can, e.g., stash it for later
// replacement.
type InteractiveCard interface {
	RenderInteractive(ctx context.Context, chatID string, view *control.FeishuTargetPickerView) (Operation, error)
}
