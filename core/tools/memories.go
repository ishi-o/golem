package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/storage"
)

// MemoryTools are the agent's persistent notes about a user, kept in the
// memories folder of their home. The default system prompt asks the model to
// read MEMORY.md before replying, so memory remains an ordinary file tool.
type MemoryTools struct {
	dir string
}

// NewMemoryTools returns the memory tools over the run's memories folder.
// Memory is personal by design — the agent's notes about a user are not the
// group's to read — so only the home's primary root is consulted even when
// the run carries a group or tenant scope.
func NewMemoryTools(home storage.Home) (*MemoryTools, error) {
	dir, err := home.Folder(storage.FolderMemories)
	if err != nil {
		return nil, err
	}
	return &MemoryTools{dir: dir}, nil
}

// List lists the memory tools, satisfying Builtin.
func (m *MemoryTools) List() []tool.InvokableTool {
	return []tool.InvokableTool{m.Read(), m.Write(), m.Create(), m.Insert(), m.Replace(), m.Rename(), m.Delete()}
}

// resolve keeps memory paths inside the memories folder; the folder name is
// a suggestion ("MEMORY.md"), not a rule, and an agent may keep several
// notes.
func (m *MemoryTools) resolve(name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("memory file %q is outside the memories folder", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("memory file %q is outside the memories folder", name)
	}
	return filepath.Join(m.dir, clean), nil
}

// Read is MemoryView.
func (m *MemoryTools) Read() tool.InvokableTool {
	type output struct {
		Content string `json:"content"`
	}
	return MustTool(utils.InferTool(ToolNameReadMemory,
		"Read a file from your memory about this user. Call MemoryView(\"MEMORY.md\") before replying to recall what you already know.",
		func(ctx context.Context, in struct {
			File string `json:"file,omitempty"`
		}) (output, error) {
			name := in.File
			if name == "" {
				name = "MEMORY.md"
			}
			path, err := m.resolve(name)
			if err != nil {
				return output{}, err
			}
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return output{Content: "(no memory yet)"}, nil
			}
			if err != nil {
				return output{}, err
			}
			return output{Content: string(data)}, nil
		}))
}

// Write is MemoryWrite.
func (m *MemoryTools) Write() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameWriteMemory,
		"Write a memory file about this user, replacing it whole. Keep MEMORY.md as the index of what you know; record durable facts, not conversation transcripts.",
		func(ctx context.Context, in struct {
			File    string `json:"file,omitempty"`
			Content string `json:"content"`
		}) (string, error) {
			name := in.File
			if name == "" {
				name = "MEMORY.md"
			}
			path, err := m.resolve(name)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return "wrote " + name, nil
		}))
}

// Create makes a new memory file and refuses to overwrite an existing note.
func (m *MemoryTools) Create() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameCreateMemory,
		"Create a new memory file. Use MemoryWrite to replace an existing file deliberately.",
		func(ctx context.Context, in struct {
			File    string `json:"file"`
			Content string `json:"content"`
		}) (string, error) {
			path, err := m.resolve(in.File)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return "", err
			}
			_, writeErr := file.WriteString(in.Content)
			closeErr := file.Close()
			if writeErr != nil {
				return "", writeErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			return "created " + in.File, nil
		}))
}

// Insert inserts text at a rune position, or appends when Position is -1.
// Rune positions keep the operation safe for UTF-8 notes.
func (m *MemoryTools) Insert() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameInsertMemory,
		"Insert text into a memory file without rewriting unrelated content. Position is a zero-based rune offset; use -1 to append.",
		func(ctx context.Context, in struct {
			File     string `json:"file"`
			Content  string `json:"content"`
			Position int    `json:"position"`
		}) (string, error) {
			path, err := m.resolve(in.File)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			runes := []rune(string(data))
			position := in.Position
			if position < 0 || position > len(runes) {
				position = len(runes)
			}
			runes = append(runes[:position], append([]rune(in.Content), runes[position:]...)...)
			if err := writeMemoryFile(path, []byte(string(runes))); err != nil {
				return "", err
			}
			return "updated " + in.File, nil
		}))
}

// Replace performs a literal, single-pass string replacement in a memory
// file. The old text must occur so a misspelled edit is not reported as done.
func (m *MemoryTools) Replace() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameReplaceMemory,
		"Replace one literal string in a memory file. The old text must exist exactly once unless all occurrences are intentionally requested.",
		func(ctx context.Context, in struct {
			File string `json:"file"`
			Old  string `json:"old"`
			New  string `json:"new"`
			All  bool   `json:"all,omitempty"`
		}) (string, error) {
			if in.Old == "" {
				return "", fmt.Errorf("old text is required")
			}
			path, err := m.resolve(in.File)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(data)
			count := strings.Count(content, in.Old)
			if count == 0 {
				return "", fmt.Errorf("old text was not found")
			}
			if count > 1 && !in.All {
				return "", fmt.Errorf("old text occurs %d times; set all=true or make it unique", count)
			}
			if in.All {
				content = strings.ReplaceAll(content, in.Old, in.New)
			} else {
				content = strings.Replace(content, in.Old, in.New, 1)
			}
			if err := writeMemoryFile(path, []byte(content)); err != nil {
				return "", err
			}
			return "updated " + in.File, nil
		}))
}

// Rename moves a memory file without allowing an existing target to be
// overwritten accidentally.
func (m *MemoryTools) Rename() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameRenameMemory,
		"Rename a memory file inside the memories folder.",
		func(ctx context.Context, in struct {
			From string `json:"from"`
			To   string `json:"to"`
		}) (string, error) {
			from, err := m.resolve(in.From)
			if err != nil {
				return "", err
			}
			to, err := m.resolve(in.To)
			if err != nil {
				return "", err
			}
			if _, err := os.Stat(to); err == nil {
				return "", fmt.Errorf("memory file %q already exists", in.To)
			} else if !os.IsNotExist(err) {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return "", err
			}
			if err := os.Rename(from, to); err != nil {
				return "", err
			}
			return "renamed " + in.From + " to " + in.To, nil
		}))
}

// Delete removes one memory file, never a directory tree.
func (m *MemoryTools) Delete() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameDeleteMemory,
		"Delete one memory file. This cannot be undone.",
		func(ctx context.Context, in struct {
			File string `json:"file"`
		}) (string, error) {
			path, err := m.resolve(in.File)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("memory path %q is a directory", in.File)
			}
			if err := os.Remove(path); err != nil {
				return "", err
			}
			return "deleted " + in.File, nil
		}))
}

func writeMemoryFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".golem-memory-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

// SkillTools are the user's skill files: directories under skills/, each
// with a SKILL.md describing what it is for. Read-only view plus management,
// the read and management operations for user-owned skills.
type SkillTools struct {
	// dirs are the skills folders the run reads through, primary first; the
	// first is where skills are written and deleted.
	dirs []string
}

// NewSkillTools returns the skill tools over the run's skills folders: the
// primary home's for everything, plus the group's and tenant's for reading,
// so a skill the group shares is as usable as the user's own. A folder may
// not exist yet — listing then simply finds nothing.
func NewSkillTools(home storage.Home) (*SkillTools, error) {
	dirs, err := home.Dirs(storage.FolderSkills)
	if err != nil {
		return nil, err
	}
	return &SkillTools{dirs: dirs}, nil
}

// List lists the skill tools, satisfying Builtin.
func (s *SkillTools) List() []tool.InvokableTool {
	return []tool.InvokableTool{s.ListSkills(), s.Read(), s.Write(), s.Delete()}
}

// resolve finds a skill path: the primary's for writes and deletes, and for
// reads the first folder holding it.
func (s *SkillTools) resolve(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("skill path %q is outside the skills folder", name)
	}
	return filepath.Join(s.dirs[0], clean), nil
}

func (s *SkillTools) resolveRead(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("skill path %q is outside the skills folder", name)
	}
	for _, dir := range s.dirs {
		candidate := filepath.Join(dir, clean)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return filepath.Join(s.dirs[0], clean), nil
}

// ListSkills lists the skills with their descriptions.
func (s *SkillTools) ListSkills() tool.InvokableTool {
	type skill struct {
		Name string `json:"name"`
		Desc string `json:"description,omitempty"`
	}
	type output struct {
		Skills []skill `json:"skills"`
	}
	return MustTool(utils.InferTool(ToolNameListSkills,
		"List the user's skills, with the description each skill's SKILL.md carries.",
		func(ctx context.Context, _ struct{}) (output, error) {
			seen := map[string]bool{}
			out := output{}
			for _, dir := range s.dirs {
				entries, err := os.ReadDir(dir)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return output{}, err
				}
				for _, e := range entries {
					if !e.IsDir() || seen[e.Name()] {
						continue
					}
					// The primary's skill wins a name collision: it is the
					// one writes and deletes touch.
					seen[e.Name()] = true
					desc := ""
					if data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
						desc = firstParagraph(string(data))
					}
					out.Skills = append(out.Skills, skill{Name: e.Name(), Desc: desc})
				}
			}
			sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Name < out.Skills[j].Name })
			return out, nil
		}))
}

// Read reads one file inside a skill.
func (s *SkillTools) Read() tool.InvokableTool {
	type output struct {
		Content string `json:"content"`
	}
	return MustTool(utils.InferTool(ToolNameReadSkillFile,
		"Read a file inside one of the user's skills.",
		func(ctx context.Context, in struct {
			Skill string `json:"skill"`
			File  string `json:"file"`
		}) (output, error) {
			path, err := s.resolveRead(in.Skill + "/" + in.File)
			if err != nil {
				return output{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return output{}, err
			}
			return output{Content: string(data)}, nil
		}))
}

// Write writes a file inside a skill, creating the skill when new.
func (s *SkillTools) Write() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameWriteSkill,
		"Write a file inside one of the user's skills, creating or replacing it whole. Create a skill's SKILL.md first; its first paragraph is what ListSkills shows.",
		func(ctx context.Context, in struct {
			Skill   string `json:"skill"`
			File    string `json:"file"`
			Content string `json:"content"`
		}) (string, error) {
			path, err := s.resolve(in.Skill + "/" + in.File)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return "wrote " + in.Skill + "/" + in.File, nil
		}))
}

// Delete removes a whole skill.
func (s *SkillTools) Delete() tool.InvokableTool {
	return MustTool(utils.InferTool(ToolNameDeleteSkill,
		"Delete one of the user's skills, entirely. This cannot be undone.",
		func(ctx context.Context, in struct {
			Skill string `json:"skill"`
		}) (string, error) {
			path, err := s.resolve(in.Skill)
			if err != nil {
				return "", err
			}
			if err := os.RemoveAll(path); err != nil {
				return "", err
			}
			return "deleted skill " + in.Skill, nil
		}))
}

// firstParagraph extracts a SKILL.md's summary: the first non-empty,
// non-heading paragraph. Front matter (a leading --- block) is skipped.
func firstParagraph(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	inFrontMatter := strings.HasPrefix(strings.TrimSpace(md), "---")
	started := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFrontMatter {
			if trimmed == "---" && started {
				inFrontMatter = false
			}
			if trimmed == "---" {
				started = true
			}
			continue
		}
		if trimmed == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, " ")
}
