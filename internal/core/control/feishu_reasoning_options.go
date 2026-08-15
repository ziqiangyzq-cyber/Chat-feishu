package control

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func ReasoningOptionsForBackend(backend agentproto.Backend) []FeishuCommandOption {
	if normalized := agentproto.NormalizeBackend(backend); normalized == agentproto.BackendAgy || normalized == agentproto.BackendGrok {
		return []FeishuCommandOption{
			commandOption("/reasoning", "reasoning", "low", "low", "把后续消息切到 low 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "medium", "medium", "把后续消息切到 medium 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "high", "high", "把后续消息切到 high 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "clear", "clear", "清除临时推理强度覆盖。"),
		}
	}
	if agentproto.NormalizeBackend(backend) == agentproto.BackendClaude {
		return []FeishuCommandOption{
			commandOption("/reasoning", "reasoning", "low", "low", "把后续飞书消息切到 low 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "medium", "medium", "把后续飞书消息切到 medium 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "high", "high", "把后续飞书消息切到 high 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "max", "max", "把后续飞书消息切到 max 推理，直到 clear 或接管清理。"),
			commandOption("/reasoning", "reasoning", "clear", "clear", "清除飞书临时推理强度覆盖。"),
		}
	}
	return []FeishuCommandOption{
		commandOption("/reasoning", "reasoning", "low", "low", "把后续飞书消息切到 low 推理，直到 clear 或接管清理。"),
		commandOption("/reasoning", "reasoning", "medium", "medium", "把后续飞书消息切到 medium 推理，直到 clear 或接管清理。"),
		commandOption("/reasoning", "reasoning", "high", "high", "把后续飞书消息切到 high 推理，直到 clear 或接管清理。"),
		commandOption("/reasoning", "reasoning", "xhigh", "xhigh", "把后续飞书消息切到 xhigh 推理，直到 clear 或接管清理。"),
		commandOption("/reasoning", "reasoning", "clear", "clear", "清除飞书临时推理强度覆盖。"),
	}
}

func NormalizeReasoningEffortForBackend(backend agentproto.Backend, value string) (string, bool) {
	effort := strings.ToLower(strings.TrimSpace(value))
	switch agentproto.NormalizeBackend(backend) {
	case agentproto.BackendClaude:
		switch effort {
		case "low", "medium", "high", "max":
			return effort, true
		default:
			return "", false
		}
	case agentproto.BackendAgy, agentproto.BackendGrok:
		switch effort {
		case "low", "medium", "high":
			return effort, true
		default:
			return "", false
		}
	default:
		switch effort {
		case "low", "medium", "high", "xhigh":
			return effort, true
		default:
			return "", false
		}
	}
}

func ReasoningEffortHintForBackend(backend agentproto.Backend) string {
	if normalized := agentproto.NormalizeBackend(backend); normalized == agentproto.BackendAgy || normalized == agentproto.BackendGrok {
		return "`low`、`medium` 或 `high`"
	}
	if agentproto.NormalizeBackend(backend) == agentproto.BackendClaude {
		return "`low`、`medium`、`high` 或 `max`"
	}
	return "`low`、`medium`、`high` 或 `xhigh`"
}
