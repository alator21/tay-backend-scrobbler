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

// fakeClient returns the next queued history on each call.
type fakeClient struct{ responses [][]ytm.Track }

func (c *fakeClient) History(context.Context) ([]byte, []ytm.Track, error) {
	tracks := c.responses[0]
	c.responses = c.responses[1:]
	return []byte(`{}`), tracks, nil
}

func track(id string) ytm.Track {
	return ytm.Track{VideoID: id, Title: "Song " + id, Artists: []string{"Artist"}, DurationSec: 200, VideoType: "MUSIC_VIDEO_TYPE_ATV"}
}

// history returns n tracks, newest first.
func history(n int) []ytm.Track {
	h := make([]ytm.Track, n)
	for i := range h {
		h[i] = track(fmt.Sprintf("v%03d", i))
	}
	return h
}

func newTestPoller(t *testing.T, responses ...[]ytm.Track) (*Poller, *store.Store) {
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
	p := &Poller{
		cfg:    Config{Dir: dir, Store: st, Alerts: alerts, RawRetention: time.Hour},
		client: &fakeClient{responses: responses},
	}
	return p, st
}

// pollAfter polls as if the previous poll was gap ago.
func pollAfter(t *testing.T, p *Poller, gap time.Duration) {
	t.Helper()
	now := time.Now()
	if p.prev != nil {
		p.prev.PolledAt = now.Add(-gap)
	}
	if err := p.poll(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

// playIDs returns the video IDs of all stored plays.
func playIDs(t *testing.T, st *store.Store) []string {
	t.Helper()
	var ids []string
	for _, s := range []store.Status{store.Playing, store.Pending, store.Sent, store.Skipped, store.Review, store.Rejected, store.Expired} {
		plays, err := st.Plays(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		for _, pl := range plays {
			ids = append(ids, pl.Track.VideoID)
		}
	}
	slices.Sort(ids)
	return ids
}

func eventTypes(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var types []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		types = append(types, e.Type)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return types
}

// takeRaw reports how many raw responses were saved, and deletes them.
// Responses saved within the same second share a name, so tests take them
// after each poll.
func takeRaw(t *testing.T, dir string) int {
	t.Helper()
	raw := filepath.Join(dir, "raw")
	entries, err := os.ReadDir(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(raw, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return len(entries)
}

// A block of old entries dropping out of one response and coming back in
// the next, together with a real play, adds only that play. On 2026-10-07
// it added over a hundred.
func TestEntriesBackAddNoPlays(t *testing.T) {
	full := history(200)
	short := slices.Concat(full[:124], full[129:])
	withNew := slices.Concat([]ytm.Track{track("new")}, full[:199])
	p, st := newTestPoller(t, full, short, withNew)

	pollAfter(t, p, 0) // baseline
	takeRaw(t, p.cfg.Dir)
	for i, step := range []string{"5 removed", "5 back plus a new play"} {
		pollAfter(t, p, 3*time.Minute)
		if got := takeRaw(t, p.cfg.Dir); got != 1 {
			t.Errorf("poll %d (%s): saved %d raw responses, want 1", i+2, step, got)
		}
	}

	if got := playIDs(t, st); !slices.Equal(got, []string{"new"}) {
		t.Errorf("plays = %v, want [new]", got)
	}
}

// A change the diff can only explain as more plays than the poll window
// holds is a resync, not a burst of plays.
func TestImplausiblePlaysResync(t *testing.T) {
	full := history(200)
	// Two old entries swap places: only explainable as 121 new plays.
	swapped := slices.Clone(full)
	swapped[120], swapped[121] = swapped[121], swapped[120]
	p, st := newTestPoller(t, full, swapped)

	pollAfter(t, p, 0)
	pollAfter(t, p, 3*time.Minute)

	if got := playIDs(t, st); got != nil {
		t.Errorf("plays = %v, want none", got)
	}
	if got := eventTypes(t, p.cfg.Dir); !slices.Equal(got, []string{"resync"}) {
		t.Errorf("events = %v, want [resync]", got)
	}
	if p.prev.TopStarted != nil {
		t.Errorf("TopStarted = %v after resync, want nil", p.prev.TopStarted)
	}
}

// Many plays after a long gap, e.g. after downtime, are still plausible.
func TestManyPlaysAfterLongGap(t *testing.T) {
	full := history(200)
	var fresh []ytm.Track
	for i := range 20 {
		fresh = append(fresh, track(fmt.Sprintf("n%02d", i)))
	}
	p, st := newTestPoller(t, full, slices.Concat(fresh, full[:180]))

	pollAfter(t, p, 0)
	pollAfter(t, p, time.Hour)

	if got := playIDs(t, st); len(got) != 20 {
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
	t0 := time.Now()
	for _, tt := range tests {
		w := playtime.Window{After: t0, Before: t0.Add(tt.window)}
		if got := plausible(tt.n, w); got != tt.want {
			t.Errorf("plausible(%d, %s) = %v, want %v", tt.n, tt.window, got, tt.want)
		}
	}
}
