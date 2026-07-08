package feishu

import "github.com/kxn/codex-remote-feishu/internal/core/surface"

// IsSurfaceOperation tags feishu.Operation as a surface.Operation. This is an
// additive marker method: it introduces no behavior and does not alter the
// existing Operation semantics. It lets core code carry Feishu operations
// opaquely through the channel-neutral surface contract.
func (Operation) IsSurfaceOperation() {}

// Compile-time assertion that feishu.Operation satisfies surface.Operation.
var _ surface.Operation = Operation{}
