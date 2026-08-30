package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/storage"
)

// FileSystemTools are the file tools of one run, rooted at the user's
// workspace. The roots are enforced on every path: a model-walked "../" must
// not turn the workspace into a file reader for the whole machine.
//
// One instance per run (not one per process) because the roots are per user.
// A run with group or tenant scope reads across those homes' workspaces too;
// writes always land in the primary — what a run produces belongs to who
// asked.
type FileSystemTools struct {
	primary string
	roots   []string
}

// NewFileSystemTools returns the file tools over the run's home. The home's
// primary root receives every write; the further roots (a group's, a
// tenant's) are read through.
func NewFileSystemTools(home storage.Home) (*FileSystemTools, error) {
	primary, err := home.Folder(storage.FolderWorkspace)
	if err != nil {
		return nil, err
	}
	t := &FileSystemTools{primary: primary, roots: []string{primary}}
	for _, root := range home.Roots()[1:] {
		dir := filepath.Join(root, string(storage.FolderWorkspace))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		t.roots = append(t.roots, dir)
	}
	return t, nil
}

// List lists the file tools, satisfying Builtin.
func (f *FileSystemTools) List() []tool.InvokableTool {
	return []tool.InvokableTool{f.Read(), f.Write(), f.ListFiles(), f.Grep()}
}

// resolveWrite keeps the write inside the primary workspace root: the
// cleaned path must be relative and not ".."-prefixed, so the join below
// never escapes.
func (f *FileSystemTools) resolveWrite(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	if clean == "." || clean == "" {
		return f.primary, nil
	}
	return filepath.Join(f.primary, clean), nil
}

// resolveRead finds a readable path: the primary's file if it exists, else
// the first further root holding it, else the primary's path (the read
// surfaces the error rather than the resolver guessing).
func (f *FileSystemTools) resolveRead(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == "" {
		return f.primary, nil
	}
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	for _, root := range f.roots {
		candidate := filepath.Join(root, clean)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return filepath.Join(f.primary, clean), nil
}

// Read reads a file, optionally a window of lines.
func (f *FileSystemTools) Read() tool.InvokableTool {
	type input struct {
		Path string `json:"path"`
		// Offset and Limit select lines [Offset, Offset+Limit); zero Limit
		// reads to the end. Line windows rather than byte offsets because
		// the consumer is a model reading text.
		Offset int `json:"offset,omitempty"`
		Limit  int `json:"limit,omitempty"`
	}
	type output struct {
		Content string `json:"content"`
		Total   int    `json:"totalLines"`
	}
	return MustTool(utils.InferTool(ToolNameReadFile,
		"Read a file from the workspace. Returns its content; offset and limit select a range of lines.",
		func(ctx context.Context, in input) (output, error) {
			path, err := f.resolveRead(in.Path)
			if err != nil {
				return output{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return output{}, err
			}
			lines := strings.Split(string(data), "\n")
			total := len(lines)
			off, lim := in.Offset, in.Limit
			if off < 0 {
				off = 0
			}
			if off > total {
				off = total
			}
			if lim <= 0 || off+lim > total {
				lim = total - off
			}
			return output{Content: strings.Join(lines[off:off+lim], "\n"), Total: total}, nil
		}))
}

// Write writes a file whole.
func (f *FileSystemTools) Write() tool.InvokableTool {
	type input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	return MustTool(utils.InferTool(ToolNameWriteFile,
		"Write a file in the workspace, creating or replacing it whole. Parent directories are created.",
		func(ctx context.Context, in input) (string, error) {
			path, err := f.resolveWrite(in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return "wrote " + in.Path, nil
		}))
}

// ListFiles lists a directory, files first at one level, with sizes.
func (f *FileSystemTools) ListFiles() tool.InvokableTool {
	type entry struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size,omitempty"`
	}
	type output struct {
		Entries []entry `json:"entries"`
	}
	return MustTool(utils.InferTool(ToolNameListFiles,
		"List the files and subdirectories of a directory in the workspace.",
		func(ctx context.Context, in struct {
			Path string `json:"path"`
		}) (output, error) {
			path, err := f.resolveRead(in.Path)
			if err != nil {
				return output{}, err
			}
			dirEntries, err := os.ReadDir(path)
			if err != nil {
				return output{}, err
			}
			sort.Slice(dirEntries, func(i, j int) bool { return dirEntries[i].Name() < dirEntries[j].Name() })
			out := output{}
			for _, e := range dirEntries {
				size := int64(0)
				if info, err := e.Info(); err == nil {
					size = info.Size()
				}
				out.Entries = append(out.Entries, entry{Name: e.Name(), Dir: e.IsDir(), Size: size})
			}
			return out, nil
		}))
}

// Grep searches file contents under a directory.
func (f *FileSystemTools) Grep() tool.InvokableTool {
	type input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path,omitempty"`
	}
	type match struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Handle string `json:"text"`
	}
	type output struct {
		Matches []match `json:"matches"`
	}
	return MustTool(utils.InferTool(ToolNameGrepFiles,
		"Search for a regular expression in the files under a workspace directory. Returns file, line number and line text.",
		func(ctx context.Context, in input) (output, error) {
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return output{}, err
			}
			clean := filepath.Clean(filepath.FromSlash(in.Path))
			if strings.HasPrefix(clean, "..") {
				return output{}, fmt.Errorf("path %q is outside the workspace", in.Path)
			}
			out := output{}
			// A cap because a pattern matching everything in a big tree
			// would otherwise return the tree; the model narrows or pages.
			const maxMatches = 200
			for _, root := range f.roots {
				at := root
				if clean != "." && clean != "" {
					at = filepath.Join(root, clean)
				}
				if _, err := os.Stat(at); err != nil {
					continue
				}
				err := filepath.WalkDir(at, func(p string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					if len(out.Matches) >= maxMatches {
						return nil
					}
					data, err := os.ReadFile(p)
					if err != nil {
						return nil // unreadable files are skipped, not fatal
					}
					rel, _ := filepath.Rel(root, p)
					for i, line := range strings.Split(string(data), "\n") {
						if re.MatchString(line) {
							out.Matches = append(out.Matches, match{File: filepath.ToSlash(rel), Line: i + 1, Handle: strings.TrimSpace(line)})
							if len(out.Matches) >= maxMatches {
								break
							}
						}
					}
					return nil
				})
				if err != nil {
					return output{}, err
				}
			}
			return out, nil
		}))
}
