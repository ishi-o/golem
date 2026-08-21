package agent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

func TestFeishuClientUsesSDKMessageAPIs(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			return jsonResponse(r, map[string]any{
				"code":                0,
				"msg":                 "ok",
				"tenant_access_token": "tenant-token",
				"expire":              3600,
			}), nil
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Errorf("receive_id_type = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Errorf("authorization = %q", got)
			}
			return jsonResponse(r, map[string]any{"code": 0, "msg": "ok", "data": map[string]string{"message_id": "om-created"}}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reply"):
			return jsonResponse(r, map[string]any{"code": 0, "msg": "ok", "data": map[string]string{"message_id": "om-replied"}}), nil
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			return jsonResponse(r, map[string]any{"code": 0, "msg": "ok"}), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":404,"msg":"not found"}`)),
				Request:    r,
			}, nil
		}
	})

	client, err := feishu.NewClient(feishu.ClientConfig{
		AppID:     "app-id",
		AppSecret: "app-secret",
		BaseURL:   "https://example.test",
		HTTPClient: &http.Client{
			Transport: transport,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := client.SendText(t.Context(), feishu.ReceiveIDChatID, "oc-chat", "hello")
	if err != nil || created != "om-created" {
		t.Fatalf("SendText() = %q, %v", created, err)
	}
	replied, err := client.ReplyText(t.Context(), created, "answer")
	if err != nil || replied != "om-replied" {
		t.Fatalf("ReplyText() = %q, %v", replied, err)
	}
	if err := client.UpdateCard(t.Context(), replied, map[string]string{"content": "done"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(paths, ","); got != "POST /open-apis/auth/v3/tenant_access_token/internal,POST /open-apis/im/v1/messages,POST /open-apis/im/v1/messages/om-created/reply,PATCH /open-apis/im/v1/messages/om-replied" {
		t.Fatalf("SDK request sequence = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(request *http.Request, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}
}
