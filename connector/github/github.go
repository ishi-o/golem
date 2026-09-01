// Package github translates GitHub repository webhooks into golem
// observations. It contains no GitHub client: applications mount the HTTP
// handler and decide how the event intake is persisted and triaged.
package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/ishi-o/golem/connector/webhook"
	"github.com/ishi-o/golem/core/observing"
)

const (
	Name = "github"

	signatureHeader = "X-Hub-Signature-256"
	deliveryHeader  = "X-GitHub-Delivery"
	eventHeader     = "X-GitHub-Event"
)

// Source is the stateless GitHub webhook adapter.
type Source struct{}

// NewSource constructs a GitHub source. The zero value is ready to use too.
func NewSource() Source { return Source{} }

// NewHandler mounts a GitHub webhook endpoint.
func NewHandler(intake observing.EventIntake, secret string, options ...webhook.HandlerOption) http.Handler {
	return webhook.NewHandler(Source{}, intake, secret, options...)
}

// Name implements webhook.Source.
func (Source) Name() string { return Name }

// Verify implements webhook.Source using GitHub's SHA-256 HMAC over the raw
// request body. The weaker SHA-1 signature header is deliberately ignored.
func (Source) Verify(headers http.Header, body []byte, secret string) bool {
	if strings.TrimSpace(secret) == "" {
		return false
	}
	header := webhook.HeaderValue(headers, signatureHeader)
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	presented, err := hex.DecodeString(header[len("sha256="):])
	if err != nil || len(presented) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), presented)
}

// Observe implements webhook.Source. Callers using Source directly must call
// Verify first; Handler enforces that order for HTTP deliveries.
func (Source) Observe(headers http.Header, body []byte) (observing.Observation, bool, error) {
	event := strings.TrimSpace(webhook.HeaderValue(headers, eventHeader))
	if event == "" || strings.EqualFold(event, "ping") {
		return observing.Observation{}, false, nil
	}
	root, err := webhook.DecodeObject(body)
	if err != nil {
		return observing.Observation{}, false, err
	}
	action := webhook.TextField(root, "action")
	kind := event
	if action != "" {
		kind += "." + action
	}
	repository := webhook.TextField(webhook.ObjectField(root, "repository"), "full_name")
	issueNumber, hasNumber := number(root)
	workflow := workflowName(root)
	return observing.Observation{
		Source:         Name,
		DeliveryID:     deliveryID(headers, body),
		Kind:           kind,
		CorrelationKey: correlationKey(repository, issueNumber, hasNumber, workflow, event),
		Title:          title(repository, issueNumber, hasNumber, workflow, kind),
		Summary:        summary(root, kind, repository, issueNumber, hasNumber),
		PayloadJSON:    string(body),
		Actor:          observing.AuthenticatedActor(webhook.TextField(webhook.ObjectField(root, "sender"), "login")),
	}, true, nil
}

func deliveryID(headers http.Header, body []byte) string {
	if value := strings.TrimSpace(webhook.HeaderValue(headers, deliveryHeader)); value != "" {
		return value
	}
	return Name + ":body:" + webhook.SHA256Hex(body)
}

func correlationKey(repository string, number int, hasNumber bool, workflow, event string) string {
	if repository == "" {
		return Name + ":" + event
	}
	if hasNumber {
		return Name + ":" + repository + "#" + strconv.Itoa(number)
	}
	if workflow != "" {
		return Name + ":" + repository + ":workflow:" + workflow
	}
	return Name + ":" + repository + ":" + event
}

func number(root map[string]any) (int, bool) {
	for _, field := range []string{"issue", "pull_request", "discussion"} {
		if value, ok := webhook.IntField(webhook.ObjectField(root, field), "number"); ok {
			return value, true
		}
	}
	return webhook.IntField(root, "number")
}

func workflowName(root map[string]any) string {
	if value := webhook.TextField(webhook.ObjectField(root, "workflow_run"), "name"); value != "" {
		return value
	}
	if value := webhook.TextField(webhook.ObjectField(root, "workflow_job"), "workflow_name"); value != "" {
		return value
	}
	return webhook.TextField(webhook.ObjectField(root, "workflow"), "name")
}

func title(repository string, number int, hasNumber bool, workflow, kind string) string {
	if repository == "" {
		return "GitHub " + kind
	}
	if hasNumber {
		return repository + "#" + strconv.Itoa(number)
	}
	if workflow != "" {
		return repository + " workflow " + workflow
	}
	return repository + " " + kind
}

func summary(root map[string]any, kind, repository string, number int, hasNumber bool) string {
	line := kind
	if repository != "" {
		line += " in " + repository
		if hasNumber {
			line += "#" + strconv.Itoa(number)
		}
	}
	headline := firstText(
		webhook.TextField(webhook.ObjectField(root, "issue"), "title"),
		webhook.TextField(webhook.ObjectField(root, "pull_request"), "title"),
		webhook.TextField(webhook.ObjectField(root, "discussion"), "title"),
		webhook.TextField(webhook.ObjectField(root, "workflow_run"), "conclusion"),
		webhook.TextField(webhook.ObjectField(root, "workflow_job"), "conclusion"),
		webhook.TextField(webhook.ObjectField(root, "release"), "name"),
	)
	if headline != "" {
		line += ": " + headline
	}
	if actor := webhook.TextField(webhook.ObjectField(root, "sender"), "login"); actor != "" {
		line += " (by " + actor + ")"
	}
	return line
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
