package storecontract

import (
	"context"
	"testing"

	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testShellCredential(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ShellCredentials()

	cred := store.ShellCredential{ID: store.ShellCredentialID(owner, "api"), OwnerID: owner, Name: "api", Value: "secret"}
	mustSave(t, repo.Save(ctx, cred))

	found, err := repo.GetByOwnerAndName(ctx, owner, "api")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "secret", found.Value)
	all, err := repo.ListByOwner(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 1)

	require.NoError(t, repo.DeleteByOwnerAndName(ctx, owner, "api"))
	all, err = repo.ListByOwner(ctx, owner)
	require.NoError(t, err)
	assert.Empty(t, all)
}
