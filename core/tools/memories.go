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
	return []tool.InvokableTool{m.Read(), m.Write()}
}

// resolve keeps memory paths inside the memories folder; the folder name is
// a suggestion ("MEMORY.md"), not a rule, and an agent may keep several
// notes.
func (m *MemoryTools) resolve(name string) (string, error) {
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
