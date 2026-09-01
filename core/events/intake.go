package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ishi-o/golem/core/observing"
	"github.com/ishi-o/golem/core/store"
)

// Intake is the cheap, durable half of external-event handling. It admits an
// observation, claims its delivery id, correlates it, stores bounded evidence,
// and computes the next evaluation deadline. It never calls a model.
type Intake struct {
	cfg         Config
	situations  store.SituationStore
	events      store.ObservedEventStore
	processed   store.ProcessedMessageStore
	clock       func() time.Time
	log         *slog.Logger
	locks       [64]sync.Mutex
	localMu     sync.Mutex
	localClaims map[string]struct{}
	patterns    map[string][]*regexp.Regexp
}

// IntakeOption customizes an Intake for tests or an embedding application.
type IntakeOption func(*Intake)

// WithClock supplies a deterministic clock.
func WithClock(clock func() time.Time) IntakeOption {
	return func(i *Intake) {
		if clock != nil {
			i.clock = clock
		}
	}
}

// WithLogger supplies the event logger.
func WithLogger(log *slog.Logger) IntakeOption {
	return func(i *Intake) {
		if log != nil {
			i.log = log
		}
	}
}

// NewIntake constructs the event admission/correlation facade.
func NewIntake(cfg Config, situations store.SituationStore, events store.ObservedEventStore, processed store.ProcessedMessageStore, options ...IntakeOption) (*Intake, error) {
	if situations == nil || events == nil {
		return nil, fmt.Errorf("events: situations and observed-events stores are required")
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	intake := &Intake{cfg: cfg, situations: situations, events: events, processed: processed, clock: time.Now, log: slog.Default(), localClaims: map[string]struct{}{}, patterns: map[string][]*regexp.Regexp{}}
	for _, option := range options {
		if option != nil {
			option(intake)
		}
	}
	for source, sourceConfig := range cfg.Sources {
		if len(sourceConfig.TrustedActors) == 0 {
			continue
		}
		compiled := make([]*regexp.Regexp, 0, len(sourceConfig.TrustedActors))
		for _, pattern := range sourceConfig.TrustedActors {
			compiled = append(compiled, regexp.MustCompile("(?i)^(?:"+pattern+")$"))
		}
		intake.patterns[source] = compiled
	}
	return intake, nil
}

// Observe implements observing.EventIntake.
func (i *Intake) Observe(raw observing.Observation) error {
	if i == nil {
		return fmt.Errorf("events: nil intake")
	}
	now := i.clock()
	observation, err := raw.Normalize(now)
	if err != nil {
		return err
	}
	policy, admitted := i.cfg.PolicyFor(observation.Source)
	if !admitted {
		return nil
	}
	if len(observation.PayloadJSON) > i.cfg.MaxBodySize {
		return fmt.Errorf("events: payload for %q exceeds configured body limit", observation.DeliveryID)
	}
	if !i.trusts(observation.Source, observation.Actor) {
		return nil
	}
	claimKey := "situations:observed:" + observation.Source + ":" + observation.DeliveryID
	claimed, err := i.claim(context.Background(), claimKey)
	if err != nil || !claimed {
		return err
	}
	lock := &i.locks[lockIndex(observation.CorrelationKey)]
	lock.Lock()
	defer lock.Unlock()
	if err := i.record(context.Background(), observation, policy, now); err != nil {
		_ = i.release(context.Background(), claimKey)
		return err
	}
	return nil
}

func (i *Intake) trusts(source string, actor *observing.Actor) bool {
	patterns := i.patterns[source]
	if len(patterns) == 0 {
		return true
	}
	if actor == nil || actor.AuthenticatedName() == "" || len(actor.AuthenticatedName()) > 320 {
		return false
	}
	for _, pattern := range patterns {
		if pattern.MatchString(actor.AuthenticatedName()) {
			return true
		}
	}
	return false
}

func (i *Intake) record(ctx context.Context, observation observing.Observation, policy Policy, now time.Time) error {
	open, err := i.situations.ListBySourceAndCorrelationAndStatus(ctx, observation.Source, observation.CorrelationKey, store.SituationStatusOpen)
	if err != nil {
		return err
	}
	var situation store.Situation
	if len(open) > 0 {
		sort.Slice(open, func(a, b int) bool { return open[a].LastEventAt.After(open[b].LastEventAt) })
		situation = open[0]
	} else {
		route := observation.Route
		situation = store.Situation{ID: newID(), Source: observation.Source, CorrelationKey: observation.CorrelationKey, Title: truncate(firstNonBlank(observation.Title, observation.CorrelationKey), 512), Status: store.SituationStatusOpen, Phase: store.SituationPhaseAwaitingEvaluation, FirstSeenAt: now, AwaitingSince: now, OwnerUserID: policy.Owner.UserID, ChatID: route.ChatID, ChatType: route.ChatType, GroupID: route.GroupID, TenantID: route.TenantID}
	}
	situation.EventCount++
	if situation.AwaitingSince.IsZero() {
		situation.AwaitingSince = now
	}
	if situation.EventCount <= i.cfg.MaxEventsPerSituation && i.cfg.MaxEvidence > 0 {
		evidence, err := i.events.ListBySituation(ctx, situation.ID)
		if err != nil {
			return err
		}
		if len(evidence) < i.cfg.MaxEvidence {
			if err := i.events.Save(ctx, store.ObservedEvent{ID: observation.DeliveryID, SituationID: situation.ID, Source: observation.Source, Kind: observation.Kind, Summary: truncate(observation.Summary, 1024), PayloadJSON: truncate(observation.PayloadJSON, 131072), ObservedAt: observation.ObservedAt}); err != nil {
				return err
			}
		}
	}
	situation.LastEventAt = now
	if situation.Phase != store.SituationPhaseInvestigating {
		situation.Phase = store.SituationPhaseAwaitingEvaluation
	}
	due := now.Add(policy.Debounce)
	ceiling := situation.AwaitingSince.Add(policy.MaxDebounce)
	if due.After(ceiling) {
		due = ceiling
	}
	if !situation.LastEvaluatedAt.IsZero() {
		floor := situation.LastEvaluatedAt.Add(policy.Cooldown)
		if due.Before(floor) {
			due = floor
		}
	}
	situation.EvaluateAfter = due
	return i.situations.Save(ctx, situation)
}

func (i *Intake) claim(ctx context.Context, id string) (bool, error) {
	if i.processed != nil {
		return i.processed.Claim(ctx, id)
	}
	i.localMu.Lock()
	defer i.localMu.Unlock()
	if _, exists := i.localClaims[id]; exists {
		return false, nil
	}
	i.localClaims[id] = struct{}{}
	return true, nil
}

func (i *Intake) release(ctx context.Context, id string) error {
	if i.processed != nil {
		return i.processed.Release(ctx, id)
	}
	i.localMu.Lock()
	delete(i.localClaims, id)
	i.localMu.Unlock()
	return nil
}

func lockIndex(value string) uint32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(value))
	return hasher.Sum32() % 64
}

func newID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("situation-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "event"
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
