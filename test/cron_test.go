package agent_test

import (
	"testing"
	"time"

	"github.com/ishi-o/golem/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCronAcceptsSundaySeven(t *testing.T) {
	cron, err := service.ParseCron("0 0 * * 7")
	require.NoError(t, err)

	start := time.Date(2026, time.August, 21, 12, 30, 0, 0, time.UTC)
	next := cron.Next(start)
	want := time.Date(2026, time.August, 23, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, want, next)
}
