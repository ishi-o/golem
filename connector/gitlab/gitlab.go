// Package gitlab translates GitLab project and group webhooks into golem
// observations. GitLab's shared token authenticates the endpoint but does not
// sign the body, so deployments must protect the endpoint with TLS.
package gitlab

import (
	"crypto/hmac"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ishi-o/golem/connector/webhook"
	"github.com/ishi-o/golem/core/observing"
)

const (
	Name = "gitlab"

	tokenHeader    = "X-Gitlab-Token"
	deliveryHeader = "X-Gitlab-Event-UUID"
	eventHeader    = "X-Gitlab-Event"
)

// Source is the stateless GitLab webhook adapter. Clock is optional and only
// affects the fallback delivery id used by older GitLab versions that omit an
// event UUID.
type Source struct {
	Clock func() time.Time
}

// NewSource constructs a GitLab source. The zero value is ready to use too.
func NewSource() Source { return Source{} }

// NewHandler mounts a GitLab webhook endpoint.
func NewHandler(intake observing.EventIntake, secret string, options ...webhook.HandlerOption) http.Handler {
	return webhook.NewHandler(Source{}, intake, secret, options...)
}

// Name implements webhook.Source.
func (Source) Name() string { return Name }

// Verify implements webhook.Source using GitLab's shared token header. The
// token is compared in constant time, but unlike an HMAC it is not bound to
// the request body.
func (Source) Verify(headers http.Header, body []byte, secret string) bool {
	if strings.TrimSpace(secret) == "" {
		return false
	}
	presented := webhook.HeaderValue(headers, tokenHeader)
	return presented != "" && hmac.Equal([]byte(secret), []byte(presented))
}

// Observe implements webhook.Source. Callers using Source directly must call
// Verify first; Handler enforces that order for HTTP deliveries.
func (s Source) Observe(headers http.Header, body []byte) (observing.Observation, bool, error) {
	root, err := webhook.DecodeObject(body)
	if err != nil {
		return observing.Observation{}, false, err
	}
	kind := eventKind(root, webhook.HeaderValue(headers, eventHeader))
	if kind == "" {
		return observing.Observation{}, false, nil
	}
	project := projectPath(root)
	iid, hasIID := issueIID(root)
	return observing.Observation{
		Source:         Name,
		DeliveryID:     s.deliveryID(headers, body),
		Kind:           kind,
		CorrelationKey: correlationKey(project, iid, hasIID, kind),
		Title:          title(project, iid, hasIID, kind),
		Summary:        summary(root, kind, project, iid, hasIID),
		PayloadJSON:    string(body),
		Actor:          observing.AuthenticatedActor(actor(root)),
	}, true, nil
}

func (s Source) deliveryID(headers http.Header, body []byte) string {
	if value := strings.TrimSpace(webhook.HeaderValue(headers, deliveryHeader)); value != "" {
		return value
	}
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock()
	}
	return Name + ":body:" + webhook.SHA256Hex(body) + ":" + formatBucket(now)
}

func formatBucket(now time.Time) string {
	return strconv.FormatInt(now.Unix()/60, 10)
}

func eventKind(root map[string]any, header string) string {
	base := webhook.TextField(root, "object_kind")
	if base == "" {
		base = slug(header)
	}
	if base == "" {
		return ""
	}
	action := firstText(
		webhook.TextField(webhook.ObjectField(root, "object_attributes"), "action"),
		webhook.TextField(root, "action"),
	)
	if action != "" {
		return base + "." + action
	}
	return base
}

func projectPath(root map[string]any) string {
	return firstText(
		webhook.TextField(webhook.ObjectField(root, "project"), "path_with_namespace"),
		webhook.TextField(webhook.ObjectField(root, "project"), "name"),
		webhook.TextField(webhook.ObjectField(root, "repository"), "name"),
	)
}

func issueIID(root map[string]any) (int, bool) {
	if value, ok := webhook.IntField(webhook.ObjectField(root, "object_attributes"), "iid"); ok {
		return value, true
	}
	for _, field := range []string{"merge_request", "issue"} {
		if value, ok := webhook.IntField(webhook.ObjectField(root, field), "iid"); ok {
			return value, true
		}
	}
	return 0, false
}

func correlationKey(project string, iid int, hasIID bool, kind string) string {
	if project == "" {
		return Name + ":" + kind
	}
	if hasIID {
		return Name + ":" + project + "!" + strconv.Itoa(iid)
	}
	return Name + ":" + project + ":" + kind
}

func title(project string, iid int, hasIID bool, kind string) string {
	if project == "" {
		return "GitLab " + kind
	}
	if hasIID {
		return project + "!" + strconv.Itoa(iid)
	}
	return project + " " + kind
}

func summary(root map[string]any, kind, project string, iid int, hasIID bool) string {
	line := kind
	if project != "" {
		line += " in " + project
		if hasIID {
			line += "!" + strconv.Itoa(iid)
		}
	}
	headline := firstText(
		webhook.TextField(webhook.ObjectField(root, "object_attributes"), "title"),
		webhook.TextField(webhook.ObjectField(root, "object_attributes"), "status"),
		webhook.TextField(webhook.ObjectField(root, "merge_request"), "title"),
		webhook.TextField(webhook.ObjectField(root, "issue"), "title"),
	)
	if headline != "" {
		line += ": " + headline
	}
	if value := actor(root); value != "" {
		line += " (by " + value + ")"
	}
	return line
}

func actor(root map[string]any) string {
	return firstText(
		webhook.TextField(webhook.ObjectField(root, "user"), "username"),
		webhook.TextField(root, "user_username"),
	)
}

func slug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if strings.HasSuffix(value, " hook") {
		value = strings.TrimSpace(strings.TrimSuffix(value, " hook"))
	}
	var out strings.Builder
	underscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			underscore = false
			continue
		}
		if out.Len() > 0 && !underscore {
			out.WriteByte('_')
			underscore = true
		}
	}
	return strings.TrimSuffix(out.String(), "_")
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
