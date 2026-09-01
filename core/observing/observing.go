// Package observing defines the transport-neutral event intake facade.
//
// Integrations turn webhook deliveries, chat messages, mail, and polling
// results into Observation values. They do not need to know how an optional
// event implementation deduplicates, correlates, or schedules triage.
package observing

import (
	"fmt"
	"strings"
	"time"
)

// Actor is the party an observation says caused it. Authenticated is kept
// next to the name so an allow-list can never accidentally trust a name that
// was merely copied from an unverified payload.
type Actor struct {
	Name          string
	Authenticated bool
}

// AuthenticatedActor constructs a verified actor, or nil for a blank name.
func AuthenticatedActor(name string) *Actor {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &Actor{Name: name, Authenticated: true}
}

// ClaimedActor constructs an explicitly unverified actor.
func ClaimedActor(name string) *Actor {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &Actor{Name: name}
}

// AuthenticatedName returns a name suitable for security decisions.
func (a *Actor) AuthenticatedName() string {
	if a == nil || !a.Authenticated {
		return ""
	}
	return a.Name
}

func (a *Actor) String() string {
	if a == nil {
		return ""
	}
	if a.Authenticated {
		return a.Name
	}
	return a.Name + " (unverified)"
}

// Route says where an event-related run may talk and which shared scopes it
// belongs to. An empty route is valid for machine-only observations.
type Route struct {
	ChatID   string
	ChatType string
	GroupID  string
	TenantID string
}

// NoRoute is the zero route, named for call sites that want to be explicit.
var NoRoute Route

// IsEmpty reports whether the route has no destination chat.
func (r Route) IsEmpty() bool { return strings.TrimSpace(r.ChatID) == "" }

// Observation is one external event in a transport-neutral form.
type Observation struct {
	Source         string
	DeliveryID     string
	Kind           string
	CorrelationKey string
	Title          string
	Summary        string
	PayloadJSON    string
	ObservedAt     time.Time
	Route          Route
	Actor          *Actor
}

// Validate checks the identity fields required for durable deduplication and
// deterministic correlation.
func (o Observation) Validate() error {
	if strings.TrimSpace(o.Source) == "" {
		return fmt.Errorf("observation source is required")
	}
	if strings.TrimSpace(o.DeliveryID) == "" {
		return fmt.Errorf("observation delivery id is required")
	}
	if strings.TrimSpace(o.CorrelationKey) == "" {
		return fmt.Errorf("observation correlation key is required")
	}
	return nil
}

// Normalize fills transport-neutral defaults and trims policy keys. It does
// not invent a delivery or correlation id.
func (o Observation) Normalize(now time.Time) (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	o.Source = strings.TrimSpace(o.Source)
	o.DeliveryID = strings.TrimSpace(o.DeliveryID)
	o.Kind = strings.TrimSpace(o.Kind)
	o.CorrelationKey = strings.TrimSpace(o.CorrelationKey)
	o.Route.ChatID = strings.TrimSpace(o.Route.ChatID)
	o.Route.ChatType = strings.TrimSpace(o.Route.ChatType)
	o.Route.GroupID = strings.TrimSpace(o.Route.GroupID)
	o.Route.TenantID = strings.TrimSpace(o.Route.TenantID)
	if o.ObservedAt.IsZero() {
		if now.IsZero() {
			now = time.Now()
		}
		o.ObservedAt = now
	}
	if o.Actor != nil {
		actor := *o.Actor
		actor.Name = strings.TrimSpace(actor.Name)
		if actor.Name == "" {
			o.Actor = nil
		} else {
			o.Actor = &actor
		}
	}
	return o, nil
}

// EventIntake receives normalized or raw observations. Implementations should
// validate at their boundary and return errors rather than silently dropping
// malformed deliveries.
type EventIntake interface {
	Observe(Observation) error
}

// EventIntakes fans one observation out to multiple independent consumers.
// Each intake gets the same value, and an error from one does not prevent the
// others from observing it. The returned error is an aggregate with enough
// context to identify every failed consumer.
type EventIntakes []EventIntake

// Observe fans out to all configured intakes.
func (all EventIntakes) Observe(observation Observation) error {
	var failures []string
	for index, intake := range all {
		if intake == nil {
			continue
		}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					failures = append(failures, fmt.Sprintf("intake[%d] panicked: %v", index, recovered))
				}
			}()
			if err := intake.Observe(observation); err != nil {
				failures = append(failures, fmt.Sprintf("intake[%d]: %v", index, err))
			}
		}()
	}
	if len(failures) > 0 {
		return fmt.Errorf("observing: %s", strings.Join(failures, "; "))
	}
	return nil
}
