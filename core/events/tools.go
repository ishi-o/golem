package events

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/store"
	coretools "github.com/ishi-o/golem/core/tools"
)

const (
	ToolNameListOpenSituations        = "ListOpenSituations"
	ToolNameGetSituationEvents        = "GetSituationEvents"
	ToolNameRecordSituationAssessment = "RecordSituationAssessment"
	ToolNameResolveSituation          = "ResolveSituation"
)

// Tools are the small management surface offered to event triage runs and,
// when explicitly registered, to ordinary runs.
type Tools struct {
	situations store.SituationStore
	events     store.ObservedEventStore
	clock      func() time.Time
}

// ToolsOption customizes event management tools.
type ToolsOption func(*Tools)

// WithEventToolsClock supplies a deterministic clock for resolution tools.
func WithEventToolsClock(clock func() time.Time) ToolsOption {
	return func(t *Tools) {
		if clock != nil {
			t.clock = clock
		}
	}
}

// NewTools constructs situation tools over the same stores used by Intake and
// Sweeper.
func NewTools(situations store.SituationStore, events store.ObservedEventStore, options ...ToolsOption) *Tools {
	t := &Tools{situations: situations, events: events, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(t)
		}
	}
	return t
}

// List implements tools.Builtin.
func (t *Tools) List() []tool.InvokableTool {
	if t == nil || t.situations == nil || t.events == nil {
		return nil
	}
	return []tool.InvokableTool{t.listOpen(), t.getEvents(), t.recordAssessment(), t.resolve()}
}

func (t *Tools) listOpen() tool.InvokableTool {
	return coretools.MustTool(utils.InferTool(ToolNameListOpenSituations,
		"List open external-event situations owned by the current identity.",
		func(ctx context.Context, _ struct{}) ([]store.Situation, error) {
			owner, err := coretools.UserID.Require(ctx)
			if err != nil {
				return nil, err
			}
			values, err := t.situations.ListByStatus(ctx, store.SituationStatusOpen)
			if err != nil {
				return nil, err
			}
			out := values[:0]
			for _, value := range values {
				if value.OwnerUserID == owner {
					out = append(out, value)
				}
			}
			return out, nil
		}))
}

func (t *Tools) getEvents() tool.InvokableTool {
	return coretools.MustTool(utils.InferTool(ToolNameGetSituationEvents,
		"Get the most recent stored evidence for the event situation currently being triaged; limit is capped at 50.",
		func(ctx context.Context, in struct {
			SituationID string `json:"situationId,omitempty"`
			Limit       int    `json:"limit,omitempty"`
		}) ([]store.ObservedEvent, error) {
			id, _ := coretools.SituationID.Get(ctx)
			if strings.TrimSpace(in.SituationID) != "" {
				id = in.SituationID
			}
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("situation id is required")
			}
			if err := requireSituationOwner(ctx, t.situations, id); err != nil {
				return nil, err
			}
			values, err := t.events.ListBySituation(ctx, id)
			if err != nil {
				return nil, err
			}
			sort.SliceStable(values, func(i, j int) bool { return values[i].ObservedAt.Before(values[j].ObservedAt) })
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			if limit > 50 {
				limit = 50
			}
			if len(values) > limit {
				values = values[len(values)-limit:]
			}
			return values, nil
		}))
}

func (t *Tools) recordAssessment() tool.InvokableTool {
	type input struct {
		Assessment string  `json:"assessment"`
		Decision   string  `json:"decision,omitempty"`
		Severity   string  `json:"severity,omitempty"`
		Confidence float64 `json:"confidence,omitempty"`
	}
	return coretools.MustTool(utils.InferTool(ToolNameRecordSituationAssessment,
		"Record the current assessment of the event situation so the next evaluation has durable context.",
		func(ctx context.Context, in input) (string, error) {
			id, err := coretools.SituationID.Require(ctx)
			if err != nil {
				return "", err
			}
			if err := requireSituationOwner(ctx, t.situations, id); err != nil {
				return "", err
			}
			if in.Confidence < 0 || in.Confidence > 1 {
				return "", fmt.Errorf("confidence must be between 0 and 1")
			}
			value, err := t.situations.Get(ctx, id)
			if err != nil || value == nil {
				if err != nil {
					return "", err
				}
				return "", fmt.Errorf("situation %q was not found", id)
			}
			value.Assessment = in.Assessment
			value.Severity = in.Severity
			value.Confidence = in.Confidence
			if strings.TrimSpace(in.Decision) != "" {
				decision := store.SituationDecision(strings.ToUpper(strings.TrimSpace(in.Decision)))
				switch decision {
				case store.SituationDecisionNoAction, store.SituationDecisionActed, store.SituationDecisionEscalated:
					value.Decision = decision
				default:
					return "", fmt.Errorf("unknown situation decision %q", in.Decision)
				}
			}
			if err := t.situations.Save(ctx, *value); err != nil {
				return "", err
			}
			return "recorded assessment for situation " + id, nil
		}))
}

func (t *Tools) resolve() tool.InvokableTool {
	return coretools.MustTool(utils.InferTool(ToolNameResolveSituation,
		"Mark the current event situation resolved when the evidence no longer needs attention; optionally record why.",
		func(ctx context.Context, in struct {
			Reason string `json:"reason,omitempty"`
		}) (string, error) {
			id, err := coretools.SituationID.Require(ctx)
			if err != nil {
				return "", err
			}
			if err := requireSituationOwner(ctx, t.situations, id); err != nil {
				return "", err
			}
			value, err := t.situations.Get(ctx, id)
			if err != nil || value == nil {
				if err != nil {
					return "", err
				}
				return "", fmt.Errorf("situation %q was not found", id)
			}
			value.Status = store.SituationStatusResolved
			value.Phase = store.SituationPhaseMonitoring
			value.ResolvedAt = t.clock()
			if reason := strings.TrimSpace(in.Reason); reason != "" {
				if strings.TrimSpace(value.Assessment) == "" {
					value.Assessment = "Closed: " + reason
				} else {
					value.Assessment += "\nClosed: " + reason
				}
			}
			if err := t.situations.Save(ctx, *value); err != nil {
				return "", err
			}
			return "resolved situation " + id, nil
		}))
}

func requireSituationOwner(ctx context.Context, situations store.SituationStore, id string) error {
	owner, err := coretools.UserID.Require(ctx)
	if err != nil {
		return err
	}
	value, err := situations.Get(ctx, id)
	if err != nil {
		return err
	}
	if value == nil || value.OwnerUserID != owner {
		return fmt.Errorf("situation %q was not found", id)
	}
	return nil
}
