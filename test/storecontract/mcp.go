package storecontract

import (
	"context"
	"testing"

	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMCPServerConfig(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().MCPServerConfigs()

	require.NoError(t, repo.Save(ctx, store.MCPServerConfig{
		ID:         "srv-1",
		OwnerID:    owner,
		Name:       "monitoring",
		Transport:  store.MCPTransportStreamableHTTP,
		URL:        "https://mcp.example.test/mcp",
		Headers:    map[string]string{"Authorization": "Bearer x"},
		Title:      "Monitoring",
		Version:    "2.0.0",
		Enabled:    true,
		SharedWith: []string{"ou_shared", store.SharedWithAll},
	}))

	found, err := repo.GetByOwnerAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "Bearer x", found.Headers["Authorization"])
	assert.Equal(t, []string{"ou_shared", store.SharedWithAll}, found.SharedWith)

	exists, err := repo.ExistsByOwnerAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	assert.True(t, exists)
	other, err := repo.ExistsByOwnerAndName(ctx, owner, "absent")
	require.NoError(t, err)
	assert.False(t, other)

	// A second server for the same owner must not disturb the first; a
	// second owner's server must stay invisible to the first owner's
	// queries.
	require.NoError(t, repo.Save(ctx, store.MCPServerConfig{ID: "srv-2", OwnerID: owner, Name: "other", Transport: store.MCPTransportStreamableHTTP, URL: "https://x.test"}))
	require.NoError(t, repo.Save(ctx, store.MCPServerConfig{ID: "srv-3", OwnerID: f.Owner(), Name: "monitoring", Transport: store.MCPTransportStreamableHTTP, URL: "https://y.test"}))
	all, err := repo.ListByOwner(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 2)

	require.NoError(t, repo.DeleteByOwnerAndName(ctx, owner, "monitoring"))
	// Deleting again is a no-op, not an error: the caller's intent ("make it
	// gone") already holds.
	require.NoError(t, repo.DeleteByOwnerAndName(ctx, owner, "monitoring"))
	all, err = repo.ListByOwner(ctx, owner)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func testMCPServerConfigAccess(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	// The caller owns the first server; the later f.Owner calls create the
	// distinct owners whose servers are reachable only through sharing.
	caller := owner
	repo := f.Backend().MCPServerConfigs()

	mustSave(t, repo.Save(ctx, store.MCPServerConfig{ID: "own-1", OwnerID: owner, Name: "own", Transport: store.MCPTransportStreamableHTTP, URL: "https://a.test"}))
	mustSave(t, repo.Save(ctx, store.MCPServerConfig{ID: "shr-1", OwnerID: f.Owner(), Name: "shared", Transport: store.MCPTransportStreamableHTTP, URL: "https://b.test", SharedWith: []string{caller}}))
	mustSave(t, repo.Save(ctx, repoSharedWithChat(f.Owner(), "chat-1")))
	mustSave(t, repo.Save(ctx, store.MCPServerConfig{ID: "pub-1", OwnerID: f.Owner(), Name: "public", Transport: store.MCPTransportStreamableHTTP, URL: "https://c.test", SharedWith: []string{store.SharedWithAll}}))
	// A server shared with somebody else reaches nobody in this test.
	mustSave(t, repo.Save(ctx, store.MCPServerConfig{ID: "oth-1", OwnerID: f.Owner(), Name: "elsewhere", Transport: store.MCPTransportStreamableHTTP, URL: "https://d.test", SharedWith: []string{"ou_nobody"}}))

	ids := func(configs []store.MCPServerConfig) []string {
		var out []string
		for _, c := range configs {
			out = append(out, c.ID)
		}
		return out
	}

	// Ownership plus sharing through the caller id, the chat, and the
	// everyone sentinel; the identifiers list mirrors what the runtime
	// derives via store.MCPServerConfigAccessIdentifiers.
	accessible, err := repo.ListAccessibleTo(ctx, caller, store.MCPServerConfigAccessIdentifiers(caller, "chat-1"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"own-1", "shr-1", "cht-1", "pub-1"}, ids(accessible))

	shared, err := repo.ListSharedWith(ctx, store.MCPServerConfigAccessIdentifiers(caller, ""))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"shr-1", "pub-1"}, ids(shared))
}

func repoSharedWithChat(owner, chatID string) store.MCPServerConfig {
	return store.MCPServerConfig{ID: "cht-1", OwnerID: owner, Name: "viachat", Transport: store.MCPTransportStreamableHTTP, URL: "https://e.test", SharedWith: []string{chatID}}
}
