package agent_test

import (
	"testing"

	"github.com/ishi-o/golem/connector/feishu"
)

func TestDecodeFeishuMessageEvent(t *testing.T) {
	event, err := feishu.DecodeMessageEvent([]byte(`{
		"header":{"event_id":"evt-1","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou-user"}},
			"message":{"message_id":"om-1","chat_id":"oc-chat","chat_type":"group","content":"{\"text\":\" hello \"}"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.EventID != "evt-1" || event.MessageID != "om-1" || event.UserID != "ou-user" || event.ChatID != "oc-chat" || event.Text != "hello" {
		t.Fatalf("decoded event = %+v", event)
	}
}

func TestFeishuClientRequiresAbsoluteBaseURL(t *testing.T) {
	if _, err := feishu.NewClient(feishu.ClientConfig{AppID: "app", AppSecret: "secret", BaseURL: "localhost"}); err == nil {
		t.Fatal("relative base URL was accepted")
	}
}
