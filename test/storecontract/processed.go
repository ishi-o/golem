package storecontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testProcessedMessage(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().ProcessedMessages()

	claimed, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.True(t, claimed, "first claim lost the race against nobody")
	again, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.False(t, again, "second claim won; a redelivery would be answered twice")

	// Release is for the nothing-was-done case; the channel's retry must be
	// able to claim again afterwards.
	require.NoError(t, repo.Release(ctx, "msg-1"))
	third, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.True(t, third)
}
