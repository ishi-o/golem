// Package email defines the transport-neutral email connector facade.
//
// It deliberately contains no IMAP, POP3, SMTP, MIME, or provider client.
// A downstream receiver turns a mail transport into Message values; Source
// then turns those values into golem observations.
package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/observing"
)

const (
	Name                  = "email"
	DefaultMaxBodyLength  = 8000
	maxCorrelationKeySize = 180
)

// Message is the transport-neutral subset of a received email needed by the
// event facade. DeliveryID must be assigned by the receiver from a stable
// mailbox identity, such as an IMAP UID plus UIDVALIDITY; Message-ID alone is
// sender-controlled and is not a safe delivery identity.
type Message struct {
	DeliveryID string
	// ThreadID is computed by the receiver from References/In-Reply-To. It is
	// used only when AuthenticatedFrom is present, so an unverified sender
	// cannot join another sender's situation by writing a guessed header.
	ThreadID string
	From     string
	// AuthenticatedFrom is set only after a trusted mail gateway or verifier
	// has vouched for the sender. A raw From header must never populate it.
	AuthenticatedFrom string
	Subject           string
	Body              string
	// ReceivedAt is the receiver's observation time, not the untrusted Date
	// header. A zero value is replaced by Source's clock.
	ReceivedAt time.Time
}

// Validate checks the identity required for at-least-once delivery handling.
func (m Message) Validate() error {
	if strings.TrimSpace(m.DeliveryID) == "" {
		return fmt.Errorf("email: delivery id is required")
	}
	return nil
}

// MessageHandler consumes one normalized mail message. A receiver decides
// whether a returned error should cause a retry or stop its receive loop.
type MessageHandler func(context.Context, Message) error

// Receiver is the transport seam for an email connector. Implementations are
// supplied by the application: for example an IMAP IDLE consumer, a POP3
// poller, or an HTTP mail-gateway receiver.
type Receiver interface {
	Receive(ctx context.Context, handler MessageHandler) error
}

// Source converts transport-neutral messages into event observations. It has
// no connection, persistence, or model lifecycle of its own.
type Source struct {
	maxBodyLength int
	clock         func() time.Time
}

// Option customizes a Source.
type Option func(*Source)

// WithMaxBodyLength bounds the body copied into the observation and payload.
func WithMaxBodyLength(limit int) Option {
	return func(s *Source) {
		if limit > 0 {
			s.maxBodyLength = limit
		}
	}
}

// WithClock supplies the fallback observation clock.
func WithClock(clock func() time.Time) Option {
	return func(s *Source) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// NewSource constructs the email observation facade.
func NewSource(options ...Option) Source {
	source := Source{maxBodyLength: DefaultMaxBodyLength, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(&source)
		}
	}
	return source
}

// Observe maps one received message to an observation. It reports both
// verified and unverified senders; the events policy decides whether an
// unverified actor is trusted to wake the agent.
func (s Source) Observe(message Message) (observing.Observation, error) {
	if err := message.Validate(); err != nil {
		return observing.Observation{}, err
	}
	claimed := cleanLine(message.From)
	authenticated := cleanLine(message.AuthenticatedFrom)
	subject := truncate(cleanLine(message.Subject), 512)
	body := truncateBody(message.Body, s.bodyLimit())
	sender := firstNonBlank(claimed, authenticated, "(unknown sender)")
	thread := strings.TrimSpace(message.ThreadID)
	correlation := correlationKey(authenticated, thread, message.DeliveryID)

	var actor *observing.Actor
	if authenticated != "" {
		actor = observing.AuthenticatedActor(authenticated)
	} else if claimed != "" {
		actor = observing.ClaimedActor(claimed)
	}

	payload, err := json.Marshal(map[string]string{
		"from":    claimed,
		"subject": subject,
		"body":    body,
	})
	if err != nil {
		return observing.Observation{}, fmt.Errorf("email: encode payload: %w", err)
	}

	now := message.ReceivedAt
	if now.IsZero() {
		now = s.now()
	}
	title := subject
	if title == "" {
		title = "Mail from " + sender
	}
	summary := "From " + sender + " — " + firstNonBlank(subject, "(no subject)")
	if body != "" {
		summary += "\n" + body
	}
	return observing.Observation{
		Source:         Name,
		DeliveryID:     strings.TrimSpace(message.DeliveryID),
		Kind:           "mail.received",
		CorrelationKey: correlation,
		Title:          title,
		Summary:        summary,
		PayloadJSON:    string(payload),
		ObservedAt:     now,
		Actor:          actor,
	}, nil
}

func (s Source) bodyLimit() int {
	if s.maxBodyLength > 0 {
		return s.maxBodyLength
	}
	return DefaultMaxBodyLength
}

func (s Source) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func correlationKey(authenticated, thread, deliveryID string) string {
	base := Name + ":" + strings.TrimSpace(deliveryID)
	if authenticated != "" {
		base = Name + ":" + authenticated + ":" + firstNonBlank(thread, deliveryID)
	}
	if len([]byte(base)) <= maxCorrelationKeySize {
		return base
	}
	digest := sha256.Sum256([]byte(base))
	return Name + ":" + hex.EncodeToString(digest[:])
}

func cleanLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func truncateBody(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	return truncate(value, limit)
}

func truncate(value string, limit int) string {
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "\n[...truncated]"
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
