package send

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/lastfm"
	"github.com/alator21/tay-backend-scrobbler/internal/playtime"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// fake answers each Scrobble call with the next of errs (nil once they run
// out), and ignores scrobbles whose track is in ignore.
type fake struct {
	errs    []error
	ignore  map[string]int
	batches [][]lastfm.Scrobble
}

func (f *fake) Scrobble(_ context.Context, s []lastfm.Scrobble) ([]lastfm.Result, error) {
	f.batches = append(f.batches, s)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	res := make([]lastfm.Result, len(s))
	for i, sc := range s {
		res[i].IgnoredCode = f.ignore[sc.Track]
	}
	return res, nil
}

// setup adds one pending play per age, named "t<i>", started that long before now.
func setup(t *testing.T, ages ...time.Duration) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i, age := range ages {
		tr := ytm.Track{VideoID: fmt.Sprint(i), Title: fmt.Sprintf("t%d", i), Artists: []string{"A"}, DurationSec: 200, VideoType: "MUSIC_VIDEO_TYPE_ATV"}
		start := now.Add(-age)
		w := playtime.Window{After: start.Add(-time.Second), Before: start.Add(time.Second)}
		if err := s.AddPlay(ctx, tr, w, start, start); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EndPlay(ctx, tr, w, start, store.Ending{Verdict: playtime.Scrobble}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func count(t *testing.T, s *store.Store, st store.Status) int {
	t.Helper()
	plays, err := s.Plays(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	return len(plays)
}

func TestSend(t *testing.T) {
	s := setup(t, 15*24*time.Hour, 2*time.Hour, time.Hour, 30*time.Minute, 10*time.Minute)
	f := &fake{ignore: map[string]int{"t3": 1, "t4": lastfm.IgnoredTooOld}}
	snd := &Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }}

	rep, err := snd.Send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (Report{Sent: 2, Rejected: 1, Expired: 2}) {
		t.Errorf("report = %+v", rep)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 4 {
		t.Fatalf("batches = %v", f.batches)
	}
	if b := f.batches[0][0]; b.Track != "t1" || b.Artist != "A" || !b.Timestamp.Equal(now.Add(-2*time.Hour)) || b.DurationSec != 200 {
		t.Errorf("first scrobble = %+v", b)
	}
	if count(t, s, store.Pending) != 0 || count(t, s, store.Sent) != 2 || count(t, s, store.Rejected) != 1 || count(t, s, store.Expired) != 2 {
		t.Error("unexpected statuses after send")
	}

	// Nothing left: no call.
	if _, err := snd.Send(context.Background()); err != nil || len(f.batches) != 1 {
		t.Errorf("second send: %v, %d batches", err, len(f.batches))
	}
}

func TestSendBatches(t *testing.T) {
	ages := make([]time.Duration, 120)
	for i := range ages {
		ages[i] = time.Duration(len(ages)-i) * time.Minute
	}
	s := setup(t, ages...)
	f := &fake{}
	rep, err := (&Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }}).Send(context.Background())
	if err != nil || rep.Sent != 120 {
		t.Fatalf("send: %+v, %v", rep, err)
	}
	if len(f.batches) != 3 || len(f.batches[0]) != 50 || len(f.batches[2]) != 20 {
		t.Errorf("batch sizes wrong: %d batches", len(f.batches))
	}
}

func TestSendRetries(t *testing.T) {
	temp := &lastfm.Error{Code: 16, Message: "temporarily unavailable"}

	s := setup(t, time.Hour)
	f := &fake{errs: []error{temp, temp}}
	snd := &Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }, Retries: []time.Duration{0, 0}}
	if rep, err := snd.Send(context.Background()); err != nil || rep.Sent != 1 || len(f.batches) != 3 {
		t.Fatalf("retried send: %+v, %v, %d calls", rep, err, len(f.batches))
	}

	// Retries run out: error, play stays pending.
	s = setup(t, time.Hour)
	f = &fake{errs: []error{temp, temp, temp}}
	snd = &Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }, Retries: []time.Duration{0, 0}}
	if _, err := snd.Send(context.Background()); !errors.Is(err, temp) {
		t.Fatalf("err = %v", err)
	}
	if count(t, s, store.Pending) != 1 {
		t.Error("play should stay pending")
	}

	// Permanent error: no retry.
	s = setup(t, time.Hour)
	perm := &lastfm.Error{Code: 9, Message: "Invalid session key"}
	f = &fake{errs: []error{perm}}
	snd = &Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }, Retries: []time.Duration{0, 0}}
	if _, err := snd.Send(context.Background()); !errors.Is(err, perm) || len(f.batches) != 1 {
		t.Fatalf("err = %v, %d calls", err, len(f.batches))
	}
}

func TestSendDailyLimit(t *testing.T) {
	ages := make([]time.Duration, 60)
	for i := range ages {
		ages[i] = time.Duration(len(ages)-i) * time.Minute
	}
	s := setup(t, ages...)
	f := &fake{ignore: map[string]int{"t49": lastfm.IgnoredDailyLimit}}
	rep, err := (&Sender{Store: s, Lastfm: f, Now: func() time.Time { return now }}).Send(context.Background())
	if err != nil || !rep.DailyLimit || rep.Sent != 49 || len(f.batches) != 1 {
		t.Fatalf("send: %+v, %v, %d batches", rep, err, len(f.batches))
	}
	if count(t, s, store.Pending) != 11 {
		t.Errorf("want 11 pending, got %d", count(t, s, store.Pending))
	}
}
