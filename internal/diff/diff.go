// Package diff detects new plays by comparing consecutive snapshots of the
// YouTube Music history (video IDs, newest first).
//
// Replaying a song moves its existing entry to the top instead of adding a
// new one, so after n new plays the current snapshot should be:
//
//	curr = [n new plays] + (prev without those n videos)
//
// possibly truncated at the end. NewPlays finds the smallest such n.
//
// Known blind spot: replaying the song that is already on top leaves the
// list unchanged and cannot be detected.
package diff

import "errors"

// ErrNoMatch means curr cannot be explained as new plays on top of prev,
// e.g. history was edited or the polling gap was too long.
var ErrNoMatch = errors.New("diff: current snapshot does not extend the previous one")

// NewPlays returns how many entries at the top of curr are new plays since prev.
func NewPlays(prev, curr []string) (int, error) {
	if len(prev) == 0 {
		return len(curr), nil
	}
	for n := 0; n <= len(curr); n++ {
		if extends(prev, curr, n) {
			return n, nil
		}
	}
	return 0, ErrNoMatch
}

func extends(prev, curr []string, n int) bool {
	head := make(map[string]bool, n)
	for _, id := range curr[:n] {
		head[id] = true
	}
	rest := curr[n:]

	matched, remaining := 0, 0
	for _, id := range prev {
		if head[id] {
			continue
		}
		remaining++
		if matched == len(rest) {
			continue // curr is truncated; the rest of prev fell off the end
		}
		if rest[matched] != id {
			return false
		}
		matched++
	}
	// Require real overlap with prev, unless every old entry was replayed.
	return matched > 0 || remaining == 0
}
