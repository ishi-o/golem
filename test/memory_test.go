package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryToolsSupportSafeStructuredEdits(t *testing.T) {
	home := storage.NewUserHome(t.TempDir())
	memory, err := tools.NewMemoryTools(home)
	require.NoError(t, err)
	ctx := context.Background()

	create := findTool(t, memory.List(), tools.ToolNameCreateMemory)
	insert := findTool(t, memory.List(), tools.ToolNameInsertMemory)
	replace := findTool(t, memory.List(), tools.ToolNameReplaceMemory)
	rename := findTool(t, memory.List(), tools.ToolNameRenameMemory)
	remove := findTool(t, memory.List(), tools.ToolNameDeleteMemory)

	invokeTool(t, create, ctx, `{"file":"notes.md","content":"你好"}`)
	_, err = create.InvokableRun(ctx, `{"file":"notes.md","content":"duplicate"}`)
	assert.Error(t, err, "create must not overwrite an existing memory")

	invokeTool(t, insert, ctx, `{"file":"notes.md","content":"世界","position":1}`)
	data, err := os.ReadFile(filepath.Join(home.Root(), "memories", "notes.md"))
	require.NoError(t, err)
	assert.Equal(t, "你世界好", string(data))

	invokeTool(t, replace, ctx, `{"file":"notes.md","old":"世界","new":"全世界"}`)
	data, err = os.ReadFile(filepath.Join(home.Root(), "memories", "notes.md"))
	require.NoError(t, err)
	assert.Equal(t, "你全世界好", string(data))

	invokeTool(t, rename, ctx, `{"from":"notes.md","to":"renamed.md"}`)
	assert.FileExists(t, filepath.Join(home.Root(), "memories", "renamed.md"))
	invokeTool(t, remove, ctx, `{"file":"renamed.md"}`)
	assert.NoFileExists(t, filepath.Join(home.Root(), "memories", "renamed.md"))

	_, err = remove.InvokableRun(ctx, `{"file":"../outside"}`)
	assert.Error(t, err)
}
