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
// workspace — spring-agent's FileSystemTools over an allowedDirectory. The
// root is enforced on every path: the workspace is the agent's scratch
// space, and a model-walked "../" must not turn it into a file reader for
// the whole machine.
//
// One instance per run (not one per process) because the root is per user.
type FileSystemTools struct {
	home *storage.UserHome
}

// NewFileSystemTools returns the file tools rooted at the user's workspace.
func NewFileSystemTools(home *storage.UserHome) (*FileSystemTools, error) {
	ws, err := home.Folder(storage.FolderWorkspace)
	if err != nil {
		return nil, err
	}
	return &FileSystemTools{home: storage.NewUserHome(ws)}, nil
}

// Tools lists the file tools. Returned as a slice because the provider
// appends them to the run's set.
func (f *FileSystemTools) Tools() []tool.InvokableTool {
	return []tool.InvokableTool{f.Read(), f.Write(), f.List(), f.Grep()}
}

// resolve keeps every path inside the workspace root.
func (f *FileSystemTools) resolve(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == "" {
		return f.home.Root(), nil
	}
	if !f.home.Contains(filepath.Join(f.home.Root(), clean)) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	return filepath.Join(f.home.Root(), clean), nil
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
	return mustTool(utils.InferTool(ToolNameReadFile,
		"Read a file from the workspace. Returns its content; offset and limit select a range of lines.",
		func(ctx context.Context, in input) (output, error) {
			path, err := f.resolve(in.Path)
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
	return mustTool(utils.InferTool(ToolNameWriteFile,
		"Write a file in the workspace, creating or replacing it whole. Parent directories are created.",
		func(ctx context.Context, in input) (string, error) {
			path, err := f.resolve(in.Path)
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

// List lists a directory, files first at one level, with sizes.
func (f *FileSystemTools) List() tool.InvokableTool {
	type entry struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size,omitempty"`
	}
	type output struct {
		Entries []entry `json:"entries"`
	}
	return mustTool(utils.InferTool(ToolNameListFiles,
		"List the files and subdirectories of a directory in the workspace.",
		func(ctx context.Context, in struct {
			Path string `json:"path"`
		}) (output, error) {
			path, err := f.resolve(in.Path)
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
	return mustTool(utils.InferTool(ToolNameGrepFiles,
		"Search for a regular expression in the files under a workspace directory. Returns file, line number and line text.",
		func(ctx context.Context, in input) (output, error) {
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return output{}, err
			}
			root, err := f.resolve(in.Path)
			if err != nil {
				return output{}, err
			}
			out := output{}
			// A cap because a pattern matching everything in a big tree
			// would otherwise return the tree; the model narrows or pages.
			const maxMatches = 200
			err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
				rel, _ := filepath.Rel(f.home.Root(), p)
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
			return out, nil
		}))
}
