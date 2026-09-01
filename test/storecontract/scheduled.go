package storecontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testScheduledTask(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ScheduledTasks()
	nextFireAt := time.Now().Add(5 * time.Minute)

	task := store.ScheduledTask{
		ID:             "task-1",
		UserID:         owner,
		TaskText:       "check the thing",
		CronExpression: "*/5 * * * *",
		ConversationID: "conversation-1",
		GroupID:        "group-1",
		TenantID:       "tenant-1",
		NextFireAt:     nextFireAt,
		MaxRuns:        3,
		RunCount:       1,
		Background:     true,
		Status:         store.ScheduledTaskStatusActive,
	}
	mustSave(t, repo.Save(ctx, task))
	mustSave(t, repo.Save(ctx, store.ScheduledTask{ID: "task-2", UserID: owner, TaskText: "once", ScheduledAt: time.Now(), Status: store.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, store.ScheduledTask{ID: "task-3", UserID: f.Owner(), TaskText: "other", Status: store.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, store.ScheduledTask{ID: "task-4", UserID: f.Owner(), TaskText: "done", Status: store.ScheduledTaskStatusCompleted}))

	active, err := repo.ListByStatus(ctx, store.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, active, 3)
	mine, err := repo.ListByUserAndStatus(ctx, owner, store.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, mine, 2)

	// The partial update the scheduler depends on: a firing, a cancel and a
	// completion can all land on the same task, none writing the others'
	// fields back.
	require.NoError(t, repo.SetStatus(ctx, "task-1", store.ScheduledTaskStatusCompleted))
	found, err := repo.Get(ctx, "task-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, store.ScheduledTaskStatusCompleted, found.Status)
	assert.Equal(t, "check the thing", found.TaskText)
	assert.Equal(t, "*/5 * * * *", found.CronExpression)
	assert.Equal(t, "conversation-1", found.ConversationID)
	assert.Equal(t, "group-1", found.GroupID)
	assert.Equal(t, "tenant-1", found.TenantID)
	assert.WithinDuration(t, nextFireAt, found.NextFireAt, time.Millisecond)
	assert.Equal(t, 3, found.MaxRuns)
	assert.Equal(t, 1, found.RunCount)
	assert.True(t, found.Background)
}
