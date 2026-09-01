package store

import "time"

// ObservedEvent is durable evidence attached to a situation. ID is the
// transport delivery id, so recording a retry is an idempotent replacement.
type ObservedEvent struct {
	ID          string    `json:"id"`
	SituationID string    `json:"situationId"`
	Source      string    `json:"source"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	PayloadJSON string    `json:"payloadJson,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
}

// SituationStatus is the coarse lifecycle of a correlated group of events.
type SituationStatus string

const (
	SituationStatusOpen     SituationStatus = "OPEN"
	SituationStatusResolved SituationStatus = "RESOLVED"
)

// SituationPhase says what the event sweeper is doing within an open
// situation. Separate status and phase values keep equality-only stores such
// as Redis useful.
type SituationPhase string

const (
	SituationPhaseAwaitingEvaluation SituationPhase = "AWAITING_EVALUATION"
	SituationPhaseInvestigating      SituationPhase = "INVESTIGATING"
	SituationPhaseMonitoring         SituationPhase = "MONITORING"
)

// SituationDecision is the last triage conclusion.
type SituationDecision string

const (
	SituationDecisionNoAction  SituationDecision = "NO_ACTION"
	SituationDecisionActed     SituationDecision = "ACTED"
	SituationDecisionEscalated SituationDecision = "ESCALATED"
)

// Situation is a deterministic correlation record. The event layer groups by
// source and correlation key; model output is recorded as assessment, never
// used to decide ownership or routing.
type Situation struct {
	ID              string            `json:"id"`
	Source          string            `json:"source"`
	CorrelationKey  string            `json:"correlationKey"`
	Title           string            `json:"title"`
	Status          SituationStatus   `json:"status"`
	Phase           SituationPhase    `json:"phase"`
	EvaluateAfter   time.Time         `json:"evaluateAfter"`
	FirstSeenAt     time.Time         `json:"firstSeenAt"`
	AwaitingSince   time.Time         `json:"awaitingSince"`
	LastEventAt     time.Time         `json:"lastEventAt"`
	LastEvaluatedAt time.Time         `json:"lastEvaluatedAt"`
	ResolvedAt      time.Time         `json:"resolvedAt,omitempty"`
	Generation      int               `json:"generation"`
	EventCount      int               `json:"eventCount"`
	Decision        SituationDecision `json:"decision,omitempty"`
	Severity        string            `json:"severity,omitempty"`
	Confidence      float64           `json:"confidence,omitempty"`
	Assessment      string            `json:"assessment,omitempty"`
	LastError       string            `json:"lastError,omitempty"`

	OwnerUserID string `json:"ownerUserId"`
	ChatID      string `json:"chatId,omitempty"`
	ChatType    string `json:"chatType,omitempty"`
	GroupID     string `json:"groupId,omitempty"`
	TenantID    string `json:"tenantId,omitempty"`
}
