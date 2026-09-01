package agent_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/observing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observingFunc func(observing.Observation) error

func (f observingFunc) Observe(value observing.Observation) error { return f(value) }

func TestObservationNormalizesWithoutInventingIdentity(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	value, err := (observing.Observation{
		Source:         " monitor ",
		DeliveryID:     " delivery-1 ",
		Kind:           " alert ",
		CorrelationKey: " incident-1 ",
		Route: observing.Route{
			ChatID: " chat ", ChatType: " p2p ", GroupID: " group ", TenantID: " tenant ",
		},
		Actor: observing.AuthenticatedActor(" operator "),
	}).Normalize(now)
	require.NoError(t, err)
	assert.Equal(t, "monitor", value.Source)
	assert.Equal(t, "delivery-1", value.DeliveryID)
	assert.Equal(t, "incident-1", value.CorrelationKey)
	assert.Equal(t, now, value.ObservedAt)
	assert.Equal(t, "operator", value.Actor.AuthenticatedName())
	assert.Equal(t, "chat", value.Route.ChatID)
	assert.False(t, value.Route.IsEmpty())

	claimed := observing.ClaimedActor("payload-claimed")
	assert.Empty(t, claimed.AuthenticatedName())
	assert.Contains(t, claimed.String(), "unverified")

	_, err = (observing.Observation{Source: "monitor", DeliveryID: "delivery"}).Normalize(now)
	assert.ErrorContains(t, err, "correlation key")
}

func TestEventIntakesFanOutAndContainFailures(t *testing.T) {
	var called []string
	all := observing.EventIntakes{
		observingFunc(func(value observing.Observation) error {
			called = append(called, value.Source)
			return errors.New("downstream unavailable")
		}),
		observingFunc(func(observing.Observation) error { panic("bad consumer") }),
		observingFunc(func(observing.Observation) error {
			called = append(called, "third")
			return nil
		}),
	}
	err := all.Observe(observing.Observation{Source: "monitor", DeliveryID: "delivery", CorrelationKey: "incident"})
	require.Error(t, err)
	assert.Equal(t, []string{"monitor", "third"}, called)
	assert.True(t, strings.Contains(err.Error(), "downstream unavailable"))
	assert.True(t, strings.Contains(err.Error(), "panicked"))
}
