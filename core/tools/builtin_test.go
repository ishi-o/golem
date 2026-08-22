package tools

import (
	"context"
	"testing"

	"github.com/ishi-o/golem/core/storage"
)

// fakeSandbox arms the sandbox tools without a backend.
type fakeSandbox struct{}

func (fakeSandbox) Ensure(context.Context, string) (SandboxSession, error) { return nil, nil }
func (fakeSandbox) Remove(context.Context, string) (bool, error)           { return false, nil }
func (fakeSandbox) Close() error                                           { return nil }

// TestBuiltinLists locks the Builtin contract: every family's List yields
// the tools its name constants promise.
func TestBuiltinLists(t *testing.T) {
	home := storage.NewUserHome(t.TempDir())
	files, err := NewFileSystemTools(home)
	if err != nil {
		t.Fatal(err)
	}
	memories, err := NewMemoryTools(home)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := NewSkillTools(home)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := NewSandboxTools(fakeSandbox{}, SandboxToolsConfig{})
	if err != nil {
		t.Fatal(err)
	}

	families := map[string]Builtin{
		"clock":      NewCurrentDateTimeTools(),
		"todo":       NewTodoWriteTools(nil),
		"ask":        NewAskUserQuestionTools(nil, AskOptions{}),
		"files":      files,
		"memories":   memories,
		"skills":     skills,
		"publish":    NewPublishFileTools(nil, nil, home, ""),
		"sandbox":    sandbox,
		"credential": NewCredentialTools(nil, ""),
	}

	want := map[string][]string{
		"clock":      {ToolNameCurrentDateTime},
		"todo":       {ToolNameTodoWrite},
		"ask":        {ToolNameAskUserQuestion},
		"files":      {ToolNameReadFile, ToolNameWriteFile, ToolNameListFiles, ToolNameGrepFiles},
		"memories":   {ToolNameReadMemory, ToolNameWriteMemory},
		"skills":     {ToolNameListSkills, ToolNameReadSkillFile, ToolNameWriteSkill, ToolNameDeleteSkill},
		"publish":    {ToolNamePublishFile, ToolNameUpdatePublishedFile, ToolNameUnpublishFile, ToolNameRenewPublishedFile},
		"sandbox":    {ToolNameBash, ToolNameBashOutput, ToolNameKillShell, ToolNameRestartSandbox},
		"credential": {},
	}

	ctx := context.Background()
	for family, builtin := range families {
		got := builtin.List()
		if len(got) != len(want[family]) {
			t.Errorf("%s: List has %d tools, want %d", family, len(got), len(want[family]))
			continue
		}
		for i, tl := range got {
			info, err := tl.Info(ctx)
			if err != nil {
				t.Fatalf("%s: tool info: %v", family, err)
			}
			if info.Name != want[family][i] {
				t.Errorf("%s: tool %d is %q, want %q", family, i, info.Name, want[family][i])
			}
		}
	}

	// A credential family without a repository offers nothing.
	if got := NewCredentialTools(nil, "").List(); len(got) != 0 {
		t.Errorf("credential family without repo lists %d tools, want 0", len(got))
	}
}
