package gateway

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const gateTestBotOpenID = "ou_bot_self"

func gateTestEnv() InboundEnv {
	return InboundEnv{
		GatewayID:                     "app-gate",
		BotOpenID:                     gateTestBotOpenID,
		ParseTextActionWithoutCatalog: parseTextAction,
		RecordSurfaceMessage:          func(string, string) {},
	}
}

func gateTestTextEvent(chatType, content string, mentions []*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: stringRef("ou_human")},
			},
			Message: &larkim.EventMessage{
				MessageId:   stringRef("om-gate"),
				ChatId:      stringRef("oc_chat"),
				ChatType:    stringRef(chatType),
				MessageType: stringRef("text"),
				Content:     stringRef(content),
				Mentions:    mentions,
			},
		},
	}
}

func botMention() []*larkim.MentionEvent {
	return []*larkim.MentionEvent{
		{
			Key:  stringRef("@_user_1"),
			Id:   &larkim.UserId{OpenId: stringRef(gateTestBotOpenID)},
			Name: stringRef("EFC Structa"),
		},
	}
}

func humanMention() []*larkim.MentionEvent {
	return []*larkim.MentionEvent{
		{
			Key:  stringRef("@_user_1"),
			Id:   &larkim.UserId{OpenId: stringRef("ou_someone_else")},
			Name: stringRef("郑子杰"),
		},
	}
}

func TestGroupPlainTextWithoutBotMentionIsIgnored(t *testing.T) {
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), gateTestTextEvent("group", `{"text":"下周四四性测试谁去"}`, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || planned.Queue != nil || planned.Action != nil {
		t.Fatalf("expected unaddressed group chatter to be ignored, got ok=%v planned=%#v", ok, planned)
	}
}

func TestGroupPlainTextMentioningOtherUserIsIgnored(t *testing.T) {
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), gateTestTextEvent("group", `{"text":"@_user_1 你去吗"}`, humanMention()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || planned.Queue != nil {
		t.Fatalf("expected message @mentioning a human (not the bot) to be ignored, got ok=%v planned=%#v", ok, planned)
	}
}

func TestGroupPlainTextMentioningBotIsQueued(t *testing.T) {
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), gateTestTextEvent("group", `{"text":"@_user_1 帮我看看"}`, botMention()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || planned.Queue == nil {
		t.Fatalf("expected message @mentioning the bot to be queued, got ok=%v planned=%#v", ok, planned)
	}
}

func TestGroupCommandWithoutMentionStillHandled(t *testing.T) {
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), gateTestTextEvent("group", `{"text":"/list"}`, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || planned.Action == nil {
		t.Fatalf("expected command to be handled even without @mention, got ok=%v planned=%#v", ok, planned)
	}
}

func TestP2PPlainTextIsQueuedWithoutMention(t *testing.T) {
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), gateTestTextEvent("p2p", `{"text":"hi"}`, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || planned.Queue == nil {
		t.Fatalf("expected p2p plain text to be queued, got ok=%v planned=%#v", ok, planned)
	}
}

func TestGroupGateFailsOpenWhenBotOpenIDUnknown(t *testing.T) {
	env := gateTestEnv()
	env.BotOpenID = ""
	planned, ok, err := PlanInboundMessageEvent(env, gateTestTextEvent("group", `{"text":"random chatter"}`, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || planned.Queue == nil {
		t.Fatalf("expected gate to fail open (queue) when bot open_id is unknown, got ok=%v planned=%#v", ok, planned)
	}
}

func TestGroupImageWithoutBotMentionIsIgnored(t *testing.T) {
	event := gateTestTextEvent("group", `{"image_key":"img_key_1"}`, nil)
	event.Event.Message.MessageType = stringRef("image")
	planned, ok, err := PlanInboundMessageEvent(gateTestEnv(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || planned.Queue != nil {
		t.Fatalf("expected unaddressed group image to be ignored, got ok=%v planned=%#v", ok, planned)
	}
}

func TestFeishuMentionsIncludeBot(t *testing.T) {
	if !feishuMentionsIncludeBot(botMention(), gateTestBotOpenID) {
		t.Fatal("expected bot mention to be detected")
	}
	if feishuMentionsIncludeBot(humanMention(), gateTestBotOpenID) {
		t.Fatal("did not expect a human mention to match the bot")
	}
	if feishuMentionsIncludeBot(botMention(), "") {
		t.Fatal("empty bot open_id must never match")
	}
	if feishuMentionsIncludeBot(nil, gateTestBotOpenID) {
		t.Fatal("nil mentions must not match")
	}
}
