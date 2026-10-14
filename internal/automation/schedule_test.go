package automation

import (
	"slices"
	"testing"
	"time"
)

func TestNextTimes(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	anchor := at("2026-11-01 00:00")
	tests := []struct {
		name  string
		sched Schedule
		after string
		want  []string
	}{
		// 00:00 EDT + 90 min is 01:30 EDT; + 180 min is 02:00 EST, because 01:00 to 02:00 repeats.
		{"interval counts real time from the anchor", Schedule{Kind: "every", Minutes: 90}, "2026-11-01 01:00",
			[]string{"2026-11-01 01:30", "2026-11-01 02:00"}},
		{"daily time that a DST change repeats runs once", Schedule{Kind: "daily", Time: "01:30"}, "2026-10-31 12:00",
			[]string{"2026-11-01 01:30", "2026-11-02 01:30", "2026-11-03 01:30"}},
		{"daily time that a DST change skips moves forward", Schedule{Kind: "daily", Time: "02:30"}, "2027-03-13 12:00",
			[]string{"2027-03-14 03:30", "2027-03-15 02:30"}},
		{"weekly on Monday and Friday", Schedule{Kind: "weekly", Time: "09:00", Days: []int{1, 5}}, "2026-10-14 10:00",
			[]string{"2026-10-16 09:00", "2026-10-19 09:00", "2026-10-23 09:00"}},
		{"weekly on Sunday", Schedule{Kind: "weekly", Time: "09:00", Days: []int{7}}, "2026-10-18 09:00",
			[]string{"2026-10-25 09:00"}},
		{"once in the future", Schedule{Kind: "once", At: at("2026-10-20 10:00").UnixMilli()}, "2026-10-14 10:00",
			[]string{"2026-10-20 10:00"}},
		{"once in the past has no time", Schedule{Kind: "once", At: at("2026-10-01 10:00").UnixMilli()}, "2026-10-14 10:00",
			nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.sched.upcoming(at(tt.after), anchor, ny, len(tt.want)+1)
			var gotText []string
			for _, g := range got[:min(len(got), len(tt.want))] {
				gotText = append(gotText, g.In(ny).Format("2006-01-02 15:04"))
			}
			if len(got) < len(tt.want) || !slices.Equal(gotText, tt.want) {
				t.Fatalf("got %v, want %v", gotText, tt.want)
			}
		})
	}
}

func TestLatestSkipsOlderMissedTimes(t *testing.T) {
	start := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	sched := Schedule{Kind: "every", Minutes: 30}
	got := sched.latest(start.Add(30*time.Minute), start.Add(8*time.Hour+10*time.Minute), start, time.UTC)
	if want := start.Add(8 * time.Hour); !got.Equal(want) {
		t.Fatalf("latest = %v, want %v", got, want)
	}
}
