package agent_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ishi-o/golem/connector/github"
	"github.com/ishi-o/golem/connector/gitlab"
	"github.com/ishi-o/golem/connector/grafana"
	"github.com/ishi-o/golem/connector/webhook"
	"github.com/ishi-o/golem/core/observing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type webhookIntake struct {
	mu           sync.Mutex
	observations []observing.Observation
	err          error
}

func (i *webhookIntake) Observe(observation observing.Observation) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return i.err
	}
	i.observations = append(i.observations, observation)
	return nil
}

func (i *webhookIntake) values() []observing.Observation {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]observing.Observation(nil), i.observations...)
}

func TestWebhookConnectorsVerifyAndNormalizeExternalEvents(t *testing.T) {
	githubBody := `{"action":"opened","repository":{"full_name":"acme/widgets"},"issue":{"number":42,"title":"Disk is full"},"sender":{"login":"octocat"}}`
	githubIntake := &webhookIntake{}
	githubHeaders := map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   "delivery-github-1",
		"X-Hub-Signature-256": githubSignature("github-secret", []byte(githubBody)),
	}
	response := serveWebhook(github.NewHandler(githubIntake, "github-secret"), githubHeaders, githubBody)
	assert.Equal(t, http.StatusNoContent, response.Code)
	values := githubIntake.values()
	require.Len(t, values, 1)
	assert.Equal(t, "github", values[0].Source)
	assert.Equal(t, "issues.opened", values[0].Kind)
	assert.Equal(t, "github:acme/widgets#42", values[0].CorrelationKey)
	assert.Equal(t, "octocat", values[0].Actor.AuthenticatedName())

	gitlabBody := `{"object_kind":"issue","project":{"path_with_namespace":"acme/widgets"},"object_attributes":{"iid":7,"action":"open","title":"Queue is full"},"user":{"username":"tanuki"}}`
	gitlabIntake := &webhookIntake{}
	response = serveWebhook(gitlab.NewHandler(gitlabIntake, "gitlab-secret"), map[string]string{
		"X-Gitlab-Token":      "gitlab-secret",
		"X-Gitlab-Event":      "Issue Hook",
		"X-Gitlab-Event-UUID": "delivery-gitlab-1",
	}, gitlabBody)
	assert.Equal(t, http.StatusNoContent, response.Code)
	values = gitlabIntake.values()
	require.Len(t, values, 1)
	assert.Equal(t, "issue.open", values[0].Kind)
	assert.Equal(t, "gitlab:acme/widgets!7", values[0].CorrelationKey)
	assert.Equal(t, "tanuki", values[0].Actor.AuthenticatedName())

	grafanaBody := `{"status":"firing","groupKey":"disk-full","groupLabels":{"alertname":"DiskFull"},"commonLabels":{"env":"prod"},"alerts":[{"labels":{"alertname":"DiskFull","instance":"db-1","severity":"critical"},"annotations":{"summary":"Disk is 97% full"},"fingerprint":"a1"}]}`
	grafanaIntake := &webhookIntake{}
	response = serveWebhook(grafana.NewHandler(grafanaIntake, "grafana-secret"), map[string]string{
		"Authorization": "Bearer grafana-secret",
	}, grafanaBody)
	assert.Equal(t, http.StatusNoContent, response.Code)
	values = grafanaIntake.values()
	require.Len(t, values, 1)
	assert.Equal(t, "alert.firing", values[0].Kind)
	assert.Equal(t, "grafana:group:disk-full", values[0].CorrelationKey)
	assert.Contains(t, values[0].Summary, "Disk is 97% full")
}

func TestGitHubWebhookRejectsInvalidSignatureAndIgnoresPing(t *testing.T) {
	body := `{"action":"opened","repository":{"full_name":"acme/widgets"}}`
	intake := &webhookIntake{}
	handler := github.NewHandler(intake, "secret")

	response := serveWebhook(handler, map[string]string{
		"X-GitHub-Event":      "issues",
		"X-Hub-Signature-256": "sha256=" + strings.Repeat("0", sha256.Size*2),
	}, body)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Empty(t, intake.values())

	ping := `{}`
	response = serveWebhook(handler, map[string]string{
		"X-GitHub-Event":      "ping",
		"X-Hub-Signature-256": githubSignature("secret", []byte(ping)),
	}, ping)
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Empty(t, intake.values())
}

func TestWebhookHandlerReturnsRetryableFailureAndBoundsBodies(t *testing.T) {
	body := `{"repository":{"full_name":"acme/widgets"}}`
	intake := &webhookIntake{err: assert.AnError}
	handler := github.NewHandler(intake, "secret")
	response := serveWebhook(handler, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": githubSignature("secret", []byte(body)),
	}, body)
	assert.Equal(t, http.StatusInternalServerError, response.Code)

	limited := github.NewHandler(&webhookIntake{}, "secret", webhook.WithMaxBodySize(4))
	response = serveWebhook(limited, map[string]string{}, "12345")
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)

	request := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	responseRecorder := httptest.NewRecorder()
	limited.ServeHTTP(responseRecorder, request)
	assert.Equal(t, http.StatusMethodNotAllowed, responseRecorder.Code)
}

func githubSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func serveWebhook(handler http.Handler, headers map[string]string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
