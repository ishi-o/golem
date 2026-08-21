package daocontract

import (
	"context"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/dao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testScheduledTask(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ScheduledTasks()

	task := dao.ScheduledTask{
		ID:             "task-1",
		UserID:         owner,
		TaskText:       "check the thing",
		CronExpression: "*/5 * * * *",
		Background:     true,
		Status:         dao.ScheduledTaskStatusActive,
	}
	mustSave(t, repo.Save(ctx, task))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-2", UserID: owner, TaskText: "once", ScheduledAt: time.Now(), Status: dao.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-3", UserID: f.Owner(), TaskText: "other", Status: dao.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-4", UserID: f.Owner(), TaskText: "done", Status: dao.ScheduledTaskStatusCompleted}))

	active, err := repo.FindByStatus(ctx, dao.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, active, 3)
	mine, err := repo.FindByUserIDAndStatus(ctx, owner, dao.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, mine, 2)

	// The partial update the scheduler depends on: a firing, a cancel and a
	// completion can all land on the same task, none writing the others'
	// fields back.
	require.NoError(t, repo.UpdateStatus(ctx, "task-1", dao.ScheduledTaskStatusCompleted))
	found, err := repo.FindByID(ctx, "task-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.ScheduledTaskStatusCompleted, found.Status)
	assert.Equal(t, "check the thing", found.TaskText)
	assert.Equal(t, "*/5 * * * *", found.CronExpression)
	assert.True(t, found.Background)
}
