package cron

import "time"

// SpecSchedule specifies a duty cycle (to the second granularity), based on a
// traditional crontab specification. It is computed initially and stored as bit sets.
type SpecSchedule struct {
	Second, Minute, Hour, Dom, Month, Dow uint64

	// Override location for this schedule.
	Location *time.Location
}

// bounds provides a range of acceptable values (plus a map of name to value).
type bounds struct {
	min, max uint
	names    map[string]uint
}

// The bounds for each field.
var (
	seconds = bounds{0, 59, nil}
	minutes = bounds{0, 59, nil}
	hours   = bounds{0, 23, nil}
	dom     = bounds{1, 31, nil}
	months  = bounds{1, 12, map[string]uint{
		"jan": 1,
		"feb": 2,
		"mar": 3,
		"apr": 4,
		"may": 5,
		"jun": 6,
		"jul": 7,
		"aug": 8,
		"sep": 9,
		"oct": 10,
		"nov": 11,
		"dec": 12,
	}}
	dow = bounds{0, 6, map[string]uint{
		"sun": 0,
		"mon": 1,
		"tue": 2,
		"wed": 3,
		"thu": 4,
		"fri": 5,
		"sat": 6,
	}}
)

const (
	// Set the top bit if a star was included in the expression.
	starBit = uint64(1) << 63

	// The remaining high bits encode Quartz day-of-month modifiers. Regular
	// day-of-month values only use bits 1 through 31.
	lastDomFlag           = uint64(1) << 62
	nearestWeekdayDomFlag = uint64(1) << 61
	lastDomOffsetShift    = 56
	lastDomOffsetMask     = uint64(31) << lastDomOffsetShift

	// The remaining high bits encode Quartz day-of-week modifiers. Regular
	// day-of-week values only use bits 0 through 6.
	nthDowFlag  = uint64(1) << 62
	nthDowShift = 59
	nthDowMask  = uint64(7) << nthDowShift
	lastDowFlag = uint64(1) << 58
)

// Next returns the next time this schedule is activated, greater than the given
// time.  If no time can be found to satisfy the schedule, return the zero time.
func (s *SpecSchedule) Next(t time.Time) time.Time {
	// General approach
	//
	// For Month, Day, Hour, Minute, Second:
	// Check if the time value matches.  If yes, continue to the next field.
	// If the field doesn't match the schedule, then increment the field until it matches.
	// While incrementing the field, a wrap-around brings it back to the beginning
	// of the field list (since it is necessary to re-verify previous field
	// values)

	// Convert the given time into the schedule's timezone, if one is specified.
	// Save the original timezone so we can convert back after we find a time.
	// Note that schedules without a time zone specified (time.Local) are treated
	// as local to the time provided.
	origLocation := t.Location()
	loc := s.Location
	if loc == time.Local {
		loc = t.Location()
	}
	if s.Location != time.Local {
		t = t.In(s.Location)
	}

	// Start at the earliest possible time (the upcoming second).
	t = t.Add(1*time.Second - time.Duration(t.Nanosecond())*time.Nanosecond)

	// This flag indicates whether a field has been incremented.
	added := false

	// If no time is found within five years, return zero.
	yearLimit := t.Year() + 5

WRAP:
	if t.Year() > yearLimit {
		return time.Time{}
	}

	// Find the first applicable month.
	// If it's this month, then do nothing.
	for 1<<uint(t.Month())&s.Month == 0 {
		// If we have to add a month, reset the other parts to 0.
		if !added {
			added = true
			// Otherwise, set the date at the beginning (since the current time is irrelevant).
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		}
		t = t.AddDate(0, 1, 0)

		// Wrapped around.
		if t.Month() == time.January {
			goto WRAP
		}
	}

	// Now get a day in that month.
	//
	// NOTE: This causes issues for daylight savings regimes where midnight does
	// not exist.  For example: Sao Paulo has DST that transforms midnight on
	// 11/3 into 1am. Handle that by noticing when the Hour ends up != 0.
	for !dayMatches(s, t) {
		if !added {
			added = true
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		}
		t = t.AddDate(0, 0, 1)
		// Notice if the hour is no longer midnight due to DST.
		// Add an hour if it's 23, subtract an hour if it's 1.
		if t.Hour() != 0 {
			if t.Hour() > 12 {
				t = t.Add(time.Duration(24-t.Hour()) * time.Hour)
			} else {
				t = t.Add(time.Duration(-t.Hour()) * time.Hour)
			}
		}

		if t.Day() == 1 {
			goto WRAP
		}
	}

	for 1<<uint(t.Hour())&s.Hour == 0 {
		if !added {
			added = true
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
		}
		t = t.Add(1 * time.Hour)

		if t.Hour() == 0 {
			goto WRAP
		}
	}

	for 1<<uint(t.Minute())&s.Minute == 0 {
		if !added {
			added = true
			t = t.Truncate(time.Minute)
		}
		t = t.Add(1 * time.Minute)

		if t.Minute() == 0 {
			goto WRAP
		}
	}

	for 1<<uint(t.Second())&s.Second == 0 {
		if !added {
			added = true
			t = t.Truncate(time.Second)
		}
		t = t.Add(1 * time.Second)

		if t.Second() == 0 {
			goto WRAP
		}
	}

	return t.In(origLocation)
}

// dayMatches returns true if the schedule's day-of-week and day-of-month
// restrictions are satisfied by the given time.
func dayMatches(s *SpecSchedule, t time.Time) bool {
	var (
		domMatch = dayOfMonthMatches(s.Dom, t)
		dowMatch = dayOfWeekMatches(s.Dow, t)
	)
	if s.Dom&starBit > 0 || s.Dow&starBit > 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

func dayOfMonthMatches(field uint64, t time.Time) bool {
	if field&(lastDomFlag|nearestWeekdayDomFlag) == 0 {
		return 1<<uint(t.Day())&field > 0
	}

	lastDay := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()

	if field&lastDomFlag > 0 {
		if field&nearestWeekdayDomFlag > 0 {
			return t.Day() == nearestWeekday(t.Year(), t.Month(), lastDay, t.Location())
		}
		offset := int((field & lastDomOffsetMask) >> lastDomOffsetShift)
		return t.Day() == lastDay-offset
	}

	if field&nearestWeekdayDomFlag > 0 {
		target := 0
		for day := dom.min; day <= dom.max; day++ {
			if field&(1<<day) > 0 {
				target = int(day)
				break
			}
		}
		if target == 0 || target > lastDay {
			return false
		}
		return t.Day() == nearestWeekday(t.Year(), t.Month(), target, t.Location())
	}

	return false
}

func nearestWeekday(year int, month time.Month, day int, loc *time.Location) int {
	date := time.Date(year, month, day, 0, 0, 0, 0, loc)
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()

	switch date.Weekday() {
	case time.Saturday:
		if day == 1 {
			return day + 2
		}
		return day - 1
	case time.Sunday:
		if day == lastDay {
			return day - 2
		}
		return day + 1
	default:
		return day
	}
}

func dayOfWeekMatches(field uint64, t time.Time) bool {
	if 1<<uint(t.Weekday())&field == 0 {
		return false
	}
	if field&nthDowFlag > 0 {
		nth := uint((field & nthDowMask) >> nthDowShift)
		occurrence := uint((t.Day()-1)/7 + 1)
		return occurrence == nth
	}
	if field&lastDowFlag > 0 {
		lastDay := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
		return t.Day()+7 > lastDay
	}
	return true
}
