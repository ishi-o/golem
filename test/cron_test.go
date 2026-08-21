package agent_test

import (
	"testing"
	"time"

	"github.com/ishi-o/golem/internal/service"
)

func TestCronAcceptsSundaySeven(t *testing.T) {
	cron, err := service.ParseCron("0 0 * * 7")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, time.August, 21, 12, 30, 0, 0, time.UTC)
	next := cron.Next(start)
	if want := time.Date(2026, time.August, 23, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
