// Package diff detects new plays by comparing consecutive snapshots of the
// YouTube Music history (video IDs, newest first).
//
// Replaying a song moves its existing entry to the top instead of adding a
// new one, so after n new plays the current snapshot should be:
//
//	curr = [n new plays] + (prev without those n videos)
//
// possibly truncated at the end, and with entries that disappeared from the
// history left out: YouTube Music occasionally drops an entry, and users can
// remove them. Dropped entries also come back, in their old place: an entry
// below the new plays that prev didn't list is such a reappearance, not a
// play. NewPlays finds the smallest such n.
//
// Known blind spots: replaying the song that is already on top leaves the
// list unchanged and cannot be detected, and an old entry reappearing on top
// of the list looks like a replay of it.
package diff

import "errors"

// ErrNoMatch means curr cannot be explained as new plays on top of prev,
// e.g. history was cleared or the polling gap was too long.
var ErrNoMatch = errors.New("diff: current snapshot does not extend the previous one")

// Result is how curr differs from prev.
type Result struct {
	// New is how many entries at the top of curr are new plays.
	New int
	// Removed are the entries of prev missing from curr, not counting ones
	// that fell off the end.
	Removed []string
	// Reappeared are the entries of curr below the new plays that prev
	// didn't list, not counting ones that came into view at the end.
	Reappeared []string
}

// NewPlays works out the new plays in curr since prev.
func NewPlays(prev, curr []string) (Result, error) {
	if len(prev) == 0 {
		return Result{New: len(curr)}, nil
	}
	inPrev := make(map[string]bool, len(prev))
	for _, id := range prev {
		inPrev[id] = true
	}
	listed := make(map[string]bool, len(curr))
	for _, id := range curr {
		listed[id] = true
	}
	for n := 0; n <= len(curr); n++ {
		if r, ok := extends(prev, curr, n, inPrev, listed); ok {
			return r, nil
		}
	}
	return Result{}, ErrNoMatch
}

// extends reports whether curr is n new plays on top of prev. inPrev and
// listed hold the IDs in prev and curr.
func extends(prev, curr []string, n int, inPrev, listed map[string]bool) (Result, bool) {
	head := make(map[string]bool, n)
	for _, id := range curr[:n] {
		head[id] = true
	}
	rest := curr[n:]

	var missing, reappeared []string
	pos, matched, remaining, removed := 0, 0, 0, 0
	for _, id := range prev {
		if head[id] {
			continue
		}
		remaining++
		if !listed[id] {
			// Removed from the history, or fell off the end; which one is
			// only known once a later entry matches.
			missing = append(missing, id)
			continue
		}
		// Below the first match, entries prev didn't list are old ones
		// coming back. Above it they can only be new plays, which belong in
		// the head.
		for matched > 0 && pos < len(rest) && !inPrev[rest[pos]] {
			reappeared = append(reappeared, rest[pos])
			pos++
		}
		if pos == len(rest) {
			continue // a duplicate in prev, already matched
		}
		if rest[pos] != id {
			return Result{}, false
		}
		pos++
		matched++
		removed = len(missing)
	}
	// Require real overlap with prev, unless every old entry was replayed.
	if matched > 0 || remaining == 0 {
		return Result{New: n, Removed: missing[:removed], Reappeared: reappeared}, true
	}
	return Result{}, false
}
