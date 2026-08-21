// Package feishu contains the optional Feishu/Lark connector.
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
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
	AppID      string
	AppSecret  string
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Client wraps the official Feishu SDK with the small messaging surface used
// by the connector. The SDK owns tenant-token caching and request handling.
type Client struct {
	sdk *lark.Client
}

// NewClient validates and constructs a client.
func NewClient(config ClientConfig) (*Client, error) {
	if strings.TrimSpace(config.AppID) == "" || strings.TrimSpace(config.AppSecret) == "" {
		return nil, errors.New("feishu: app id and app secret are required")
	}
	if config.BaseURL == "" {
		config.BaseURL = lark.FeishuBaseUrl
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

	return &Client{sdk: lark.NewClient(
		config.AppID,
		config.AppSecret,
		lark.WithAppType(larkcore.AppTypeSelfBuilt),
		lark.WithEnableTokenCache(true),
		lark.WithOpenBaseUrl(config.BaseURL),
		lark.WithOAuthBaseUrl(config.BaseURL),
		lark.WithHttpClient(config.HTTPClient),
		lark.WithLogger(sdkLogger{logger: config.Logger}),
	)}, nil
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
	response, err := c.sdk.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(string(receiveType)).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(messageType).
			Content(string(content)).
			Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu: send message: %w", err)
	}
	if !response.Success() {
		return "", apiError("send message", response.Code, response.Msg)
	}
	if response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return "", errors.New("feishu: send message response has no message id")
	}
	return *response.Data.MessageId, nil
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
	response, err := c.sdk.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(content)).
			Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu: reply message: %w", err)
	}
	if !response.Success() {
		return "", apiError("reply message", response.Code, response.Msg)
	}
	if response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return "", errors.New("feishu: reply message response has no message id")
	}
	return *response.Data.MessageId, nil
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
	response, err := c.sdk.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(content)).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu: update card: %w", err)
	}
	if !response.Success() {
		return apiError("update card", response.Code, response.Msg)
	}
	return nil
}

type sdkLogger struct {
	logger *slog.Logger
}

func (l sdkLogger) Debug(ctx context.Context, args ...interface{}) {
	l.logger.DebugContext(ctx, fmt.Sprint(args...))
}

func (l sdkLogger) Info(ctx context.Context, args ...interface{}) {
	l.logger.InfoContext(ctx, fmt.Sprint(args...))
}

func (l sdkLogger) Warn(ctx context.Context, args ...interface{}) {
	l.logger.WarnContext(ctx, fmt.Sprint(args...))
}

func (l sdkLogger) Error(ctx context.Context, args ...interface{}) {
	l.logger.ErrorContext(ctx, fmt.Sprint(args...))
}

func apiError(operation string, code int, message string) error {
	return fmt.Errorf("feishu: %s failed with code %d: %s", operation, code, message)
}
