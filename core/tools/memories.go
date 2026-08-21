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
// memories folder of their home — the Go port of spring-agent's memory
// advisor. The default system prompt's first working rule sends the model to
// MemoryView("MEMORY.md") before replying, so a run continues where the last
// one left off without the conversation history having to say so.
type MemoryTools struct {
	dir string
}

// NewMemoryTools returns the memory tools over a user's memories folder.
func NewMemoryTools(home *storage.UserHome) (*MemoryTools, error) {
	dir, err := home.Folder(storage.FolderMemories)
	if err != nil {
		return nil, err
	}
	return &MemoryTools{dir: dir}, nil
}

// Tools lists the memory tools.
func (m *MemoryTools) Tools() []tool.InvokableTool {
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
	return mustTool(utils.InferTool(ToolNameReadMemory,
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
	return mustTool(utils.InferTool(ToolNameWriteMemory,
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
// the two halves spring-agent split between SkillsTool and
// SkillManagementTools.
type SkillTools struct {
	dir string
}

// NewSkillTools returns the skill tools over a user's skills folder. The
// folder may not exist yet — listing then simply finds nothing.
func NewSkillTools(home *storage.UserHome) (*SkillTools, error) {
	dir, err := home.Folder(storage.FolderSkills)
	if err != nil {
		return nil, err
	}
	return &SkillTools{dir: dir}, nil
}

// Tools lists the skill tools.
func (s *SkillTools) Tools() []tool.InvokableTool {
	return []tool.InvokableTool{s.List(), s.Read(), s.Write(), s.Delete()}
}

func (s *SkillTools) resolve(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("skill path %q is outside the skills folder", name)
	}
	return filepath.Join(s.dir, clean), nil
}

// List lists the skills with their descriptions.
func (s *SkillTools) List() tool.InvokableTool {
	type skill struct {
		Name string `json:"name"`
		Desc string `json:"description,omitempty"`
	}
	type output struct {
		Skills []skill `json:"skills"`
	}
	return mustTool(utils.InferTool(ToolNameListSkills,
		"List the user's skills, with the description each skill's SKILL.md carries.",
		func(ctx context.Context, _ struct{}) (output, error) {
			entries, err := os.ReadDir(s.dir)
			if os.IsNotExist(err) {
				return output{}, nil
			}
			if err != nil {
				return output{}, err
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			out := output{}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				desc := ""
				if data, err := os.ReadFile(filepath.Join(s.dir, name, "SKILL.md")); err == nil {
					desc = firstParagraph(string(data))
				}
				out.Skills = append(out.Skills, skill{Name: name, Desc: desc})
			}
			return out, nil
		}))
}

// Read reads one file inside a skill.
func (s *SkillTools) Read() tool.InvokableTool {
	type output struct {
		Content string `json:"content"`
	}
	return mustTool(utils.InferTool(ToolNameReadSkillFile,
		"Read a file inside one of the user's skills.",
		func(ctx context.Context, in struct {
			Skill string `json:"skill"`
			File  string `json:"file"`
		}) (output, error) {
			path, err := s.resolve(in.Skill + "/" + in.File)
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
	return mustTool(utils.InferTool(ToolNameWriteSkill,
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
	return mustTool(utils.InferTool(ToolNameDeleteSkill,
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
