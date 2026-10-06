package playtime

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func TestPlayed(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Window
		min, max time.Duration
	}{
		{
			name: "consecutive windows",
			a:    Window{at(0), at(30)},
			b:    Window{at(180), at(210)},
			min:  150 * time.Second, max: 210 * time.Second,
		},
		{
			name: "same window",
			a:    Window{at(0), at(180)},
			b:    Window{at(0), at(180)},
			min:  0, max: 180 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max := Played(tt.a, tt.b)
			if min != tt.min || max != tt.max {
				t.Errorf("Played = %s..%s, want %s..%s", min, max, tt.min, tt.max)
			}
		})
	}
}

func TestJudge(t *testing.T) {
	s := time.Second
	tests := []struct {
		name     string
		duration int
		min, max time.Duration
		want     Verdict
	}{
		{name: "played through", duration: 209, min: 200 * s, max: 240 * s, want: Scrobble},
		{name: "skipped early", duration: 154, min: 0, max: 45 * s, want: Skip},
		{name: "bounds straddle half", duration: 154, min: 0, max: 180 * s, want: Unsure},
		{name: "exactly half", duration: 154, min: 77 * s, max: 90 * s, want: Scrobble},
		{name: "long track needs only 4 min", duration: 600, min: 240 * s, max: 300 * s, want: Scrobble},
		{name: "track too short", duration: 30, min: 30 * s, max: 60 * s, want: Skip},
		{name: "unknown length", duration: 0, min: 300 * s, max: 400 * s, want: Unsure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Judge(tt.duration, tt.min, tt.max); got != tt.want {
				t.Errorf("Judge = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStarts(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	w := Window{After: t0, Before: t0.Add(30 * time.Second)}
	if got := Starts(w, 1); !got[0].Equal(t0.Add(15 * time.Second)) {
		t.Errorf("single play: %v, want midpoint", got)
	}
	got := Starts(Window{After: t0, Before: t0.Add(3 * time.Minute)}, 2)
	if !got[0].Equal(t0.Add(time.Minute)) || !got[1].Equal(t0.Add(2*time.Minute)) {
		t.Errorf("two plays: %v", got)
	}
}
