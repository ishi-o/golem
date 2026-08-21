package daocontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/dao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPendingQuestion(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().PendingQuestions()

	q := dao.PendingQuestion{
		ID:             "pq-1",
		UserID:         f.Owner(),
		ConversationID: "conv-1",
		CardID:         "card-1",
		QuestionsJSON:  `[{"question":"Which?","options":["A","B"]}]`,
		Status:         dao.PendingQuestionStatusPending,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	mustSave(t, repo.Save(ctx, q))

	found, err := repo.FindByID(ctx, "pq-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, q.QuestionsJSON, found.QuestionsJSON)
	assert.Equal(t, "card-1", found.CardID)

	pending, err := repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// A partial update must leave every other field alone; callers race other
	// answer paths and cannot be trusted to write the rest of the row back.
	require.NoError(t, repo.UpdateStatus(ctx, "pq-1", dao.PendingQuestionStatusAnswered))
	found, err = repo.FindByID(ctx, "pq-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.PendingQuestionStatusAnswered, found.Status)
	assert.Equal(t, q.QuestionsJSON, found.QuestionsJSON)
	// What stops a double-submit from starting a second run.
	pending, err = repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	require.NoError(t, err)
	assert.Empty(t, pending)
}
