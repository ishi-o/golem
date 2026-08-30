package storage

import (
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Home is one run's view of the directories its tools work in: the primary
// root things are created under, every root reads may span, and the
// containment test publish and upload gate on. A user's own home and a
// composite over several scopes implement it alike.
type Home interface {
	// Root is where this home's writes belong, absolute.
	Root() string
	// Folder creates (when missing) and returns one of the well-known
	// folders under the primary root.
	Folder(f Folder) (string, error)
	// Roots lists every root this home spans, primary first. Reads may look
	// in all of them; writes go to the primary.
	Roots() []string
	// Dirs returns the well-known folder under every root, primary first —
	// the search path a read-only family (skills, say) walks.
	Dirs(f Folder) ([]string, error)
	// Contains reports whether candidate sits inside any root this home
	// spans. A path outside them all is somebody else's data, and
	// "publish" must not become a way to hand out a link to it.
	Contains(candidate string) bool
}

type UserHome struct {
	root string
}

// Folder names one of the well-known subdirectories. The names are stable
// on-disk format: a user upgrading the runtime keeps their files where they
// were.
type Folder string

const (
	FolderMemories  Folder = "memories"
	FolderArtifacts Folder = "artifacts"
	FolderSkills    Folder = "skills"
	FolderWorkspace Folder = "workspace"
)

// NewUserHome roots a user's directories at root.
func NewUserHome(root string) *UserHome {
	// Absolute and normalized once, here, so Contains below is a prefix test
	// callers can trust rather than a lexical comparison of differently
	// spelled paths.
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &UserHome{root: filepath.Clean(abs)}
}

// Root is the user's directory, absolute.
func (h *UserHome) Root() string { return h.root }

// Folder creates (when missing) and returns one of the well-known folders.
func (h *UserHome) Folder(f Folder) (string, error) {
	if err := validFolder(f); err != nil {
		return "", err
	}
	dir := filepath.Join(h.root, string(f))
	return dir, os.MkdirAll(dir, 0o755)
}

// Roots lists the one root a user's own home spans.
func (h *UserHome) Roots() []string { return []string{h.root} }

// Dirs returns the well-known folder under the one root.
func (h *UserHome) Dirs(f Folder) ([]string, error) {
	dir, err := h.Folder(f)
	if err != nil {
		return nil, err
	}
	return []string{dir}, nil
}

// Contains reports whether candidate sits inside this home. The publish tool
// and the image reuploader both gate on it: a path outside the user's
// workspace is somebody else's data, and "publish" must not become a way to
// hand out a link to it.
func (h *UserHome) Contains(candidate string) bool {
	return containsRoot(h.root, candidate)
}

func validFolder(f Folder) error {
	if f == "" || strings.ContainsAny(string(f), `/\\`) || filepath.Clean(string(f)) == ".." || strings.HasPrefix(filepath.Clean(string(f)), ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid user-home folder %q", f)
	}
	return nil
}

func containsRoot(root, candidate string) bool {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// CompositeHome is one run's view of several scopes' homes: the user's own,
// the group's, the tenant's. The primary home is where writes belong — what
// a run produces belongs to who asked — and reads span every member, so a
// file the group shares is as readable as the user's own.
type CompositeHome struct {
	primary Home
	members []Home
}

// NewCompositeHome builds a home over primary (which writes and Folder
// belong to) and the further members reads may span, in order.
func NewCompositeHome(primary Home, members ...Home) *CompositeHome {
	return &CompositeHome{primary: primary, members: members}
}

// Root is the primary home's root: where this run's writes belong.
func (c *CompositeHome) Root() string { return c.primary.Root() }

// Folder creates (when missing) and returns one of the well-known folders
// under the primary root.
func (c *CompositeHome) Folder(f Folder) (string, error) { return c.primary.Folder(f) }

// Roots lists every member's root, primary first.
func (c *CompositeHome) Roots() []string {
	roots := append([]string(nil), c.primary.Roots()...)
	for _, m := range c.members {
		roots = append(roots, m.Roots()...)
	}
	return roots
}

// Dirs returns the well-known folder under every member, primary first.
func (c *CompositeHome) Dirs(f Folder) ([]string, error) {
	if err := validFolder(f); err != nil {
		return nil, err
	}
	dirs, err := c.primary.Dirs(f)
	if err != nil {
		return nil, err
	}
	for _, m := range c.members {
		// Created rather than assumed present, same as the primary: a dir
		// that fails to materialize costs the read path nothing to skip.
		if d, err := m.Folder(f); err == nil {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// Contains reports whether candidate sits inside any member's roots.
func (c *CompositeHome) Contains(candidate string) bool {
	if c.primary.Contains(candidate) {
		return true
	}
	for _, m := range c.members {
		if m.Contains(candidate) {
			return true
		}
	}
	return false
}

// WorkspaceFactory builds per-user homes under a storage location. One type
// rather than a bare function so integrations can substitute their own
// layout (a per-tenant prefix, say) without touching the call sites.
type WorkspaceFactory struct {
	// Location is the storage root user directories live under.
	Location string
}

// NewWorkspaceFactory returns a factory over location.
func NewWorkspaceFactory(location string) *WorkspaceFactory {
	return &WorkspaceFactory{Location: location}
}

// ForOwner returns the home of one owner. The ownerId is a channel identity
// (a Feishu open_id, a CLI user name) and is sanitized: those ids are safe
// as path components on every channel seen so far, but "safe so far" is not
// a contract, and an id carrying "/" or ".." must not become a directory
// traversal.
func (f *WorkspaceFactory) ForOwner(ownerID string) *UserHome {
	return NewUserHome(filepath.Join(f.Location, sanitizeOwner(ownerID)))
}

// ForGroup returns the home a group's shared files live under. Namespaced
// under groups/ so a group id can never collide with a user id, however the
// channel mints them.
func (f *WorkspaceFactory) ForGroup(groupID string) *UserHome {
	if strings.TrimSpace(groupID) == "" {
		return NewUserHome(filepath.Join(f.Location))
	}
	return NewUserHome(filepath.Join(f.Location, "groups", sanitizeOwner(groupID)))
}

// ForTenant returns the home a tenant's shared files live under, namespaced
// under tenants/ for the same reason as the group home.
func (f *WorkspaceFactory) ForTenant(tenantID string) *UserHome {
	if strings.TrimSpace(tenantID) == "" {
		return NewUserHome(filepath.Join(f.Location))
	}
	return NewUserHome(filepath.Join(f.Location, "tenants", sanitizeOwner(tenantID)))
}

func sanitizeOwner(ownerID string) string {
	clean := path.Clean("/" + ownerID)
	// Ordinary channel ids remain readable on disk. An id that can alter
	// path structure is encoded instead of rejected: rejecting it would make
	// a channel identity unable to use the agent, while using it raw would
	// cross the per-user boundary.
	if ownerID == "" || clean == "/" || strings.ContainsAny(ownerID, `/\\`) || strings.HasPrefix(ownerID, "id-") || clean == "/.." || strings.HasPrefix(clean, "/../") {
		clean = "id-" + base64.RawURLEncoding.EncodeToString([]byte(ownerID))
	} else {
		clean = strings.TrimPrefix(clean, "/")
	}
	return clean
}
