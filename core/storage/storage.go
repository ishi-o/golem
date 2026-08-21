// Package storage is where published files live on disk, and the per-user
// directory layout the agent's file tools are rooted at.
package storage

import (
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Storage writes and reads files under a root directory. spring-agent's
// interface carried Spring's MultipartFile and Resource types; the Go port
// deals in plain io/fs and io, which is all any caller wanted from them.
type Storage interface {
	// Init creates the root directory when missing.
	Init() error

	// Store writes a file under the root, returning its path relative to the
	// root. The filename is cleaned; a name trying to escape the root is an
	// error rather than a silent write elsewhere.
	Store(r io.Reader, filename string) (string, error)

	// Load returns a reader over a stored file.
	Load(filename string) (fs.File, error)

	// LoadAll lists every stored path relative to the root, walking
	// subdirectories.
	LoadAll() ([]string, error)

	// DeleteAll removes everything under the root.
	DeleteAll() error

	// Resolve maps a stored-relative path to a filesystem path, refusing
	// names that escape the root.
	Resolve(filename string) (string, error)
}

// FileSystem is the one Storage implementation: files under a directory the
// process can see. There is deliberately no S3/GCS variant in this port yet;
// the interface exists because the share handler and the publish tool both
// speak it and a network-backed store should slot in without touching them.
type FileSystem struct {
	// Root is the directory everything is stored under.
	Root string
}

// NewFileSystem returns a Storage over root.
func NewFileSystem(root string) *FileSystem { return &FileSystem{Root: root} }

func (s *FileSystem) Init() error { return os.MkdirAll(s.Root, 0o755) }

func (s *FileSystem) Store(r io.Reader, filename string) (string, error) {
	clean, err := s.Resolve(filename)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(clean)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return filepath.Rel(s.Root, clean)
}

func (s *FileSystem) Load(filename string) (fs.File, error) {
	clean, err := s.Resolve(filename)
	if err != nil {
		return nil, err
	}
	return os.Open(clean)
}

func (s *FileSystem) LoadAll() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

func (s *FileSystem) DeleteAll() error {
	// RemoveContents rather than Remove of the root itself: the root is
	// created by Init and may be a mounted volume a redeploy expects to keep
	// existing.
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.Root, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileSystem) Resolve(filename string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(filename))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage path %q escapes the root", filename)
	}
	return filepath.Join(s.Root, clean), nil
}

// UserHome is one user's directory under the storage root: the workspace the
// file tools operate in, plus the folders of things the runtime keeps beside
// it. Created on demand, because a user's first action may touch any of them.
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
