package poller

// Scenarios replaying how YouTube Music's history behaved in production,
// through the poller, on a fake clock. Song names are made up; positions,
// counts and timings are the observed ones.

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/store"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

// historyLimit is how many entries the history response lists.
const historyLimit = 200

// fakeYTM is a listening history that behaves like YouTube Music's:
//   - playing a song puts it on top, moving its entry if it was listed;
//   - responses list the newest historyLimit entries;
//   - entries can be left out of a single response (it then lists fewer),
//     or of every response for a while (later entries move up to fill in).
type fakeYTM struct {
	entries  []ytm.Track // newest first, including ones past the limit
	absent   map[string]bool
	dropNext map[string]bool
}

// newFakeYTM returns a history of n songs nobody plays during the test.
func newFakeYTM(n int) *fakeYTM {
	y := &fakeYTM{absent: map[string]bool{}, dropNext: map[string]bool{}}
	for i := range n {
		y.entries = append(y.entries, song(fmt.Sprintf("old%03d", i)))
	}
	return y
}

func song(id string) ytm.Track {
	return ytm.Track{VideoID: id, Title: "Song " + id, Artists: []string{"Artist " + id}, DurationSec: 200, VideoType: "MUSIC_VIDEO_TYPE_ATV"}
}

func (y *fakeYTM) History(context.Context) ([]byte, []ytm.Track, error) {
	var listed []ytm.Track
	for _, t := range y.entries {
		if !y.absent[t.VideoID] {
			listed = append(listed, t)
		}
	}
	listed = listed[:min(len(listed), historyLimit)]
	listed = slices.DeleteFunc(slices.Clone(listed), func(t ytm.Track) bool { return y.dropNext[t.VideoID] })
	clear(y.dropNext)
	return []byte(`{}`), listed, nil
}

// play starts the song id, a new one or a replay.
func (y *fakeYTM) play(id string) {
	t := song(id)
	if i := slices.IndexFunc(y.entries, func(t ytm.Track) bool { return t.VideoID == id }); i >= 0 {
		t = y.entries[i]
		y.entries = slices.Delete(y.entries, i, i+1)
	}
	y.entries = slices.Insert(y.entries, 0, t)
}

// at returns the ID listed at position i (0 = newest) of the next response.
func (y *fakeYTM) at(i int) string {
	_, listed, _ := y.History(context.Background())
	return listed[i].VideoID
}

// action is something done to the history at a time into a run.
type action struct {
	at time.Duration
	do func()
}

// run polls like Poller.Run, at its adaptive intervals, for d, doing each
// action (in order) before the first poll at or after its time.
func (r *testRun) run(d time.Duration, actions ...action) {
	r.t.Helper()
	start := r.now
	for {
		next := r.now.Add(r.p.nextInterval(r.now))
		if next.After(start.Add(d)) {
			return
		}
		for len(actions) > 0 && !start.Add(actions[0].at).After(next) {
			actions[0].do()
			actions = actions[1:]
		}
		r.pollAfter(next.Sub(r.now))
	}
}

// listening plays songs back to back, each for its full 200 s, starting at
// offset from the start of a run.
func listening(y *fakeYTM, offset time.Duration, ids ...string) []action {
	var as []action
	for i, id := range ids {
		as = append(as, action{offset + time.Duration(i)*205*time.Second, func() { y.play(id) }})
	}
	return as
}

func checkPlays(t *testing.T, r *testRun, want map[store.Status][]string) {
	t.Helper()
	got := r.plays()
	for _, s := range []store.Status{store.Playing, store.Pending, store.Skipped, store.Review} {
		if !slices.Equal(got[s], want[s]) {
			t.Errorf("%s plays = %v, want %v", s, got[s], want[s])
		}
	}
}

// Normal listening: replays of songs further down the history and new
// songs, back to back (2026-10-07, 12:01-12:41). Each is scrobbled once,
// the last one when the session ends.
func TestYTMListening(t *testing.T) {
	y := newFakeYTM(300)
	r := newTestRun(t, y)
	r.pollAfter(0)
	songs := []string{y.at(40), y.at(85), "new1", y.at(12), y.at(150), y.at(7)}

	r.run(30*time.Minute, listening(y, time.Minute, songs...)...)

	checkPlays(t, r, map[store.Status][]string{store.Pending: songs})
}

// While a song plays, one response leaves out the entries at positions
// 116-119 and 121; the next lists them again (2026-10-07 12:32:41). This
// made 122 old entries count as new plays.
func TestYTMDropsEntriesForOnePollWhileListening(t *testing.T) {
	y := newFakeYTM(300)
	r := newTestRun(t, y)
	r.pollAfter(0)
	songs := []string{y.at(30), y.at(60)}
	glitch := action{5 * time.Minute, func() {
		for _, i := range []int{116, 117, 118, 119, 121} {
			y.dropNext[y.at(i)] = true
		}
	}}

	r.run(20*time.Minute, append(listening(y, time.Minute, songs...), glitch)...)

	checkPlays(t, r, map[store.Status][]string{store.Pending: songs})
}

// While idle, one response leaves out positions 70-74, and a later one
// 122-126; the responses after list them again (2026-10-07 15:54:29 and
// 16:00:05). These made 75 and 127 old entries count as new plays.
func TestYTMDropsEntriesForOnePollWhileIdle(t *testing.T) {
	y := newFakeYTM(300)
	r := newTestRun(t, y)
	r.pollAfter(0)
	drop := func(from, to int) func() {
		return func() {
			for i := from; i <= to; i++ {
				y.dropNext[y.at(i)] = true
			}
		}
	}

	r.run(30*time.Minute, action{10 * time.Minute, drop(70, 74)}, action{16 * time.Minute, drop(122, 126)})

	checkPlays(t, r, nil)
	if got := r.eventTypes(); got != nil {
		t.Errorf("events = %v, want none", got)
	}
}

// An old entry is missing from every response for hours, then listed again
// at position 105 in the same response as a replay (2026-10-07, back at
// 19:07:59). This made 106 plays out of one.
func TestYTMEntryAbsentForHours(t *testing.T) {
	y := newFakeYTM(300)
	r := newTestRun(t, y)
	r.pollAfter(0)
	missing, replay := y.at(105), y.at(30)

	r.run(7*time.Hour,
		action{time.Minute, func() { y.absent[missing] = true }},
		action{6*time.Hour + 30*time.Minute, func() {
			delete(y.absent, missing)
			y.play(replay)
		}},
	)

	checkPlays(t, r, map[store.Status][]string{store.Pending: {replay}})
}

// Raw responses are saved when entries are left out and when they are
// listed again, so the glitch can be looked at.
func TestYTMGlitchSavesRaw(t *testing.T) {
	y := newFakeYTM(300)
	r := newTestRun(t, y)
	r.pollAfter(0)
	r.takeRaw()

	y.dropNext[y.at(50)] = true
	r.pollAfter(3 * time.Minute)
	if got := r.takeRaw(); got != 1 {
		t.Errorf("entry left out: saved %d raw responses, want 1", got)
	}
	r.pollAfter(3 * time.Minute)
	if got := r.takeRaw(); got != 1 {
		t.Errorf("entry back: saved %d raw responses, want 1", got)
	}
	r.pollAfter(3 * time.Minute)
	if got := r.takeRaw(); got != 0 {
		t.Errorf("no change: saved %d raw responses, want 0", got)
	}
}
