package poller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/alert"
	"github.com/alator21/tay-backend-scrobbler/internal/playtime"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

// testRun drives a Poller against a fake history, on a fake clock.
type testRun struct {
	t   *testing.T
	p   *Poller
	st  *store.Store
	now time.Time
}

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func newTestRun(t *testing.T, client historyClient) *testRun {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	alerts, err := alert.New(nil, filepath.Join(dir, "alerts.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := &testRun{t: t, st: st, now: t0}
	r.p = &Poller{
		cfg:    Config{Dir: dir, Store: st, Alerts: alerts, Interval: 30 * time.Second, IdleInterval: 3 * time.Minute, RawRetention: time.Hour},
		client: client,
		now:    func() time.Time { return r.now },
	}
	return r
}

// pollAfter polls gap after the previous poll.
func (r *testRun) pollAfter(gap time.Duration) {
	r.t.Helper()
	r.now = r.now.Add(gap)
	if err := r.p.poll(context.Background(), r.now); err != nil {
		r.t.Fatal(err)
	}
}

// plays returns the video IDs of the stored plays by status, oldest first.
func (r *testRun) plays() map[store.Status][]string {
	r.t.Helper()
	got := map[store.Status][]string{}
	for _, s := range []store.Status{store.Playing, store.Pending, store.Sent, store.Skipped, store.Review, store.Rejected, store.Expired} {
		plays, err := r.st.Plays(context.Background(), s)
		if err != nil {
			r.t.Fatal(err)
		}
		for _, pl := range plays {
			got[s] = append(got[s], pl.Track.VideoID)
		}
	}
	return got
}

// allPlays returns the video IDs of all stored plays, sorted.
func (r *testRun) allPlays() []string {
	var ids []string
	for _, s := range r.plays() {
		ids = append(ids, s...)
	}
	slices.Sort(ids)
	return ids
}

func (r *testRun) eventTypes() []string {
	r.t.Helper()
	f, err := os.Open(filepath.Join(r.p.cfg.Dir, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	var types []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			r.t.Fatal(err)
		}
		types = append(types, e.Type)
	}
	if err := sc.Err(); err != nil {
		r.t.Fatal(err)
	}
	return types
}

// takeRaw reports how many raw responses were saved, and deletes them.
// Responses saved within the same second share a name, so tests take them
// after each poll.
func (r *testRun) takeRaw() int {
	r.t.Helper()
	raw := filepath.Join(r.p.cfg.Dir, "raw")
	entries, err := os.ReadDir(raw)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(raw, e.Name())); err != nil {
			r.t.Fatal(err)
		}
	}
	return len(entries)
}

// queue returns the queued histories in turn.
type queue struct{ responses [][]ytm.Track }

func (q *queue) History(context.Context) ([]byte, []ytm.Track, error) {
	tracks := q.responses[0]
	q.responses = q.responses[1:]
	return []byte(`{}`), tracks, nil
}

func track(id string) ytm.Track {
	return ytm.Track{VideoID: id, Title: "Song " + id, Artists: []string{"Artist"}, DurationSec: 200, VideoType: "MUSIC_VIDEO_TYPE_ATV"}
}

// tracks returns n tracks, newest first.
func tracks(n int) []ytm.Track {
	h := make([]ytm.Track, n)
	for i := range h {
		h[i] = track(fmt.Sprintf("v%03d", i))
	}
	return h
}

// A change the diff can only explain as more plays than the poll window
// holds is a resync, not a burst of plays.
func TestImplausiblePlaysResync(t *testing.T) {
	full := tracks(200)
	// Two old entries swap places: only explainable as 121 new plays.
	swapped := slices.Clone(full)
	swapped[120], swapped[121] = swapped[121], swapped[120]
	r := newTestRun(t, &queue{[][]ytm.Track{full, swapped}})

	r.pollAfter(0)
	r.pollAfter(3 * time.Minute)

	if got := r.allPlays(); got != nil {
		t.Errorf("plays = %v, want none", got)
	}
	if got := r.eventTypes(); !slices.Equal(got, []string{"resync"}) {
		t.Errorf("events = %v, want [resync]", got)
	}
	if r.p.prev.TopStarted != nil {
		t.Errorf("TopStarted = %v after resync, want nil", r.p.prev.TopStarted)
	}
}

// Many plays after a long gap, e.g. after downtime, are still plausible.
func TestManyPlaysAfterLongGap(t *testing.T) {
	full := tracks(200)
	var fresh []ytm.Track
	for i := range 20 {
		fresh = append(fresh, track(fmt.Sprintf("n%02d", i)))
	}
	r := newTestRun(t, &queue{[][]ytm.Track{full, slices.Concat(fresh, full[:180])}})

	r.pollAfter(0)
	r.pollAfter(time.Hour)

	if got := r.allPlays(); len(got) != 20 {
		t.Errorf("got %d plays, want 20", len(got))
	}
}

func TestPlausible(t *testing.T) {
	tests := []struct {
		n      int
		window time.Duration
		want   bool
	}{
		{1, 0, true},
		{4, 30 * time.Second, true},
		{5, 30 * time.Second, false},
		{19, 3 * time.Minute, true},
		{106, 3 * time.Minute, false},
		{122, 30 * time.Second, false},
		{100, time.Hour, true},
	}
	for _, tt := range tests {
		w := playtime.Window{After: t0, Before: t0.Add(tt.window)}
		if got := plausible(tt.n, w); got != tt.want {
			t.Errorf("plausible(%d, %s) = %v, want %v", tt.n, tt.window, got, tt.want)
		}
	}
}
