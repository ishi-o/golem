package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/events"
	"github.com/ishi-o/golem/core/knowledge"
	"github.com/ishi-o/golem/core/observing"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
	coretools "github.com/ishi-o/golem/core/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventIntakeDeduplicatesCorrelatesAndBoundsEvidence(t *testing.T) {
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })

	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	trusted := "ops@example.com"
	cfg := events.Config{
		Enabled:               true,
		Debounce:              time.Minute,
		MaxDebounce:           5 * time.Minute,
		MaxEventsPerSituation: 2,
		MaxEvidence:           1,
		Sources: map[string]events.SourceConfig{
			"monitor": {Owner: events.Owner{UserID: "owner-1"}, TrustedActors: []string{trusted}},
			"other":   {Owner: events.Owner{UserID: "owner-1"}},
		},
	}
	intake, err := events.NewIntake(cfg, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(), events.WithClock(func() time.Time { return now }))
	require.NoError(t, err)

	observation := observing.Observation{
		Source: "monitor", DeliveryID: "delivery-1", Kind: "alert", CorrelationKey: "incident-1",
		Title: "Latency", Summary: "first alert", PayloadJSON: `{"latency":900}`,
		Actor: observing.AuthenticatedActor(trusted),
	}
	require.NoError(t, intake.Observe(observation))
	// The same delivery is a transport retry, not a second event.
	require.NoError(t, intake.Observe(observation))

	second := observation
	second.DeliveryID = "delivery-2"
	second.Summary = "second alert"
	require.NoError(t, intake.Observe(second))

	values, err := fixture.Backend().Situations().ListBySourceAndCorrelationAndStatus(context.Background(), "monitor", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Equal(t, 2, values[0].EventCount)
	evidence, err := fixture.Backend().ObservedEvents().ListBySituation(context.Background(), values[0].ID)
	require.NoError(t, err)
	assert.Len(t, evidence, 1, "MaxEvidence bounds durable prompt evidence")

	// A claimed actor is never accepted by a trusted-actor allow-list.
	untrusted := observation
	untrusted.DeliveryID = "delivery-untrusted"
	untrusted.Actor = observing.ClaimedActor(trusted)
	require.NoError(t, intake.Observe(untrusted))
	values, err = fixture.Backend().Situations().ListBySourceAndCorrelationAndStatus(context.Background(), "monitor", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	assert.Equal(t, 2, values[0].EventCount)

	// Correlation keys are scoped by source; another integration cannot join
	// this incident accidentally.
	other := observation
	other.Source, other.DeliveryID = "other", "other-delivery-1"
	require.NoError(t, intake.Observe(other))
	monitor, err := fixture.Backend().Situations().ListBySourceAndCorrelationAndStatus(context.Background(), "monitor", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	otherValues, err := fixture.Backend().Situations().ListBySourceAndCorrelationAndStatus(context.Background(), "other", "incident-1", store.SituationStatusOpen)
	require.NoError(t, err)
	assert.Len(t, monitor, 1)
	assert.Len(t, otherValues, 1)
	assert.NotEqual(t, monitor[0].ID, otherValues[0].ID)
}

func TestEventSweeperRunsMemorylessTriageAndCanResolveAfterEvaluation(t *testing.T) {
	now := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	a, fixture := newTestAgent(t, &fakeModel{turns: [][]*schema.Message{textChunks("triaged")}})

	cfg := events.Config{
		Enabled:                true,
		Debounce:               time.Second,
		MaxDebounce:            5 * time.Second,
		ResolveAfterEvaluation: true,
		Sources: map[string]events.SourceConfig{
			"monitor": {Owner: events.Owner{UserID: "owner-1"}},
		},
	}
	clock := func() time.Time { return now }
	intake, err := events.NewIntake(cfg, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(), events.WithClock(clock))
	require.NoError(t, err)
	require.NoError(t, intake.Observe(observing.Observation{Source: "monitor", DeliveryID: "delivery-1", CorrelationKey: "incident-1", Summary: "alert"}))

	now = now.Add(2 * time.Second)
	sweeper, err := events.NewSweeper(cfg, a, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(), events.WithSweeperClock(clock))
	require.NoError(t, err)
	require.NoError(t, sweeper.Sweep(context.Background()))
	assert.Eventually(t, func() bool { return sweeper.InFlight() == 0 }, 5*time.Second, 10*time.Millisecond)

	values, err := fixture.Backend().Situations().ListByStatus(context.Background(), store.SituationStatusResolved)
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Equal(t, store.SituationPhaseMonitoring, values[0].Phase)
}

func TestEventToolsKeepSituationOwnershipAtTheFacade(t *testing.T) {
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	ctx := coretools.UserID.With(coretools.SituationID.With(context.Background(), "situation-1"), "owner-1")
	require.NoError(t, fixture.Backend().Situations().Save(ctx, store.Situation{ID: "situation-1", OwnerUserID: "owner-1", Status: store.SituationStatusOpen, Phase: store.SituationPhaseMonitoring}))
	require.NoError(t, fixture.Backend().ObservedEvents().Save(ctx, store.ObservedEvent{ID: "event-1", SituationID: "situation-1", Summary: "evidence"}))

	family := events.NewTools(fixture.Backend().Situations(), fixture.Backend().ObservedEvents())
	get := findTool(t, family.List(), events.ToolNameGetSituationEvents)
	result := invokeTool(t, get, ctx, `{}`)
	assert.Contains(t, result, "evidence")

	other := coretools.UserID.With(coretools.SituationID.With(context.Background(), "situation-1"), "not-owner")
	resolve := findTool(t, family.List(), events.ToolNameResolveSituation)
	_, err := resolve.InvokableRun(other, `{}`)
	assert.Error(t, err)
}

func TestEventPlaybooksAreAdminOnlyAndOwnerScoped(t *testing.T) {
	base := knowledge.NewInMemory(knowledge.InMemoryOptions{})
	cfg := events.Config{
		Enabled: true,
		Sources: map[string]events.SourceConfig{
			"monitor": {
				Owner: events.Owner{UserID: "triage-owner"},
				Playbook: events.Playbook{
					Query:  "database incident",
					DocIDs: []string{"runbook"},
				},
			},
		},
	}
	family := events.NewPlaybookTools(base, cfg, func(ctx context.Context) bool {
		user, _ := coretools.UserID.Get(ctx)
		return user == "admin"
	})
	list := findTool(t, family.List(), events.ToolNameListPlaybooks)
	write := findTool(t, family.List(), events.ToolNameWritePlaybook)

	denied := coretools.UserID.With(context.Background(), "operator")
	_, err := list.InvokableRun(denied, `{}`)
	assert.Error(t, err)
	_, err = write.InvokableRun(denied, `{"source":"monitor","docId":"runbook","title":"Runbook","text":"Restart the database safely."}`)
	assert.Error(t, err)

	admin := coretools.ReplyMessageID.With(coretools.UserID.With(context.Background(), "admin"), "admin-reply")
	result := invokeTool(t, write, admin, `{"source":"monitor","docId":"runbook","title":"Runbook","text":"Restart the database safely."}`)
	assert.Contains(t, result, "monitor")
	result = invokeTool(t, list, admin, `{}`)
	assert.Contains(t, result, `"query":"database incident"`)
	assert.Contains(t, result, `"docIds":["runbook"]`)

	page, err := base.List(context.Background(), knowledge.NewScope("triage-owner", "", ""), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"runbook"}, entryIDs(page.Entries))
	_, err = write.InvokableRun(admin, `{"source":"monitor","docId":"not-allowed","title":"Bad","text":"x"}`)
	assert.Error(t, err)
}

func TestEventSweeperUsesTheConfiguredOwnerPlaybook(t *testing.T) {
	dir := t.TempDir()
	model := &fakeModel{turns: [][]*schema.Message{textChunks("triaged")}}
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })

	agentConfig := config.Config{}
	require.NoError(t, agentConfig.Normalize())
	agentConfig.Storage.Location = dir
	backend := fixture.Backend()
	provider := coretools.NewProvider(agentConfig, storage.NewWorkspaceFactory(dir), backend, nil)
	base := knowledge.NewInMemory(knowledge.InMemoryOptions{})
	_, err := base.Index(context.Background(), knowledge.NewTextSource(
		knowledge.NewScope("triage-owner", "", ""), knowledge.TargetOwn,
		"Database runbook", "database incident restart procedure", "", "runbook",
	))
	require.NoError(t, err)
	_, err = base.Index(context.Background(), knowledge.NewTextSource(
		knowledge.NewScope("triage-owner", "", ""), knowledge.TargetOwn,
		"Unrelated", "database incident unrelated notes", "", "unrelated",
	))
	require.NoError(t, err)
	a := agent.New(model, fixture.Memory(), provider, agentConfig,
		agent.WithBackend(backend),
		agent.WithKnowledgeBase(base, knowledge.RetrievalConfig{TopK: 1, MaxChars: 1000}),
	)

	now := time.Date(2026, 4, 6, 7, 8, 9, 0, time.UTC)
	clock := func() time.Time { return now }
	eventConfig := events.Config{
		Enabled:     true,
		Debounce:    time.Second,
		MaxDebounce: 5 * time.Second,
		Sources: map[string]events.SourceConfig{
			"monitor": {
				Owner:    events.Owner{UserID: "triage-owner"},
				Playbook: events.Playbook{Query: "database incident", DocIDs: []string{"runbook"}},
			},
		},
	}
	intake, err := events.NewIntake(eventConfig, backend.Situations(), backend.ObservedEvents(), backend.ProcessedMessages(), events.WithClock(clock))
	require.NoError(t, err)
	require.NoError(t, intake.Observe(observing.Observation{Source: "monitor", DeliveryID: "delivery-playbook", CorrelationKey: "incident-playbook", Summary: "alert"}))
	now = now.Add(2 * time.Second)
	sweeper, err := events.NewSweeper(eventConfig, a, backend.Situations(), backend.ObservedEvents(), backend.ProcessedMessages(), events.WithSweeperClock(clock))
	require.NoError(t, err)
	require.NoError(t, sweeper.Sweep(context.Background()))
	assert.Eventually(t, func() bool { return sweeper.InFlight() == 0 }, 5*time.Second, 10*time.Millisecond)

	model.mu.Lock()
	var knowledgeContext string
	for _, message := range model.seen {
		if message.Role == schema.System && strings.Contains(message.Content, "<knowledge>") {
			knowledgeContext += message.Content
		}
	}
	model.mu.Unlock()
	assert.Contains(t, knowledgeContext, "Database runbook")
	assert.NotContains(t, knowledgeContext, "Unrelated")
}

func TestEventSweeperSeparatesFailureRouteAndRejectsAdminOwners(t *testing.T) {
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	cfg := events.Config{
		Enabled: true,
		Sources: map[string]events.SourceConfig{
			"monitor": {
				Owner:        events.Owner{UserID: "triage-owner"},
				FailureRoute: observing.Route{ChatID: "failure-chat", ChatType: "p2p"},
			},
		},
	}
	_, err := events.NewSweeper(cfg, nil, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(), events.WithSweeperAdminCheck(func(owner string) bool {
		return owner == "triage-owner"
	}))
	assert.Error(t, err)

	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	clock := func() time.Time { return now }
	intake, err := events.NewIntake(cfg, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(), events.WithClock(clock))
	require.NoError(t, err)
	require.NoError(t, intake.Observe(observing.Observation{
		Source: "monitor", DeliveryID: "failure-delivery", CorrelationKey: "failure-incident",
		Route: observing.Route{ChatID: "origin-chat", ChatType: "group"}, Summary: "alert",
	}))
	open, err := fixture.Backend().Situations().ListByStatus(context.Background(), store.SituationStatusOpen)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "origin-chat", open[0].ChatID, "failure routing must not become the event's reply route")

	var notifiedRoute observing.Route
	var notifiedMessage string
	sweeper, err := events.NewSweeper(cfg, nil, fixture.Backend().Situations(), fixture.Backend().ObservedEvents(), fixture.Backend().ProcessedMessages(),
		events.WithSweeperClock(clock),
		events.WithSweeperFailureNotifier(func(_ context.Context, route observing.Route, message string) error {
			notifiedRoute, notifiedMessage = route, message
			return nil
		}),
	)
	require.NoError(t, err)
	now = now.Add(time.Minute)
	require.NoError(t, sweeper.Sweep(context.Background()))
	assert.Equal(t, "failure-chat", notifiedRoute.ChatID)
	assert.Contains(t, notifiedMessage, "failure-incident")
}
