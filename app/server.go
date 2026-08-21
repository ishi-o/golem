// Package app is the HTTP surface of golem. It owns routing and lifecycle,
// while the agent and connector packages own domain behavior. The package is
// deliberately usable without a particular persistence backend: callers pass
// the handlers their selected adapter produced.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// RouterConfig configures the HTTP routes. Optional handlers are mounted only
// when supplied, which lets a deployment expose the share endpoint without
// enabling a connector it did not configure.
type RouterConfig struct {
	Logger        *slog.Logger
	ShareHandler  http.Handler
	FeishuHandler http.Handler
	Ready         func(context.Context) error
}

// NewRouter creates the application's chi router.
func NewRouter(config RouterConfig) chi.Router {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(requestLogger(config.Logger))
	router.Use(middleware.Recoverer)
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if config.Ready != nil {
			if err := config.Ready(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if config.ShareHandler != nil {
		// core/share parses the path after /share; StripPrefix keeps the
		// router's public URL and the handler's path contract separate.
		router.Handle("/share/*", http.StripPrefix("/share", config.ShareHandler))
	}
	if config.FeishuHandler != nil {
		router.Post("/webhooks/feishu", config.FeishuHandler.ServeHTTP)
	}
	return router
}

// Server owns the HTTP server settings and graceful shutdown policy.
type Server struct {
	http *http.Server
}

// NewServer creates a server with conservative timeouts. The handler owns
// application routes; this type only owns network lifecycle.
func NewServer(address string, handler http.Handler) *Server {
	return &Server{http: &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}}
}

// Addr reports the configured listen address.
func (s *Server) Addr() string {
	if s == nil || s.http == nil {
		return ""
	}
	return s.http.Addr
}

// Run serves until ctx is cancelled, then waits for active requests to leave.
func (s *Server) Run(ctx context.Context) error {
	if s == nil || s.http == nil {
		return errors.New("app: nil server")
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r)
			log.Info("http request", "method", r.Method, "path", r.URL.Path, "status", wrapped.Status(), "bytes", wrapped.BytesWritten(), "duration", time.Since(started).String())
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
