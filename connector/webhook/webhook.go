// Package webhook contains the transport adapter shared by signed webhook
// connectors. It owns HTTP mechanics only; a vendor connector owns headers,
// authentication, and payload interpretation.
package webhook

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ishi-o/golem/core/observing"
)

// DefaultMaxBodySize bounds a webhook body before a connector parses it.
const DefaultMaxBodySize = 1 << 20

// ErrBodyTooLarge means the request body exceeded the handler limit.
var ErrBodyTooLarge = errors.New("webhook body exceeds configured limit")

// Source authenticates and translates one vendor webhook delivery. Observe is
// called only after Verify succeeds when the source is used through Handler.
// A source can return handled=false for harmless deliveries such as pings or
// test callbacks.
type Source interface {
	Name() string
	Verify(headers http.Header, body []byte, secret string) bool
	Observe(headers http.Header, body []byte) (observation observing.Observation, handled bool, err error)
}

// Handler is an HTTP facade around a webhook Source and an event intake. It
// acknowledges accepted and intentionally ignored deliveries with 204. Intake
// errors are returned as 500 so the vendor may retry them.
type Handler struct {
	source      Source
	intake      observing.EventIntake
	secret      string
	maxBodySize int
}

// HandlerOption customizes a Handler.
type HandlerOption func(*Handler)

// WithMaxBodySize changes the raw request-body limit. The default is one MiB.
func WithMaxBodySize(size int) HandlerOption {
	return func(h *Handler) {
		if size > 0 {
			h.maxBodySize = size
		}
	}
}

// NewHandler builds a reusable HTTP handler. It does not return an error so a
// nil source or intake can be reported as a clear 503 at request time while an
// application is assembling optional integrations.
func NewHandler(source Source, intake observing.EventIntake, secret string, options ...HandlerOption) *Handler {
	h := &Handler{source: source, intake: intake, secret: secret, maxBodySize: DefaultMaxBodySize}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

// ServeHTTP authenticates and forwards one webhook delivery.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.source == nil || h.intake == nil {
		http.Error(w, "webhook is not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := readBody(r, h.maxBodySize)
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "could not read webhook body", http.StatusBadRequest)
		return
	}
	if !h.source.Verify(r.Header, body, h.secret) {
		http.Error(w, "invalid webhook credential", http.StatusUnauthorized)
		return
	}
	observation, handled, err := h.source.Observe(r.Header, body)
	if err != nil {
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if !handled {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.intake.Observe(observation); err != nil {
		http.Error(w, "event intake failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readBody(r *http.Request, max int) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxBodySize
	}
	if r.Body == nil {
		return []byte{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > max {
		return nil, fmt.Errorf("%w: %d bytes", ErrBodyTooLarge, max)
	}
	return body, nil
}

// HeaderValue reads one header without relying on the caller having canonical
// map keys. Real net/http requests are canonicalized, but tests and adapters
// often construct http.Header values directly.
func HeaderValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
