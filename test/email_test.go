package agent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ishi-o/golem/connector/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailFacadeNormalizesVerifiedThreadAndBoundsBody(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 13, 14, 0, time.UTC)
	source := email.NewSource(email.WithClock(func() time.Time { return now }), email.WithMaxBodyLength(12))

	observation, err := source.Observe(email.Message{
		DeliveryID:        "uidvalidity:42",
		ThreadID:          "<root@example.test>",
		From:              "alerts@example.test",
		AuthenticatedFrom: "alerts@example.test",
		Subject:           " Disk full\n",
		Body:              "disk is very full",
	})
	require.NoError(t, err)
	assert.Equal(t, email.Name, observation.Source)
	assert.Equal(t, "uidvalidity:42", observation.DeliveryID)
	assert.Equal(t, "mail.received", observation.Kind)
	assert.Equal(t, "email:alerts@example.test:<root@example.test>", observation.CorrelationKey)
	assert.Equal(t, "Disk full", observation.Title)
	assert.Equal(t, now, observation.ObservedAt)
	assert.Equal(t, "alerts@example.test", observation.Actor.AuthenticatedName())
	assert.Contains(t, observation.Summary, "[...truncated]")

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(observation.PayloadJSON), &payload))
	assert.Equal(t, "alerts@example.test", payload["from"])
	assert.Equal(t, "Disk full", payload["subject"])
	assert.Contains(t, payload["body"], "[...truncated]")
}

func TestEmailFacadeDoesNotTrustUnverifiedThreadOrSender(t *testing.T) {
	source := email.NewSource()
	observation, err := source.Observe(email.Message{
		DeliveryID: "mailbox:9",
		ThreadID:   "<somebody-elses-thread@example.test>",
		From:       "spoofed@example.test",
		Subject:    "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "email:mailbox:9", observation.CorrelationKey)
	assert.Empty(t, observation.Actor.AuthenticatedName())
	assert.Equal(t, "spoofed@example.test (unverified)", observation.Actor.String())
}

func TestEmailFacadeRequiresReceiverDeliveryIdentity(t *testing.T) {
	_, err := email.NewSource().Observe(email.Message{From: "sender@example.test"})
	assert.ErrorContains(t, err, "delivery id is required")
}

type testEmailReceiver struct{}

func (testEmailReceiver) Receive(ctx context.Context, handler email.MessageHandler) error {
	return handler(ctx, email.Message{DeliveryID: "test:1"})
}

var _ email.Receiver = testEmailReceiver{}
