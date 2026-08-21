// Package share serves the files the publish tool made a link to.
//
// The URL shape is /share/{visibility}/{userId}/{token}/{subPath...}, and the
// three path variables are checked against the record before anything is
// read: an unknown token, a token reached by the wrong visibility, or a token
// with the wrong owner in the path are all a plain 404 — not 403, which would
// confirm to a prober that the token exists. An expired resource is 410 Gone:
// the link was once good, and the response says what happened to it rather
// than pretending it never existed.
package share

import (
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/storage"
)

// Handler serves published resources. It is an http.HandlerFunc compatible
// with any router, not tied to chi; the app mounts it under /share/.
type Handler struct {
	Repos   dao.PublishedResourceRepo
	Storage storage.Storage
	Now     func() time.Time
	Log     *slog.Logger
}

// NewHandler wires a handler. now defaults to time.Now and log to the default
// logger.
func NewHandler(repos dao.PublishedResourceRepo, store storage.Storage, log *slog.Logger) *Handler {
	h := &Handler{Repos: repos, Storage: store, Log: log}
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.Log == nil {
		h.Log = slog.Default()
	}
	return h
}

// ServeHTTP handles /share/{visibility}/{userId}/{token}/{subPath...}. The
// router must leave the sub-path unconsumed after the token; this handler
// reads it from the remaining URL path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Repos == nil || h.Storage == nil {
		http.Error(w, "share service is not configured", http.StatusServiceUnavailable)
		return
	}
	// The remaining path after /share/: split into visibility, userId,
	// token, and whatever sub-path follows.
	rest := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	visibilityRaw, userID, token := parts[0], parts[1], parts[2]
	if !safePathComponent(userID) || !safePathComponent(token) {
		http.NotFound(w, r)
		return
	}
	subPath := ""
	if len(parts) == 4 {
		subPath = parts[3]
	}

	// An unparsable visibility is a 404 like an unknown token, for the same
	// reason: the response must not distinguish "bad guess" from "good
	// guess, wrong path".
	visibility, ok := dao.VisibilityFrom(visibilityRaw)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	resource, err := h.Repos.FindByID(ctx, token)
	if err != nil {
		h.Log.Error("published resource lookup failed", "token", token, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if resource == nil || resource.Visibility != visibility || resource.OwnerID != userID {
		h.Log.Warn("no such published resource", "token", token, "visibility", visibilityRaw, "userId", userID)
		http.NotFound(w, r)
		return
	}

	if !resource.ExpiresAt.IsZero() && resource.ExpiresAt.Before(h.Now()) {
		w.WriteHeader(http.StatusGone)
		return
	}

	visibilityDir := strings.ToLower(string(visibility))
	filename := resource.EntryFilename
	if resource.Directory {
		switch {
		case subPath == "":
			// A directory requested without a sub-path serves its entry
			// file; a directory without one serves nothing.
			if filename == "" {
				http.NotFound(w, r)
				return
			}
		default:
			filename = subPath
		}
	} else if subPath != "" {
		http.NotFound(w, r)
		return
	}
	filename, ok = safeRelativePath(filename)
	if !ok || filename == "" {
		http.NotFound(w, r)
		return
	}

	storageKey := path.Join(visibilityDir, userID, token, filename)
	resolved, err := h.Storage.Resolve(storageKey)
	if err != nil {
		// Resolve rejects keys escaping the root; reaching that here means
		// the sub-path carried traversal, and the answer is the same 404 as
		// a missing file so the shape of the storage layout stays private.
		h.Log.Warn("rejected storage key outside the storage root", "token", token, "key", storageKey)
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		h.Log.Warn("published file missing on disk", "token", token, "path", resolved)
		http.NotFound(w, r)
		return
	}

	// Inline disposition: the link is how a user reads the thing, and a
	// download prompt on an HTML file defeats it. Serving user-uploaded HTML
	// inline is an XSS consideration the caller's CSP should own.
	contentType := mime.TypeByExtension(path.Ext(filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, resolved)
}

func safePathComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\\`) && path.Clean(value) == value
}

func safeRelativePath(value string) (string, bool) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return "", false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(value)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}
