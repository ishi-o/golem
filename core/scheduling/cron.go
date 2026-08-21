// Package scheduling fires the tasks the model scheduled: a minimal cron
// parser, the scheduler service, and the schedule tools.
package scheduling

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field cron expression (minute hour day-of-month month
// day-of-week). A library-sized problem done in ~120 lines rather than
// pulled in as a dependency: the runtime needs Next and nothing else, and
// every field the full libraries handle (seconds, years, nicknames) is one
// more way for a model-written expression to surprise.
type Cron struct {
	minute, hour, dom, month, dow uint32
}

// ParseCron parses a 5-field cron expression. Supported per field: *, */n,
// a, a-b, a-b/n, and comma lists of those. Day-of-week is 0-6 with both 0
// and 7 meaning Sunday.
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("cron %q must have 5 fields (minute hour day-of-month month day-of-week)", expr)
	}
	var sets [5]uint32
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, f := range fields {
		s, err := parseField(f, bounds[i][0], bounds[i][1])
		if err != nil {
			return Cron{}, fmt.Errorf("cron field %d (%q): %w", i+1, f, err)
		}
		sets[i] = s
	}
	// Day-of-week 7 (Sunday, cron's one ambiguity) folds onto 0.
	if sets[4]&(1<<7) != 0 {
		sets[4] |= 1
	}
	return Cron{minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4]}, nil
}

func parseField(f string, min, max int) (uint32, error) {
	var set uint32
	for _, part := range strings.Split(f, ",") {
		lo, hi, step := min, max, 1
		rangeAndStep := strings.SplitN(part, "/", 2)
		if rangeAndStep[0] != "*" {
			bounds := strings.SplitN(rangeAndStep[0], "-", 2)
			v, err := strconv.Atoi(bounds[0])
			if err != nil || v < min || v > max {
				return 0, fmt.Errorf("value %q out of range %d-%d", bounds[0], min, max)
			}
			lo = v
			hi = v
			if len(bounds) == 2 {
				v2, err := strconv.Atoi(bounds[1])
				if err != nil || v2 < min || v2 > max {
					return 0, fmt.Errorf("value %q out of range %d-%d", bounds[1], min, max)
				}
				hi = v2
			}
		}
		if len(rangeAndStep) == 2 {
			s, err := strconv.Atoi(rangeAndStep[1])
			if err != nil || s <= 0 {
				return 0, fmt.Errorf("step %q must be positive", rangeAndStep[1])
			}
			step = s
		}
		if hi < lo {
			return 0, fmt.Errorf("range %q is descending", part)
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

// Next returns the first firing strictly after t, or the zero time if none
// occurs within four years (a month/day combination that never matches, say
// February 31st).
func (c Cron) Next(t time.Time) time.Time {
	// Start at the next whole minute after t.
	n := t.Truncate(time.Minute).Add(time.Minute)
	limit := n.Add(4 * 365 * 24 * time.Hour)
	for n.Before(limit) {
		if c.month&(1<<uint(n.Month())) == 0 {
			// Wrong month: jump to the first minute of the next one.
			n = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, n.Location()).AddDate(0, 1, 0)
			continue
		}
		if !c.dayMatches(n) {
			n = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).AddDate(0, 0, 1)
			continue
		}
		if c.hour&(1<<uint(n.Hour())) == 0 {
			n = time.Date(n.Year(), n.Month(), n.Day(), n.Hour(), 0, 0, 0, n.Location()).Add(time.Hour)
			continue
		}
		if c.minute&(1<<uint(n.Minute())) == 0 {
			n = n.Add(time.Minute)
			continue
		}
		return n
	}
	return time.Time{}
}

// dayMatches applies cron's day rule: when both day-of-month and day-of-week
// are restricted, either matching is enough; when only one is restricted,
// that one decides.
func (c Cron) dayMatches(n time.Time) bool {
	domSet := c.dom != allBits(1, 31)
	dowSet := c.dow != allBits(0, 6)
	domOK := c.dom&(1<<uint(n.Day())) != 0
	// Go's Weekday: Sunday = 0, matching cron.
	dowOK := c.dow&(1<<uint(n.Weekday())) != 0
	switch {
	case domSet && dowSet:
		return domOK || dowOK
	case domSet:
		return domOK
	case dowSet:
		return dowOK
	default:
		return true
	}
}

func allBits(min, max int) uint32 {
	var set uint32
	for v := min; v <= max; v++ {
		set |= 1 << uint(v)
	}
	return set
}
