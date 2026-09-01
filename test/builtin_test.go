package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
)

// TestBuiltinLists locks the Builtin contract: every family's List yields
// the tools its name constants promise.
func TestBuiltinLists(t *testing.T) {
	home := storage.NewUserHome(t.TempDir())
	files, err := tools.NewFileSystemTools(home)
	require.NoError(t, err)
	memories, err := tools.NewMemoryTools(home)
	require.NoError(t, err)
	skills, err := tools.NewSkillTools(home)
	require.NoError(t, err)
	sandbox, err := tools.NewSandboxTools(&fakeSandbox{}, tools.SandboxToolsConfig{})
	require.NoError(t, err)

	families := map[string]tools.Builtin{
		"clock":      tools.NewCurrentDateTimeTools(),
		"todo":       tools.NewTodoWriteTools(nil),
		"ask":        tools.NewAskUserQuestionTools(nil, tools.AskOptions{}),
		"files":      files,
		"memories":   memories,
		"skills":     skills,
		"publish":    tools.NewPublishFileTools(nil, nil, home, ""),
		"sandbox":    sandbox,
		"credential": tools.NewCredentialTools(nil, ""),
	}
	want := map[string][]string{
		"clock":      {tools.ToolNameCurrentDateTime},
		"todo":       {tools.ToolNameTodoWrite},
		"ask":        {tools.ToolNameAskUserQuestion},
		"files":      {tools.ToolNameReadFile, tools.ToolNameWriteFile, tools.ToolNameListFiles, tools.ToolNameGrepFiles},
		"memories":   {tools.ToolNameReadMemory, tools.ToolNameWriteMemory, tools.ToolNameCreateMemory, tools.ToolNameInsertMemory, tools.ToolNameReplaceMemory, tools.ToolNameRenameMemory, tools.ToolNameDeleteMemory},
		"skills":     {tools.ToolNameListSkills, tools.ToolNameReadSkillFile, tools.ToolNameWriteSkill, tools.ToolNameDeleteSkill},
		"publish":    {tools.ToolNamePublishFile, tools.ToolNameUpdatePublishedFile, tools.ToolNameUnpublishFile, tools.ToolNameRenewPublishedFile},
		"sandbox":    {tools.ToolNameBash, tools.ToolNameBashOutput, tools.ToolNameKillShell, tools.ToolNameRestartSandbox},
		"credential": nil,
	}

	ctx := context.Background()
	for family, builtin := range families {
		got := builtin.List()
		if len(want[family]) == 0 {
			assert.Empty(t, got, "%s: expected no tools", family)
			continue
		}
		require.NotEmpty(t, got, family)
		names := make([]string, 0, len(got))
		for _, tl := range got {
			info, err := tl.Info(ctx)
			require.NoError(t, err, family)
			names = append(names, info.Name)
		}
		assert.Equal(t, want[family], names, family)
	}

	// A credential family without a repository offers nothing.
	assert.Empty(t, tools.NewCredentialTools(nil, "").List())
}
