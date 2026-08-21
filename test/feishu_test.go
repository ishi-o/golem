package agent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ishi-o/golem/connector/feishu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeFeishuMessageEvent(t *testing.T) {
	event, err := feishu.DecodeMessageEvent([]byte(`{
		"header":{"event_id":"evt-1","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou-user"}},
			"message":{"message_id":"om-1","chat_id":"oc-chat","chat_type":"group","content":"{\"text\":\" hello \"}"}}
	}`))
	require.NoError(t, err)
	assert.Equal(t, "evt-1", event.EventID)
	assert.Equal(t, "om-1", event.MessageID)
	assert.Equal(t, "ou-user", event.UserID)
	assert.Equal(t, "oc-chat", event.ChatID)
	assert.Equal(t, "hello", event.Text)
}

func TestFeishuClientRequiresAbsoluteBaseURL(t *testing.T) {
	if _, err := feishu.NewClient(feishu.ClientConfig{AppID: "app", AppSecret: "secret", BaseURL: "localhost"}); err == nil {
		require.FailNow(t, "relative base URL was accepted")
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
				assert.Equal(t, "chat_id", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				assert.Equal(t, "Bearer tenant-token", got)
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
	require.NoError(t, err)

	created, err := client.SendText(t.Context(), feishu.ReceiveIDChatID, "oc-chat", "hello")
	require.NoError(t, err)
	assert.Equal(t, "om-created", created)
	replied, err := client.ReplyText(t.Context(), created, "answer")
	require.NoError(t, err)
	assert.Equal(t, "om-replied", replied)
	require.NoError(t, client.UpdateCard(t.Context(), replied, map[string]string{"content": "done"}))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t,
		"POST /open-apis/auth/v3/tenant_access_token/internal,POST /open-apis/im/v1/messages,POST /open-apis/im/v1/messages/om-created/reply,PATCH /open-apis/im/v1/messages/om-replied",
		strings.Join(paths, ","),
	)
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
