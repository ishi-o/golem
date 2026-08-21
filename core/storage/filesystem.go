// Package storage is where published files live on disk, and the per-user
// directory layout the agent's file tools are rooted at.
package storage

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Storage reads and writes files under a root directory using plain io/fs and
// io values.
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
