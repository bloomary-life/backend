package crosspost

import (
	"sort"
	"time"
)

// PostingWindow is one weekday's best hours in the audience's local time.
type PostingWindow struct {
	Weekday   time.Weekday
	StartHour int
	EndHour   int
}

// WindowSource is where a network's windows come from (rollout.md "Best-time rows").
type WindowSource struct {
	URL            string
	Publisher      string
	Published      string
	DataPeriod     string
	Sample         string
	Metric         string
	LocalTimeBasis string
}

type NetworkWindows struct {
	Source  WindowSource
	Windows []PostingWindow
}

// PostingWindows: not LinkedIn (it keeps schedule_next.go). No row = post now, development only;
// every row is sourced before prod (Kat 2026-11-03).
var PostingWindows = map[Network]NetworkWindows{}

// NextPostingTime is the earliest moment in the network's windows in the audience zone, on a day with no other
// delivery of this member to this network (taken); when the week's windows are used, the next window's start.
// A moment inside today's open window is now.
func NextPostingTime(network Network, now time.Time, audience *time.Location, taken []time.Time) time.Time {
	windows, ok := PostingWindows[network]
	if !ok || len(windows.Windows) == 0 {
		return now
	}
	return nextInWindows(windows.Windows, now, audience, taken)
}

func nextInWindows(windows []PostingWindow, now time.Time, audience *time.Location, taken []time.Time) time.Time {
	if audience == nil {
		audience = time.UTC
	}
	sorted := append([]PostingWindow(nil), windows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartHour < sorted[j].StartHour })
	local := now.In(audience)
	busy := map[string]bool{}
	for _, t := range taken {
		busy[t.In(audience).Format(time.DateOnly)] = true
	}
	var fallback time.Time
	for day := 0; day <= 7; day++ {
		date := time.Date(local.Year(), local.Month(), local.Day()+day, 0, 0, 0, 0, audience)
		for _, w := range sorted {
			if w.Weekday != date.Weekday() {
				continue
			}
			start := time.Date(date.Year(), date.Month(), date.Day(), w.StartHour, 0, 0, 0, audience)
			end := time.Date(date.Year(), date.Month(), date.Day(), w.EndHour, 0, 0, 0, audience)
			if !now.Before(end) {
				continue
			}
			at := start
			if now.After(start) {
				at = now
			}
			if !busy[date.Format(time.DateOnly)] {
				return at
			}
			if fallback.IsZero() && day > 0 {
				fallback = at
			}
		}
	}
	if fallback.IsZero() {
		return now
	}
	return fallback
}
