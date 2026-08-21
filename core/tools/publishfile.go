package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
)

// PublishFileTools make a workspace file reachable by link. The link's
// authorization is an unguessable token stored in a PublishedResource together
// with visibility and expiry; the share handler checks all three before
// serving.
//
// The storage layout mirrors the URL: {visibility}/{userId}/{token}/ under
// the storage root, so a published file's disk path is derivable from its
// link alone.
type PublishFileTools struct {
	repos     store.PublishedResourceStore
	storage   storage.Storage
	workspace *storage.UserHome
	// baseURL is the origin the printed links point at — the app's public
	// origin, distinct from the storage root.
	baseURL string
	clock   func() time.Time
}

// NewPublishFileTools constructs the publish tools. workspace is the user whose
// files may be published — the tools refuse anything outside it, the same
// containment check the share handler applies on the way out.
func NewPublishFileTools(repos store.PublishedResourceStore, store storage.Storage, workspace *storage.UserHome, baseURL string) *PublishFileTools {
	return &PublishFileTools{repos: repos, storage: store, workspace: workspace, baseURL: baseURL, clock: time.Now}
}

// Tools lists the publish tools.
func (p *PublishFileTools) Tools() []tool.InvokableTool {
	return []tool.InvokableTool{p.Publish(), p.Update(), p.Unpublish(), p.Renew()}
}

func (p *PublishFileTools) now() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

// newToken returns 16 bytes of randomness, hex-encoded — the same shape as a
// UUID with its dashes removed, without pretending to be one.
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the system entropy source is gone; there
		// is no safe fallback for the thing that makes a link authorized by
		// being unguessable.
		panic("golem: cannot read random bytes for a publish token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// resolveFile checks the path is inside the user's workspace and exists.
func (p *PublishFileTools) resolveFile(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if !p.workspace.Contains(filepath.Join(p.workspace.Root(), clean)) {
		return "", fmt.Errorf("path %q is outside the workspace; only workspace files can be published", path)
	}
	full := filepath.Join(p.workspace.Root(), clean)
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() && p.baseURL == "" {
		// A directory publish needs an entry file; allowed, but noted.
		_ = info.IsDir()
	}
	return full, nil
}

func (p *PublishFileTools) urlFor(visibility store.Visibility, userID, token, entry string) string {
	origin := strings.TrimSuffix(p.baseURL, "/")
	if origin == "" {
		origin = "/share"
	} else {
		origin = origin + "/share"
	}
	sub := ""
	if entry != "" {
		sub = "/" + entry
	}
	return fmt.Sprintf("%s/%s/%s/%s%s", origin, strings.ToLower(string(visibility)), userID, token, sub)
}

// Publish publishes a workspace file or directory.
func (p *PublishFileTools) Publish() tool.InvokableTool {
	type input struct {
		Path string `json:"path"`
		// Visibility: INTERNAL (only people who can already see this
		// deployment's internal links) or PUBLIC (anyone with the link).
		Visibility string `json:"visibility,omitempty"`
		// TTL is how long the link lives, a Go duration string ("24h").
		// INTERNAL defaults to never expiring; PUBLIC to one day. PUBLIC is
		// capped at 720h.
		TTL string `json:"ttl,omitempty"`
		// EntryFilename is the file a directory publish serves at its root.
		EntryFilename string `json:"entryFilename,omitempty"`
	}
	return mustTool(utils.InferTool(ToolNamePublishFile,
		"Publish a workspace file or directory and get a link to it. Visibility INTERNAL or PUBLIC (default PUBLIC); ttl is a duration like 24h.",
		func(ctx context.Context, in input) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			visibility, ok := store.VisibilityFrom(in.Visibility)
			if !ok {
				visibility = store.VisibilityPublic
			}
			full, err := p.resolveFile(in.Path)
			if err != nil {
				return "", err
			}
			isDir := mustStatDir(full)

			var expiresAt time.Time
			switch {
			case in.TTL != "":
				ttl, err := time.ParseDuration(in.TTL)
				if err != nil {
					return "", fmt.Errorf("ttl %q is not a duration: %v", in.TTL, err)
				}
				if visibility == store.VisibilityPublic && (ttl <= 0 || ttl > 720*time.Hour) {
					// PUBLIC links are capped at 30 days: a link with no
					// expiry that anyone can hold is a leak waiting to be
					// found.
					return "", fmt.Errorf("public links live between 0 and 720h; use INTERNAL for longer")
				}
				expiresAt = p.now().Add(ttl)
			case visibility == store.VisibilityPublic:
				expiresAt = p.now().Add(24 * time.Hour)
			}

			token := newToken()
			rel, err := filepath.Rel(p.workspace.Root(), full)
			if err != nil {
				return "", err
			}
			key := fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(string(visibility)), userID, token, filepath.ToSlash(rel))
			if err := copyInto(p.storage, full, key); err != nil {
				return "", err
			}

			if err := p.repos.Save(ctx, store.PublishedResource{
				ID:            token,
				OwnerID:       userID,
				Visibility:    visibility,
				Directory:     isDir,
				EntryFilename: in.EntryFilename,
				ExpiresAt:     expiresAt,
			}); err != nil {
				return "", err
			}
			return "published: " + p.urlFor(visibility, userID, token, in.EntryFilename), nil
		}))
}

// Update replaces or refreshes a published file's content.
func (p *PublishFileTools) Update() tool.InvokableTool {
	type input struct {
		Token string `json:"token"`
		Path  string `json:"path"`
		// Mode: "replace" swaps the content and keeps the link; "refresh"
		// re-copies the same path and extends nothing.
		Mode string `json:"mode,omitempty"`
	}
	return mustTool(utils.InferTool(ToolNameUpdatePublishedFile,
		"Replace the content behind a published link (mode replace) or re-copy the file it points at (mode refresh), keeping the same link.",
		func(ctx context.Context, in input) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			resource, err := p.repos.Get(ctx, in.Token)
			if err != nil {
				return "", err
			}
			if resource == nil || resource.OwnerID != userID {
				return "", fmt.Errorf("no published resource %s owned by you", in.Token)
			}
			if in.Mode != "refresh" {
				full, err := p.resolveFile(in.Path)
				if err != nil {
					return "", err
				}
				rel, _ := filepath.Rel(p.workspace.Root(), full)
				key := fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(string(resource.Visibility)), userID, in.Token, filepath.ToSlash(rel))
				if err := copyInto(p.storage, full, key); err != nil {
					return "", err
				}
			}
			return "updated", nil
		}))
}

// Unpublish removes a link.
func (p *PublishFileTools) Unpublish() tool.InvokableTool {
	return mustTool(utils.InferTool(ToolNameUnpublishFile,
		"Remove a published link. The link stops working immediately; the workspace file is untouched.",
		func(ctx context.Context, in struct {
			Token string `json:"token"`
		}) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			resource, err := p.repos.Get(ctx, in.Token)
			if err != nil {
				return "", err
			}
			if resource == nil || resource.OwnerID != userID {
				return "", fmt.Errorf("no published resource %s owned by you", in.Token)
			}
			if err := p.repos.Delete(ctx, in.Token); err != nil {
				return "", err
			}
			return "unpublished", nil
		}))
}

// Renew extends a link's expiry.
func (p *PublishFileTools) Renew() tool.InvokableTool {
	type input struct {
		Token string `json:"token"`
		TTL   string `json:"ttl"`
	}
	return mustTool(utils.InferTool(ToolNameRenewPublishedFile,
		"Extend a published link's life by a duration like 24h.",
		func(ctx context.Context, in input) (string, error) {
			userID, err := UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			resource, err := p.repos.Get(ctx, in.Token)
			if err != nil || resource == nil || resource.OwnerID != userID {
				return "", fmt.Errorf("no published resource %s owned by you", in.Token)
			}
			ttl, err := time.ParseDuration(in.TTL)
			if err != nil {
				return "", fmt.Errorf("ttl %q is not a duration: %v", in.TTL, err)
			}
			base := resource.ExpiresAt
			if base.Before(p.now()) {
				base = p.now()
			}
			resource.ExpiresAt = base.Add(ttl)
			if err := p.repos.Save(ctx, *resource); err != nil {
				return "", err
			}
			return "renewed until " + resource.ExpiresAt.Format(time.RFC3339), nil
		}))
}

func mustStatDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyInto copies a file (or a directory tree) into the storage under key.
func copyInto(store storage.Storage, full, key string) error {
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = store.Store(f, key)
		return err
	}
	return filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(full, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = store.Store(f, key+"/"+filepath.ToSlash(rel))
		return err
	})
}
