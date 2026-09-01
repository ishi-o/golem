package storecontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testObservedEventsAndSituations(t *testing.T, f Fixture) {
	ctx := context.Background()
	situationRepo := f.Backend().Situations()
	eventRepo := f.Backend().ObservedEvents()

	created := time.Now().UTC().Truncate(time.Microsecond)
	situation := store.Situation{
		ID: "situation-1", Source: "monitor", CorrelationKey: "incident-1", Title: "Database latency",
		Status: store.SituationStatusOpen, Phase: store.SituationPhaseAwaitingEvaluation,
		EvaluateAfter: created.Add(time.Minute), FirstSeenAt: created, AwaitingSince: created,
		LastEventAt: created, Generation: 2, EventCount: 1, Decision: store.SituationDecisionNoAction,
		Severity: "high", Confidence: 0.75, Assessment: "waiting", OwnerUserID: f.Owner(),
		ChatID: "chat-1", ChatType: "p2p", GroupID: "group-1", TenantID: "tenant-1",
	}
	require.NoError(t, situationRepo.Save(ctx, situation))
	require.NoError(t, eventRepo.Save(ctx, store.ObservedEvent{
		ID: "delivery-1", SituationID: situation.ID, Source: situation.Source, Kind: "alert",
		Summary: "latency crossed the threshold", PayloadJSON: `{"latency_ms":900}`, ObservedAt: created,
	}))

	found, err := situationRepo.Get(ctx, situation.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, situation.CorrelationKey, found.CorrelationKey)
	assert.Equal(t, situation.Phase, found.Phase)
	assert.Equal(t, situation.Generation, found.Generation)
	assert.Equal(t, situation.OwnerUserID, found.OwnerUserID)

	evidence, err := eventRepo.ListBySituation(ctx, situation.ID)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	assert.Equal(t, `{"latency_ms":900}`, evidence[0].PayloadJSON)

	bySource, err := situationRepo.ListBySourceAndCorrelationAndStatus(ctx, "monitor", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	require.Len(t, bySource, 1)
	byOtherSource, err := situationRepo.ListBySourceAndCorrelationAndStatus(ctx, "other-monitor", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	assert.Empty(t, byOtherSource)

	situation.Status = store.SituationStatusResolved
	situation.Phase = store.SituationPhaseMonitoring
	situation.ResolvedAt = created.Add(time.Hour)
	require.NoError(t, situationRepo.Save(ctx, situation))
	resolved, err := situationRepo.ListByStatus(ctx, store.SituationStatusResolved)
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	assert.Equal(t, store.SituationPhaseMonitoring, resolved[0].Phase)
}
