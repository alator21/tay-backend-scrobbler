// Package playtime infers when plays started and how long they ran, using
// only the poll times at which they appeared in the history.
//
// A history entry is added when a song starts (or within a few seconds of
// it), so a play first seen at one poll started between the previous poll and
// that one. A play ended at the latest when the next play started, so the
// time between two start windows bounds how long the earlier one ran.
package playtime

import "time"

// Window is the interval a play started in: after the previous poll, and no
// later than the poll that first listed it.
type Window struct {
	After  time.Time `json:"after"`
	Before time.Time `json:"before"`
}

// Played bounds the time from the start of a play in a to the start of the
// next play in b. That is how long the first play ran, assuming playback was
// continuous; if it was paused or stopped in between, it ran for less.
func Played(a, b Window) (min, max time.Duration) {
	min = b.After.Sub(a.Before)
	if min < 0 {
		min = 0
	}
	return min, b.Before.Sub(a.After)
}

// Verdict is whether a play counts as a scrobble under Last.fm's rule: the
// track is longer than 30 s and was played for at least half its length or
// 4 minutes, whichever comes first.
type Verdict string

const (
	Scrobble Verdict = "scrobble" // even the shortest possible play meets the rule
	Skip     Verdict = "skip"     // even the longest possible play falls short
	Unsure   Verdict = "unsure"   // the bounds straddle the threshold, or the length is unknown
)

// Judge applies the Last.fm rule to a play of a track of the given length
// that ran between min and max.
func Judge(durationSec int, min, max time.Duration) Verdict {
	if durationSec <= 0 {
		return Unsure
	}
	length := time.Duration(durationSec) * time.Second
	if length <= 30*time.Second {
		return Skip
	}
	need := length / 2
	if need > 4*time.Minute {
		need = 4 * time.Minute
	}
	switch {
	case min >= need:
		return Scrobble
	case max < need:
		return Skip
	default:
		return Unsure
	}
}

// Starts estimates when n plays that all started in w began, oldest first.
// Which part of the window each one used is unknown, so they are spread
// evenly across it, which keeps them in order; a single play gets the
// midpoint.
func Starts(w Window, n int) []time.Time {
	ts := make([]time.Time, n)
	step := w.Before.Sub(w.After) / time.Duration(n+1)
	for i := range ts {
		ts[i] = w.After.Add(step * time.Duration(i+1))
	}
	return ts
}
