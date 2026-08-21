package daocontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/dao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPublishedResource(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().PublishedResources()

	r := dao.PublishedResource{
		ID:            "token-1",
		OwnerID:       f.Owner(),
		Visibility:    dao.VisibilityPublic,
		EntryFilename: "report.html",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}
	mustSave(t, repo.Save(ctx, r))

	found, err := repo.FindByID(ctx, "token-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.VisibilityPublic, found.Visibility)
	missing, err := repo.FindByID(ctx, "absent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	require.NoError(t, repo.DeleteByID(ctx, "token-1"))
	found, err = repo.FindByID(ctx, "token-1")
	require.NoError(t, err)
	assert.Nil(t, found)
}
