package automation

import (
	"errors"
	"fmt"
	"slices"
	"time"

	// Time zones also on a system without zoneinfo files.
	_ "time/tzdata"
)

// Schedule says when an automation runs, in its time zone. It is the API's Schedule.
type Schedule struct {
	Kind    string `json:"kind"`             // every, daily, weekly, or once
	Minutes int    `json:"minutes,omitzero"` // every
	Time    string `json:"time,omitempty"`   // daily and weekly: HH:MM
	Days    []int  `json:"days,omitempty"`   // weekly: ISO weekdays, 1 is Monday
	At      int64  `json:"at,omitzero"`      // once: Unix milliseconds
}

const (
	minMinutes = 5
	maxMinutes = 7 * 24 * 60
)

// clean returns s with only the fields of its kind, or an error that says what is wrong.
func (s Schedule) clean() (Schedule, error) {
	switch s.Kind {
	case "every":
		if s.Minutes < minMinutes || s.Minutes > maxMinutes {
			return s, fmt.Errorf("the interval must be %d to %d minutes", minMinutes, maxMinutes)
		}
		return Schedule{Kind: s.Kind, Minutes: s.Minutes}, nil
	case "daily", "weekly":
		if _, err := time.Parse("15:04", s.Time); err != nil {
			return s, errors.New("the time must be HH:MM")
		}
		if s.Kind == "daily" {
			return Schedule{Kind: s.Kind, Time: s.Time}, nil
		}
		days := slices.Compact(slices.Sorted(slices.Values(s.Days)))
		if len(days) == 0 || days[0] < 1 || days[len(days)-1] > 7 {
			return s, errors.New("choose at least one day, from 1 (Monday) to 7 (Sunday)")
		}
		return Schedule{Kind: s.Kind, Time: s.Time, Days: days}, nil
	case "once":
		if s.At <= 0 {
			return s, errors.New("choose the time of the run")
		}
		return Schedule{Kind: s.Kind, At: s.At}, nil
	}
	return s, errors.New("the schedule kind must be every, daily, weekly, or once")
}

// next returns the first time of a clean schedule after t, or false when it has none. An interval
// counts from anchor. A daily time that a DST change skips moves forward with it; a time that a DST
// change repeats is one time.
func (s Schedule) next(t, anchor time.Time, loc *time.Location) (time.Time, bool) {
	switch s.Kind {
	case "every":
		period := time.Duration(s.Minutes) * time.Minute
		if t.Before(anchor) {
			return anchor, true
		}
		return anchor.Add((t.Sub(anchor)/period + 1) * period), true
	case "once":
		at := time.UnixMilli(s.At)
		return at, at.After(t)
	}
	clock, _ := time.Parse("15:04", s.Time)
	local := t.In(loc)
	// A weekly schedule has a time within 8 days, as a DST change can move today's time back.
	for i := range 9 {
		d := wallTime(local.Year(), local.Month(), local.Day()+i, clock.Hour(), clock.Minute(), loc)
		if d.After(t) && (s.Kind == "daily" || slices.Contains(s.Days, isoWeekday(d))) {
			return d, true
		}
	}
	return time.Time{}, false
}

// wallTime is time.Date, but a time that a DST change skips moves forward by the gap; time.Date
// does not say which way it moves.
func wallTime(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	d := time.Date(year, month, day, hour, minute, 0, 0, loc)
	if d.Hour() == hour && d.Minute() == minute {
		return d
	}
	// The wall time with the offset from before the change.
	_, offset := d.Add(-12 * time.Hour).Zone()
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Add(-time.Duration(offset) * time.Second).In(loc)
}

func isoWeekday(t time.Time) int {
	if t.Weekday() == time.Sunday {
		return 7
	}
	return int(t.Weekday())
}

// latest returns the newest time of s from due to now; due is a time of s that has come.
func (s Schedule) latest(due, now, anchor time.Time, loc *time.Location) time.Time {
	for {
		next, ok := s.next(due, anchor, loc)
		if !ok || next.After(now) {
			return due
		}
		due = next
	}
}

// upcoming returns at most n times of s after t.
func (s Schedule) upcoming(t, anchor time.Time, loc *time.Location, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		next, ok := s.next(t, anchor, loc)
		if !ok {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}
