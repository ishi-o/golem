// Package events contains the optional external-event implementation. Core
// remains usable without it; an application opts in by constructing an Intake
// and a Sweeper and registering the event tools.
package events

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/observing"
)

// Owner is the identity an unattended triage run assumes.
type Owner struct {
	UserID   string
	GroupID  string
	TenantID string
}

// Playbook identifies the knowledge documents used to triage one event
// source. Query is operator configuration, not event input; DocIDs optionally
// narrows the result to an explicit set of documents.
type Playbook struct {
	Query  string
	DocIDs []string
}

// HasQuery reports whether this playbook is configured for retrieval.
func (p Playbook) HasQuery() bool { return strings.TrimSpace(p.Query) != "" }

// Accepts reports whether a document is in the optional allow-list. An empty
// allow-list means all documents returned for the fixed query are accepted.
func (p Playbook) Accepts(docID string) bool {
	if len(p.DocIDs) == 0 {
		return true
	}
	docID = strings.TrimSpace(docID)
	for _, allowed := range p.DocIDs {
		if strings.TrimSpace(allowed) == docID {
			return true
		}
	}
	return false
}

func (p Playbook) normalized() Playbook {
	p.Query = strings.TrimSpace(p.Query)
	if len(p.DocIDs) == 0 {
		return p
	}
	ids := make([]string, 0, len(p.DocIDs))
	seen := make(map[string]struct{}, len(p.DocIDs))
	for _, id := range p.DocIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	p.DocIDs = ids
	return p
}

// SourceConfig overrides the global event policy for one source. Zero
// durations inherit the global value; Enabled is a pointer so false can be
// distinguished from omitted.
type SourceConfig struct {
	Enabled  *bool
	Owner    Owner
	Playbook Playbook
	// FailureRoute is used only when the optional sweeper notifier reports a
	// failed unattended triage run. An observation's route remains its own.
	FailureRoute           observing.Route
	TrustedActors          []string
	Debounce               time.Duration
	MaxDebounce            time.Duration
	Cooldown               time.Duration
	ResolveAfterQuiet      time.Duration
	ResolveAfterEvaluation bool
	TriagePrompt           string
}

// Config controls external event correlation and triage.
type Config struct {
	Enabled                  bool
	SweepInterval            time.Duration
	MaxConcurrentEvaluations int
	MaxEventsPerSituation    int
	MaxEvidence              int
	MaxBodySize              int
	Debounce                 time.Duration
	MaxDebounce              time.Duration
	Cooldown                 time.Duration
	ResolveAfterQuiet        time.Duration
	ResolveAfterEvaluation   bool
	TriagePrompt             string
	Playbook                 Playbook
	Sources                  map[string]SourceConfig
}

// Policy is a fully resolved source policy.
type Policy struct {
	Source                 string
	Owner                  Owner
	Playbook               Playbook
	FailureRoute           observing.Route
	TrustedActors          []string
	Debounce               time.Duration
	MaxDebounce            time.Duration
	Cooldown               time.Duration
	ResolveAfterQuiet      time.Duration
	ResolveAfterEvaluation bool
	TriagePrompt           string
}

// Normalize fills conservative defaults and validates values shared by the
// intake and sweeper.
func (c *Config) Normalize() error {
	if c.MaxConcurrentEvaluations < 0 || c.MaxEventsPerSituation < 0 || c.MaxEvidence < 0 || c.MaxBodySize < 0 {
		return fmt.Errorf("events: numeric limits cannot be negative")
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 5 * time.Second
	}
	if c.MaxConcurrentEvaluations <= 0 {
		c.MaxConcurrentEvaluations = 2
	}
	if c.MaxEventsPerSituation <= 0 {
		c.MaxEventsPerSituation = 200
	}
	if c.MaxEvidence <= 0 {
		c.MaxEvidence = 20
	}
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 1 << 20
	}
	if c.Debounce <= 0 {
		c.Debounce = 30 * time.Second
	}
	if c.MaxDebounce <= 0 {
		c.MaxDebounce = 5 * time.Minute
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 10 * time.Minute
	}
	if c.ResolveAfterQuiet <= 0 {
		c.ResolveAfterQuiet = 6 * time.Hour
	}
	if c.MaxDebounce < c.Debounce {
		return fmt.Errorf("events: max debounce must not be shorter than debounce")
	}
	for source, policy := range c.Sources {
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("events: source name must not be blank")
		}
		debounce := c.Debounce
		if policy.Debounce > 0 {
			debounce = policy.Debounce
		}
		maxDebounce := c.MaxDebounce
		if policy.MaxDebounce > 0 {
			maxDebounce = policy.MaxDebounce
		}
		if maxDebounce < debounce {
			return fmt.Errorf("events: source %q max debounce must not be shorter than debounce", source)
		}
		for _, pattern := range policy.TrustedActors {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("events: source %q trusted actor pattern %q: %w", source, pattern, err)
			}
		}
	}
	return nil
}

// PolicyFor resolves a configured source. A source absent from Sources is not
// admitted, which makes adding a webhook endpoint an explicit operator act.
func (c Config) PolicyFor(source string) (Policy, bool) {
	if !c.Enabled {
		return Policy{}, false
	}
	value, ok := c.Sources[source]
	if !ok {
		return Policy{}, false
	}
	if value.Enabled != nil && !*value.Enabled {
		return Policy{}, false
	}
	policy := Policy{
		Source: source, Owner: value.Owner, FailureRoute: value.FailureRoute,
		Playbook:      value.Playbook.normalized(),
		TrustedActors: append([]string(nil), value.TrustedActors...),
		Debounce:      c.Debounce, MaxDebounce: c.MaxDebounce, Cooldown: c.Cooldown,
		ResolveAfterQuiet:      c.ResolveAfterQuiet,
		ResolveAfterEvaluation: c.ResolveAfterEvaluation,
		TriagePrompt:           c.TriagePrompt,
	}
	if !policy.Playbook.HasQuery() && len(policy.Playbook.DocIDs) == 0 {
		policy.Playbook = c.Playbook.normalized()
	}
	if value.Debounce > 0 {
		policy.Debounce = value.Debounce
	}
	if value.MaxDebounce > 0 {
		policy.MaxDebounce = value.MaxDebounce
	}
	if value.Cooldown > 0 {
		policy.Cooldown = value.Cooldown
	}
	if value.ResolveAfterQuiet > 0 {
		policy.ResolveAfterQuiet = value.ResolveAfterQuiet
	}
	if value.TriagePrompt != "" {
		policy.TriagePrompt = value.TriagePrompt
	}
	if value.ResolveAfterEvaluation {
		policy.ResolveAfterEvaluation = true
	}
	return policy, true
}
