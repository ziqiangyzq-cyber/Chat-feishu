package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bridgeContext is generated from the verified Feishu event rather than from
// user text. It is deliberately rendered as a prompt preamble so downstream
// Codex sessions receive the event identity even when the outer bridge cannot
// attach a separate context envelope.
type bridgeContext struct {
	Surface    string   `json:"surface"`
	SenderID   string   `json:"senderId"`
	SenderName string   `json:"senderName"`
	ChatID     string   `json:"chatId"`
	MessageIDs []string `json:"messageIds"`
}

func makeBridgeContext(surface, senderID, chatID, messageID, relatedMessageID string) (string, error) {
	surface = strings.TrimSpace(surface)
	senderID = strings.TrimSpace(senderID)
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	relatedMessageID = strings.TrimSpace(relatedMessageID)
	if surface == "" || senderID == "" || chatID == "" || messageID == "" {
		return "", fmt.Errorf("incomplete Feishu bridge context: surface=%q senderId=%q chatId=%q messageId=%q", surface, senderID, chatID, messageID)
	}
	ids := []string{messageID}
	if relatedMessageID != "" && relatedMessageID != messageID {
		ids = append(ids, relatedMessageID)
	}
	payload, err := json.Marshal(bridgeContext{
		Surface:    surface,
		SenderID:   senderID,
		SenderName: senderID,
		ChatID:     chatID,
		MessageIDs: ids,
	})
	if err != nil {
		return "", fmt.Errorf("marshal Feishu bridge context: %w", err)
	}
	return "<bridge_context>\n" + string(payload) + "\n</bridge_context>", nil
}
