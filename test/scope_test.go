package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
)

// TestCompositeHomeSpansScopesForReading writes to the primary covers the
// group/tenant scoping contract of storage.CompositeHome: reads span every
// member, writes and Folder belong to the primary.
func TestCompositeHomeSpansScopesForReading(t *testing.T) {
	dir := t.TempDir()
	factory := storage.NewWorkspaceFactory(dir)
	user := factory.ForOwner("user-scope")
	group := factory.ForGroup("../../etc") // traversal-shaped on purpose
	tenant := factory.ForTenant("tenant-1")

	home := storage.NewCompositeHome(user, group, tenant)

	// Roots are namespaced: a group id can never collide with a user id.
	assert.Equal(t, []string{user.Root(), group.Root(), tenant.Root()}, home.Roots())
	assert.Contains(t, group.Root(), filepath.Join(dir, "groups"))

	// Writes belong to the primary.
	ws, err := home.Folder(storage.FolderWorkspace)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "mine.txt"), []byte("mine"), 0o644))

	// A group skill is readable through the composite home.
	skillDirs, err := home.Dirs(storage.FolderSkills)
	require.NoError(t, err)
	require.Len(t, skillDirs, 3)
	require.NoError(t, os.MkdirAll(filepath.Join(skillDirs[1], "shared"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDirs[1], "shared", "SKILL.md"), []byte("# Shared\nA group skill."), 0o644))

	skills, err := tools.NewSkillTools(home)
	require.NoError(t, err)
	list := findTool(t, skills.List(), tools.ToolNameListSkills)
	out := invokeTool(t, list, context.Background(), `{}`)
	assert.Contains(t, out, "shared", "a group skill should be listed through the composite home")

	// Containment spans every member; a path outside them all is refused.
	assert.True(t, home.Contains(filepath.Join(group.Root(), "anything")))
	assert.False(t, home.Contains(filepath.Join(dir, "elsewhere")))

	// File tools read through the scopes and write to the primary.
	files, err := tools.NewFileSystemTools(home)
	require.NoError(t, err)
	read := findTool(t, files.List(), tools.ToolNameReadFile)
	groupFile := filepath.Join(group.Root(), string(storage.FolderWorkspace), "groupnote.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(groupFile), 0o755))
	require.NoError(t, os.WriteFile(groupFile, []byte("from the group"), 0o644))
	out = invokeTool(t, read, context.Background(), `{"path":"groupnote.txt"}`)
	assert.Contains(t, out, "from the group", "a group file should be readable through the composite home")

	write := findTool(t, files.List(), tools.ToolNameWriteFile)
	_ = invokeTool(t, write, context.Background(), `{"path":"new.txt","content":"written by the run"}`)
	data, err := os.ReadFile(filepath.Join(ws, "new.txt"))
	require.NoError(t, err, "writes must land in the primary (user's) workspace")
	assert.Equal(t, "written by the run", string(data))
	_, err = os.Stat(filepath.Join(group.Root(), string(storage.FolderWorkspace), "new.txt"))
	require.Error(t, err, "writes must not land in the group's workspace")
}
