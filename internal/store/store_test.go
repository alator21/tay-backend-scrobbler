package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/meta"
	"github.com/alator21/tay-backend-scrobbler/internal/playtime"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

var (
	t0       = time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	careful  = ytm.Track{VideoID: "a", Title: "Careful", Artists: []string{"Paramore"}, Album: "Brand New Eyes", DurationSec: 231, VideoType: "MUSIC_VIDEO_TYPE_ATV"}
	upload   = ytm.Track{VideoID: "b", Title: "Paramore - Temporary (Lyrics)", Artists: []string{"Some Uploader"}, DurationSec: 204, VideoType: "MUSIC_VIDEO_TYPE_UGC"}
	untitled = ytm.Track{VideoID: "c", Title: "my favourite song", Artists: []string{"Channel"}, DurationSec: 200, VideoType: "MUSIC_VIDEO_TYPE_UGC"}
)

func win(after, before time.Duration) playtime.Window {
	return playtime.Window{After: t0.Add(after), Before: t0.Add(before)}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func statuses(t *testing.T, s *Store) map[Status][]string {
	t.Helper()
	got := map[Status][]string{}
	for _, st := range []Status{Playing, Pending, Sent, Skipped, Review, Rejected, Expired} {
		plays, err := s.Plays(context.Background(), st)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range plays {
			got[st] = append(got[st], p.Track.VideoID)
		}
	}
	return got
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name    string
		verdict playtime.Verdict
		cleaned bool
		policy  Policy
		want    Status
	}{
		{"scrobble", playtime.Scrobble, true, Policy{}, Pending},
		{"unsure is scrobbled", playtime.Unsure, true, Policy{}, Pending},
		{"session end is scrobbled", SessionEnd, true, Policy{}, Pending},
		{"skip", playtime.Skip, true, Policy{}, Skipped},
		{"skip wins over bad metadata", playtime.Skip, false, Policy{}, Skipped},
		{"bad metadata", playtime.Scrobble, false, Policy{}, Review},
		{"unsure skipped by policy", playtime.Unsure, true, Policy{SkipUnsure: true}, Skipped},
		{"session end kept despite policy", SessionEnd, true, Policy{SkipUnsure: true}, Pending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.status(Ending{Verdict: tt.verdict}, tt.cleaned); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPlayLifecycle(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	started := win(0, 30*time.Second)
	if err := s.AddPlay(ctx, careful, started, t0.Add(10*time.Second), t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Re-detected after a crash: same start window, later detection.
	if err := s.AddPlay(ctx, careful, playtime.Window{After: started.After, Before: t0.Add(time.Minute)}, time.Time{}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := statuses(t, s); len(got[Playing]) != 1 {
		t.Fatalf("want one playing play, got %v", got)
	}

	end := Ending{Verdict: playtime.Unsure, Min: 100 * time.Second, Max: 200 * time.Second}
	if _, err := s.EndPlay(ctx, careful, started, t0.Add(4*time.Minute), end); err != nil {
		t.Fatal(err)
	}
	plays, err := s.Plays(ctx, Pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(plays) != 1 {
		t.Fatalf("want 1 pending play, got %d", len(plays))
	}
	p := plays[0]
	if p.Track.Title != "Careful" || p.Scrobble != (meta.Scrobble{Artist: "Paramore", Track: "Careful", Album: "Brand New Eyes"}) {
		t.Errorf("unexpected track/scrobble: %+v %+v", p.Track, p.Scrobble)
	}
	if !p.StartedAt.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("startedAt = %v", p.StartedAt)
	}
	if !p.Started.After.Equal(started.After) || !p.Started.Before.Equal(started.Before) {
		t.Errorf("started = %+v, want %+v", p.Started, started)
	}
	if p.MinSec == nil || *p.MinSec != 100 || p.MaxSec == nil || *p.MaxSec != 200 {
		t.Errorf("bounds = %v, %v", p.MinSec, p.MaxSec)
	}
	if p.Verdict != playtime.Unsure || !p.Inferred {
		t.Errorf("verdict = %s, inferred = %v", p.Verdict, p.Inferred)
	}

	// Ending it again doesn't change the status it already has.
	if st, err := s.EndPlay(ctx, careful, started, t0.Add(5*time.Minute), Ending{Verdict: playtime.Skip}); err != nil || st != "" {
		t.Fatalf("second end: %q, %v", st, err)
	}
	if got := statuses(t, s); len(got[Pending]) != 1 || len(got[Skipped]) != 0 {
		t.Errorf("status changed after second end: %v", got)
	}
}

func TestEndPlayStatuses(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// Not added first: EndPlay adds it.
	if _, err := s.EndPlay(ctx, upload, win(0, 30*time.Second), t0, Ending{Verdict: SessionEnd}); err != nil {
		t.Fatal(err)
	}
	if st, err := s.EndPlay(ctx, untitled, win(time.Minute, 90*time.Second), t0, Ending{Verdict: playtime.Scrobble, Min: 3 * time.Minute, Max: 4 * time.Minute}); err != nil || st != Review {
		t.Fatalf("end untitled: %q, %v", st, err)
	}
	if _, err := s.EndPlay(ctx, careful, win(2*time.Minute, 150*time.Second), t0, Ending{Verdict: playtime.Skip, Min: 10 * time.Second, Max: 40 * time.Second}); err != nil {
		t.Fatal(err)
	}

	got := statuses(t, s)
	if len(got[Pending]) != 1 || got[Pending][0] != "b" || len(got[Review]) != 1 || got[Review][0] != "c" ||
		len(got[Skipped]) != 1 || got[Skipped][0] != "a" {
		t.Fatalf("unexpected statuses: %v", got)
	}

	plays, _ := s.Plays(ctx, Pending)
	p := plays[0]
	if p.Scrobble != (meta.Scrobble{Artist: "Paramore", Track: "Temporary", Parsed: true}) {
		t.Errorf("scrobble = %+v", p.Scrobble)
	}
	if p.MinSec != nil || p.MaxSec != nil || p.Verdict != SessionEnd || !p.Inferred {
		t.Errorf("session end stored as %v %v %s %v", p.MinSec, p.MaxSec, p.Verdict, p.Inferred)
	}
}

func TestMarkSentAndFailed(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, tr := range []ytm.Track{careful, upload} {
		w := win(time.Duration(i)*time.Minute, time.Duration(i)*time.Minute+30*time.Second)
		if _, err := s.EndPlay(ctx, tr, w, t0, Ending{Verdict: SessionEnd}); err != nil {
			t.Fatal(err)
		}
	}
	plays, _ := s.Plays(ctx, Pending)
	if len(plays) != 2 || plays[0].Track.VideoID != "a" {
		t.Fatalf("pending = %+v", plays)
	}
	// Added by EndPlay without an estimate: midpoint of the window.
	if !plays[0].StartedAt.Equal(t0.Add(15 * time.Second)) {
		t.Errorf("startedAt = %v, want midpoint", plays[0].StartedAt)
	}

	if err := s.MarkSent(ctx, plays[0].ID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, plays[1].ID, Rejected, "Artist was ignored"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent(ctx, plays[1].ID, t0); err == nil {
		t.Error("marking a non-pending play should fail")
	}

	sent, _ := s.Plays(ctx, Sent)
	if len(sent) != 1 || sent[0].SentAt == nil || !sent[0].SentAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("sent = %+v", sent)
	}
	rejected, _ := s.Plays(ctx, Rejected)
	if len(rejected) != 1 || rejected[0].Error != "Artist was ignored" || rejected[0].SentAt != nil {
		t.Errorf("rejected = %+v", rejected)
	}
}

// A database created before the send columns existed is migrated in place.
func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Version 1 databases were created without setting user_version.
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plays (video_id, yt_track, artist, track, started_after, started_before, detected_at, status)
		VALUES ('a', '{"videoId":"a"}', 'Paramore', 'Careful', 1000, 3000, 3000, 'pending')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plays, err := s.Plays(context.Background(), Pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(plays) != 1 || plays[0].StartedAt.UnixMilli() != 2000 {
		t.Fatalf("migrated plays = %+v", plays)
	}
	// Opening again doesn't re-run migrations.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

// A fresh install has no data directory yet.
func TestOpenCreatesDirectory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data", "scrobbler.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
