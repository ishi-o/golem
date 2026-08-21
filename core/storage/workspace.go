package storage

import (
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

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
	if f == "" || strings.ContainsAny(string(f), `/\\`) || filepath.Clean(string(f)) == ".." || strings.HasPrefix(filepath.Clean(string(f)), ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid user-home folder %q", f)
	}
	dir := filepath.Join(h.root, string(f))
	return dir, os.MkdirAll(dir, 0o755)
}

// Contains reports whether candidate sits inside this home. The publish tool
// and the image reuploader both gate on it: a path outside the user's
// workspace is somebody else's data, and "publish" must not become a way to
// hand out a link to it.
func (h *UserHome) Contains(candidate string) bool {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	return abs == h.root || strings.HasPrefix(abs, h.root+string(filepath.Separator))
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
	return NewUserHome(filepath.Join(f.Location, clean))
}
