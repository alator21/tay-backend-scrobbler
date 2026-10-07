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
// remove them. NewPlays finds the smallest such n.
//
// Known blind spot: replaying the song that is already on top leaves the
// list unchanged and cannot be detected.
package diff

import "errors"

// ErrNoMatch means curr cannot be explained as new plays on top of prev,
// e.g. history was cleared or the polling gap was too long.
var ErrNoMatch = errors.New("diff: current snapshot does not extend the previous one")

// NewPlays returns how many entries at the top of curr are new plays since
// prev, and the entries of prev that were removed from the history (not
// counting ones that fell off the end).
func NewPlays(prev, curr []string) (int, []string, error) {
	if len(prev) == 0 {
		return len(curr), nil, nil
	}
	listed := make(map[string]bool, len(curr))
	for _, id := range curr {
		listed[id] = true
	}
	for n := 0; n <= len(curr); n++ {
		if ok, removed := extends(prev, curr, n, listed); ok {
			return n, removed, nil
		}
	}
	return 0, nil, ErrNoMatch
}

// extends reports whether curr is n new plays on top of prev. listed holds
// the IDs in curr.
func extends(prev, curr []string, n int, listed map[string]bool) (bool, []string) {
	head := make(map[string]bool, n)
	for _, id := range curr[:n] {
		head[id] = true
	}
	rest := curr[n:]

	var missing []string
	matched, remaining, removed := 0, 0, 0
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
		if matched == len(rest) {
			continue // a duplicate in prev, already matched
		}
		if rest[matched] != id {
			return false, nil
		}
		matched++
		removed = len(missing)
	}
	// Require real overlap with prev, unless every old entry was replayed.
	if matched > 0 || remaining == 0 {
		return true, missing[:removed]
	}
	return false, nil
}
