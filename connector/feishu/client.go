// Package feishu contains the Feishu/Lark connector. It uses the REST API
// directly so the connector does not make the public core depend on a large
// generated SDK. The client is intentionally small: the agent surface needs
// tenant authentication, text messages, interactive cards, and card updates.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ReceiveIDType selects the kind of identifier used by a message call.
type ReceiveIDType string

const (
	ReceiveIDOpenID  ReceiveIDType = "open_id"
	ReceiveIDUserID  ReceiveIDType = "user_id"
	ReceiveIDUnionID ReceiveIDType = "union_id"
	ReceiveIDEmail   ReceiveIDType = "email"
	ReceiveIDChatID  ReceiveIDType = "chat_id"
)

// ClientConfig configures the Feishu API client.
type ClientConfig struct {
	AppID            string
	AppSecret        string
	BaseURL          string
	HTTPClient       *http.Client
	Logger           *slog.Logger
	TokenRefreshSkew time.Duration
}

// Client is a tenant-authenticated Feishu REST client. It is safe for use by
// concurrent webhook runs.
type Client struct {
	appID            string
	appSecret        string
	baseURL          string
	httpClient       *http.Client
	log              *slog.Logger
	tokenRefreshSkew time.Duration

	mu         sync.Mutex
	token      string
	tokenUntil time.Time
}

// NewClient validates and constructs a client.
func NewClient(config ClientConfig) (*Client, error) {
	if strings.TrimSpace(config.AppID) == "" || strings.TrimSpace(config.AppSecret) == "" {
		return nil, errors.New("feishu: app id and app secret are required")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://open.feishu.cn"
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	parsedURL, err := url.Parse(config.BaseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		if err == nil {
			err = errors.New("URL must include a scheme and host")
		}
		return nil, fmt.Errorf("feishu: invalid base URL: %w", err)
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.TokenRefreshSkew <= 0 {
		config.TokenRefreshSkew = time.Minute
	}
	return &Client{appID: config.AppID, appSecret: config.AppSecret, baseURL: config.BaseURL, httpClient: config.HTTPClient, log: config.Logger, tokenRefreshSkew: config.TokenRefreshSkew}, nil
}

type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int64  `json:"expire"`
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	now := time.Now()
	c.mu.Lock()
	if c.token != "" && now.Add(c.tokenRefreshSkew).Before(c.tokenUntil) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	payload, err := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	var response tokenResponse
	if err := c.do(request, &response); err != nil {
		return "", err
	}
	if response.Code != 0 || response.TenantAccessToken == "" {
		return "", apiError("tenant access token", response.Code, response.Msg)
	}
	c.mu.Lock()
	c.token = response.TenantAccessToken
	c.tokenUntil = time.Now().Add(time.Duration(response.Expire) * time.Second)
	token := c.token
	c.mu.Unlock()
	return token, nil
}

// SendText sends a plain text message and returns the Feishu message id.
func (c *Client) SendText(ctx context.Context, receiveType ReceiveIDType, receiveID, text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	return c.sendMessage(ctx, receiveType, receiveID, "text", content)
}

// SendCard sends an interactive card. card may be any JSON-marshalable value
// matching Feishu's card schema.
func (c *Client) SendCard(ctx context.Context, receiveType ReceiveIDType, receiveID string, card any) (string, error) {
	content, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("feishu: encode card: %w", err)
	}
	return c.sendMessage(ctx, receiveType, receiveID, "interactive", content)
}

func (c *Client) sendMessage(ctx context.Context, receiveType ReceiveIDType, receiveID, messageType string, content []byte) (string, error) {
	if receiveType == "" || strings.TrimSpace(receiveID) == "" {
		return "", errors.New("feishu: receive id type and receive id are required")
	}
	payload := map[string]string{"receive_id": receiveID, "msg_type": messageType, "content": string(content)}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	endpoint := c.baseURL + "/open-apis/im/v1/messages?receive_id_type=" + url.QueryEscape(string(receiveType))
	response, err := c.doAuthorized(ctx, http.MethodPost, endpoint, data)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return "", fmt.Errorf("feishu: decode send response: %w", err)
	}
	if envelope.Code != 0 {
		return "", apiError("send message", envelope.Code, envelope.Msg)
	}
	if envelope.Data.MessageID == "" {
		return "", errors.New("feishu: send message response has no message id")
	}
	return envelope.Data.MessageID, nil
}

// ReplyText replies to an existing message, preserving the thread when the
// channel supports it.
func (c *Client) ReplyText(ctx context.Context, messageID, text string) (string, error) {
	if strings.TrimSpace(messageID) == "" {
		return "", errors.New("feishu: message id is required")
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{"msg_type": "text", "content": string(content)})
	if err != nil {
		return "", err
	}
	response, err := c.doAuthorized(ctx, http.MethodPost, c.baseURL+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", payload)
	if err != nil {
		return "", err
	}
	return decodeMessageID("reply message", response)
}

// UpdateCard replaces the content of an existing interactive card.
func (c *Client) UpdateCard(ctx context.Context, messageID string, card any) error {
	if strings.TrimSpace(messageID) == "" {
		return errors.New("feishu: message id is required")
	}
	content, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("feishu: encode card update: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"content": string(content)})
	if err != nil {
		return err
	}
	response, err := c.doAuthorized(ctx, http.MethodPatch, c.baseURL+"/open-apis/im/v1/messages/"+url.PathEscape(messageID), payload)
	if err != nil {
		return err
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return fmt.Errorf("feishu: decode card update response: %w", err)
	}
	if envelope.Code != 0 {
		return apiError("update card", envelope.Code, envelope.Msg)
	}
	return nil
}

func (c *Client) doAuthorized(ctx context.Context, method, endpoint string, payload []byte) ([]byte, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	var response json.RawMessage
	if err := c.do(request, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) do(request *http.Request, target any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("feishu: HTTP request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("feishu: read response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("feishu: HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("feishu: decode response: %w", err)
	}
	return nil
}

func decodeMessageID(operation string, payload []byte) (string, error) {
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", fmt.Errorf("feishu: decode %s response: %w", operation, err)
	}
	if envelope.Code != 0 {
		return "", apiError(operation, envelope.Code, envelope.Msg)
	}
	if envelope.Data.MessageID == "" {
		return "", fmt.Errorf("feishu: %s response has no message id", operation)
	}
	return envelope.Data.MessageID, nil
}

func apiError(operation string, code int, message string) error {
	return fmt.Errorf("feishu: %s failed with code %d: %s", operation, code, message)
}
