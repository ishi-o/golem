// Package grafana translates Grafana unified-alerting webhook batches into
// golem observations. Grafana authenticates a contact point with a bearer or
// basic credential, not a body signature; TLS is therefore required to keep
// the shared secret confidential.
package grafana

import (
	"crypto/hmac"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ishi-o/golem/connector/webhook"
	"github.com/ishi-o/golem/core/observing"
)

const (
	Name = "grafana"

	authorizationHeader = "Authorization"
	deliveryBucket      = 60
	maxCorrelationKey   = 180
	maxSummaryAlerts    = 5
)

// Source is the Grafana unified-alerting webhook adapter. Clock is optional
// and only affects the fallback delivery id because Grafana does not send one.
type Source struct {
	Clock func() time.Time
}

// NewSource constructs a Grafana source. The zero value is ready to use too.
func NewSource() Source { return Source{} }

// NewHandler mounts a Grafana webhook endpoint.
func NewHandler(intake observing.EventIntake, secret string, options ...webhook.HandlerOption) http.Handler {
	return webhook.NewHandler(Source{}, intake, secret, options...)
}

// Name implements webhook.Source.
func (Source) Name() string { return Name }

// Verify implements webhook.Source. Grafana contact points support bearer and
// HTTP basic authentication; in both forms the configured secret is the
// credential, while a basic-auth username is intentionally ignored.
func (Source) Verify(headers http.Header, body []byte, secret string) bool {
	if strings.TrimSpace(secret) == "" {
		return false
	}
	header := webhook.HeaderValue(headers, authorizationHeader)
	lower := strings.ToLower(header)
	switch {
	case strings.HasPrefix(lower, "bearer "):
		return constantTimeEquals(secret, strings.TrimSpace(header[len("bearer "):]))
	case strings.HasPrefix(lower, "basic "):
		return basicPasswordMatches(secret, strings.TrimSpace(header[len("basic "):]))
	default:
		return false
	}
}

func basicPasswordMatches(secret, encoded string) bool {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	credentials := string(decoded)
	separator := strings.IndexByte(credentials, ':')
	if separator < 0 {
		return false
	}
	return constantTimeEquals(secret, credentials[separator+1:])
}

// Observe implements webhook.Source. One Grafana delivery stays one
// observation because Grafana's notification policy has already grouped the
// alerts. Callers using Source directly must call Verify first.
func (s Source) Observe(_ http.Header, body []byte) (observing.Observation, bool, error) {
	root, err := webhook.DecodeObject(body)
	if err != nil {
		return observing.Observation{}, false, err
	}
	alerts := webhook.ArrayField(root, "alerts")
	if len(alerts) == 0 {
		return observing.Observation{}, false, nil
	}
	status := webhook.TextField(root, "status")
	if status == "" {
		status = "firing"
	}
	return observing.Observation{
		Source:         Name,
		DeliveryID:     s.deliveryID(body),
		Kind:           "alert." + status,
		CorrelationKey: correlationKey(root),
		Title:          title(root, alerts),
		Summary:        summary(root, alerts, status),
		PayloadJSON:    string(body),
	}, true, nil
}

func (s Source) deliveryID(body []byte) string {
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock()
	}
	return fmt.Sprintf("%s:%s:%d", Name, webhook.SHA256Hex(body), now.Unix()/deliveryBucket)
}

func correlationKey(root map[string]any) string {
	if groupKey := webhook.TextField(root, "groupKey"); groupKey != "" {
		return Name + ":group:" + shorten(groupKey)
	}
	if groupLabels := labels(webhook.ObjectField(root, "groupLabels")); len(groupLabels) > 0 {
		return Name + ":group:" + shorten(canonical(groupLabels))
	}
	if commonLabels := labels(webhook.ObjectField(root, "commonLabels")); len(commonLabels) > 0 {
		return Name + ":common:" + shorten(canonical(commonLabels))
	}
	return Name + ":ungrouped"
}

func shorten(value string) string {
	if len(value) <= maxCorrelationKey {
		return value
	}
	return webhook.SHA256Hex([]byte(value))
}

func canonical(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(values[key])
		out.WriteByte('\n')
	}
	return out.String()
}

func labels(object map[string]any) map[string]string {
	values := make(map[string]string)
	for key, value := range object {
		if text, ok := value.(string); ok {
			values[key] = text
		}
	}
	return values
}

func title(root map[string]any, alerts []any) string {
	group := labels(webhook.ObjectField(root, "groupLabels"))
	common := labels(webhook.ObjectField(root, "commonLabels"))
	name := firstText(
		group["alertname"], common["alertname"],
		group["rulename"], common["rulename"],
		webhook.TextField(root, "title"),
	)
	if name == "" {
		return strconv.Itoa(len(alerts)) + " Grafana alerts"
	}
	where := firstText(common["instance"], common["service"])
	if where != "" {
		name += " on " + where
	}
	if len(alerts) == 1 {
		return name
	}
	return name + " (" + strconv.Itoa(len(alerts)) + " alerts)"
}

func summary(root map[string]any, alerts []any, status string) string {
	line := strconv.Itoa(len(alerts))
	if len(alerts) == 1 {
		line += " alert "
	} else {
		line += " alerts "
	}
	line += status
	if truncated, ok := webhook.IntField(root, "truncatedAlerts"); ok && truncated > 0 {
		line += ", " + strconv.Itoa(truncated) + " more truncated by Grafana"
	}
	line += ":"
	shown := len(alerts)
	if shown > maxSummaryAlerts {
		shown = maxSummaryAlerts
	}
	for index := 0; index < shown; index++ {
		alert, ok := alerts[index].(map[string]any)
		if !ok {
			continue
		}
		line += "\n- " + describe(alert)
	}
	if len(alerts) > shown {
		line += "\n- and " + strconv.Itoa(len(alerts)-shown) + " more"
	}
	return line
}

func describe(alert map[string]any) string {
	values := labels(webhook.ObjectField(alert, "labels"))
	name := firstText(values["alertname"], values["rulename"], "alert")
	line := name
	if instance := firstText(values["instance"], values["service"]); instance != "" {
		line += " on " + instance
	}
	if severity := values["severity"]; severity != "" {
		line += " [" + severity + "]"
	}
	if status := webhook.TextField(alert, "status"); status != "" {
		line += " " + status
	}
	annotations := webhook.ObjectField(alert, "annotations")
	if detail := firstText(webhook.TextField(annotations, "summary"), webhook.TextField(annotations, "description")); detail != "" {
		line += ": " + detail
	}
	if startsAt := webhook.TextField(alert, "startsAt"); startsAt != "" {
		line += " (since " + startsAt + ")"
	}
	return line
}

func constantTimeEquals(expected, actual string) bool {
	return hmac.Equal([]byte(expected), []byte(actual))
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
