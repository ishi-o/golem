package storecontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPublishedResource(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().PublishedResources()

	r := store.PublishedResource{
		ID:            "token-1",
		OwnerID:       f.Owner(),
		Visibility:    store.VisibilityPublic,
		EntryFilename: "report.html",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}
	mustSave(t, repo.Save(ctx, r))

	found, err := repo.Get(ctx, "token-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, store.VisibilityPublic, found.Visibility)
	missing, err := repo.Get(ctx, "absent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	require.NoError(t, repo.Delete(ctx, "token-1"))
	found, err = repo.Get(ctx, "token-1")
	require.NoError(t, err)
	assert.Nil(t, found)
}
