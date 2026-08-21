package daocontract

import (
	"context"
	"testing"

	"github.com/ishi-o/golem/core/dao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMcpServerConfig(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().McpServerConfigs()

	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{
		ID:         "srv-1",
		OwnerID:    owner,
		Name:       "monitoring",
		Transport:  dao.McpTransportStreamableHTTP,
		URL:        "https://mcp.example.test/mcp",
		Headers:    map[string]string{"Authorization": "Bearer x"},
		Title:      "Monitoring",
		Version:    "2.0.0",
		Enabled:    true,
		SharedWith: []string{"ou_shared", dao.SharedWithAll},
	}))

	found, err := repo.FindByOwnerIDAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "Bearer x", found.Headers["Authorization"])
	assert.Equal(t, []string{"ou_shared", dao.SharedWithAll}, found.SharedWith)

	exists, err := repo.ExistsByOwnerIDAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	assert.True(t, exists)
	other, err := repo.ExistsByOwnerIDAndName(ctx, owner, "absent")
	require.NoError(t, err)
	assert.False(t, other)

	// A second server for the same owner must not disturb the first; a
	// second owner's server must stay invisible to the first owner's
	// queries.
	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{ID: "srv-2", OwnerID: owner, Name: "other", Transport: dao.McpTransportStreamableHTTP, URL: "https://x.test"}))
	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{ID: "srv-3", OwnerID: f.Owner(), Name: "monitoring", Transport: dao.McpTransportStreamableHTTP, URL: "https://y.test"}))
	all, err := repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 2)

	require.NoError(t, repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"))
	// Deleting again is a no-op, not an error: the caller's intent ("make it
	// gone") already holds.
	require.NoError(t, repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"))
	all, err = repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func testMcpServerConfigAccess(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	// The caller owns the first server; the later f.Owner calls create the
	// distinct owners whose servers are reachable only through sharing.
	caller := owner
	repo := f.Backend().McpServerConfigs()

	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "own-1", OwnerID: owner, Name: "own", Transport: dao.McpTransportStreamableHTTP, URL: "https://a.test"}))
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "shr-1", OwnerID: f.Owner(), Name: "shared", Transport: dao.McpTransportStreamableHTTP, URL: "https://b.test", SharedWith: []string{caller}}))
	mustSave(t, repo.Save(ctx, repoSharedWithChat(f.Owner(), "chat-1")))
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "pub-1", OwnerID: f.Owner(), Name: "public", Transport: dao.McpTransportStreamableHTTP, URL: "https://c.test", SharedWith: []string{dao.SharedWithAll}}))
	// A server shared with somebody else reaches nobody in this test.
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "oth-1", OwnerID: f.Owner(), Name: "elsewhere", Transport: dao.McpTransportStreamableHTTP, URL: "https://d.test", SharedWith: []string{"ou_nobody"}}))

	ids := func(configs []dao.McpServerConfig) []string {
		var out []string
		for _, c := range configs {
			out = append(out, c.ID)
		}
		return out
	}

	// Ownership plus sharing through the caller id, the chat, and the
	// everyone sentinel; the identifiers list mirrors what the runtime
	// derives via dao.McpServerConfigAccessIdentifiers.
	accessible, err := repo.FindAccessibleTo(ctx, caller, dao.McpServerConfigAccessIdentifiers(caller, "chat-1"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"own-1", "shr-1", "cht-1", "pub-1"}, ids(accessible))

	shared, err := repo.FindBySharedWithIn(ctx, dao.McpServerConfigAccessIdentifiers(caller, ""))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"shr-1", "pub-1"}, ids(shared))
}

func repoSharedWithChat(owner, chatID string) dao.McpServerConfig {
	return dao.McpServerConfig{ID: "cht-1", OwnerID: owner, Name: "viachat", Transport: dao.McpTransportStreamableHTTP, URL: "https://e.test", SharedWith: []string{chatID}}
}
