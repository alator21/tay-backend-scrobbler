// Package store keeps detected plays in SQLite, with the status that decides
// whether they get scrobbled.
//
// A play is added when it shows up in the history (status playing) and gets
// its final status once it ends: skipped if it ran too short, review if its
// metadata couldn't be cleaned, pending otherwise. Plays whose length could
// only be guessed (an unsure verdict, or a session end) are scrobbled anyway
// but flagged as inferred. Sending a pending play makes it sent, or rejected
// or expired if Last.fm won't take it.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/alator21/tay-backend-scrobbler/internal/meta"
	"github.com/alator21/tay-backend-scrobbler/internal/playtime"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

// Status is where a play is in the scrobbling pipeline.
type Status string

const (
	Playing  Status = "playing"  // detected, not ended yet
	Pending  Status = "pending"  // to be scrobbled
	Sent     Status = "sent"     // scrobbled
	Skipped  Status = "skipped"  // ran too short to count
	Review   Status = "review"   // metadata needs a manual fix before scrobbling
	Rejected Status = "rejected" // Last.fm ignored it (see Error)
	Expired  Status = "expired"  // too old for Last.fm to accept
)

// SessionEnd is the verdict for the last play of a session: nothing came
// after it, so how long it ran is unknown and it is assumed to have played
// through.
const SessionEnd playtime.Verdict = "session-end"

// Play is one detected play.
type Play struct {
	ID         int64
	Track      ytm.Track
	Scrobble   meta.Scrobble // zero if the metadata couldn't be cleaned
	Started    playtime.Window
	StartedAt  time.Time // estimate within Started; the scrobble timestamp
	DetectedAt time.Time
	// MinSec, MaxSec and Verdict are set once the play has ended. The bounds
	// stay nil for a session end.
	MinSec, MaxSec *int
	Verdict        playtime.Verdict
	Status         Status
	Inferred       bool
	SentAt         *time.Time
	Error          string // why it was rejected or expired
}

// Ending is how a play ended.
type Ending struct {
	Verdict  playtime.Verdict
	Min, Max time.Duration // unused for SessionEnd
}

// status is the status a play gets once it has ended.
func status(e Ending, cleaned bool) Status {
	switch {
	case e.Verdict == playtime.Skip:
		return Skipped
	case !cleaned:
		return Review
	default:
		return Pending
	}
}

// migrations[i] brings the schema from version i to i+1 (PRAGMA user_version).
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS plays (
		id             INTEGER PRIMARY KEY,
		video_id       TEXT    NOT NULL,
		yt_track       TEXT    NOT NULL, -- ytm.Track as JSON
		artist         TEXT,             -- cleaned metadata, NULL if cleanup failed
		track          TEXT,
		album          TEXT,
		parsed         INTEGER NOT NULL DEFAULT 0,
		started_after  INTEGER NOT NULL, -- unix ms
		started_before INTEGER NOT NULL,
		detected_at    INTEGER NOT NULL,
		min_sec        INTEGER,
		max_sec        INTEGER,
		verdict        TEXT,
		status         TEXT    NOT NULL,
		inferred       INTEGER NOT NULL DEFAULT 0,
		-- A play is identified by its video and the poll before it appeared, so
		-- re-detecting it after a crash doesn't add it twice.
		UNIQUE (video_id, started_after)
	);
	CREATE INDEX IF NOT EXISTS plays_status ON plays (status);`,

	`ALTER TABLE plays ADD COLUMN started_at INTEGER; -- unix ms, estimate within the window
	UPDATE plays SET started_at = (started_after + started_before) / 2;
	ALTER TABLE plays ADD COLUMN sent_at INTEGER;
	ALTER TABLE plays ADD COLUMN error TEXT;`,
}

// Store is the plays database.
type Store struct {
	db *sql.DB
}

// Open opens or creates the database at path, creating its directory if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for v := version; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("to version %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// AddPlay records a newly detected play that started in the window started,
// at startedAt by estimate (the window's midpoint if zero). Adding the same
// play again (same video and start window) does nothing.
func (s *Store) AddPlay(ctx context.Context, t ytm.Track, started playtime.Window, startedAt, detectedAt time.Time) error {
	if startedAt.IsZero() {
		startedAt = playtime.Starts(started, 1)[0]
	}
	yt, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var artist, track, album sql.NullString
	sc, cleaned := meta.Clean(t)
	if cleaned {
		artist, track = nullString(sc.Artist), nullString(sc.Track)
		album = nullString(sc.Album)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO plays (video_id, yt_track, artist, track, album, parsed,
			started_after, started_before, started_at, detected_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (video_id, started_after) DO NOTHING`,
		t.VideoID, yt, artist, track, album, sc.Parsed,
		started.After.UnixMilli(), started.Before.UnixMilli(), startedAt.UnixMilli(), detectedAt.UnixMilli(), Playing)
	if err != nil {
		return fmt.Errorf("store: add play: %w", err)
	}
	return nil
}

// EndPlay records how a play ended and sets its final status, which it
// returns; empty if the play had already ended. The play is added first if it
// isn't there yet, e.g. when it was detected before the store existed.
func (s *Store) EndPlay(ctx context.Context, t ytm.Track, started playtime.Window, detectedAt time.Time, e Ending) (Status, error) {
	if err := s.AddPlay(ctx, t, started, time.Time{}, detectedAt); err != nil {
		return "", err
	}
	var min, max sql.NullInt64
	if e.Verdict != SessionEnd {
		min = sql.NullInt64{Int64: int64(e.Min.Seconds()), Valid: true}
		max = sql.NullInt64{Int64: int64(e.Max.Seconds()), Valid: true}
	}
	_, cleaned := meta.Clean(t)
	inferred := e.Verdict == playtime.Unsure || e.Verdict == SessionEnd
	// Only a play still marked playing is updated, so a late duplicate
	// can't undo a status that was already acted on.
	st := status(e, cleaned)
	res, err := s.db.ExecContext(ctx, `
		UPDATE plays SET min_sec = ?, max_sec = ?, verdict = ?, status = ?, inferred = ?
		WHERE video_id = ? AND started_after = ? AND status = ?`,
		min, max, e.Verdict, st, inferred,
		t.VideoID, started.After.UnixMilli(), Playing)
	if err != nil {
		return "", fmt.Errorf("store: end play: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", nil
	}
	return st, nil
}

// MarkSent records that a pending play was scrobbled.
func (s *Store) MarkSent(ctx context.Context, id int64, at time.Time) error {
	return s.resolve(ctx, id, Sent, sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, "")
}

// MarkFailed gives a pending play that Last.fm won't take its final status,
// Rejected or Expired, with the reason.
func (s *Store) MarkFailed(ctx context.Context, id int64, st Status, reason string) error {
	return s.resolve(ctx, id, st, sql.NullInt64{}, reason)
}

func (s *Store) resolve(ctx context.Context, id int64, st Status, sentAt sql.NullInt64, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE plays SET status = ?, sent_at = ?, error = ? WHERE id = ? AND status = ?`,
		st, sentAt, nullString(reason), id, Pending)
	if err != nil {
		return fmt.Errorf("store: mark play %d %s: %w", id, st, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("store: mark play %d %s: not pending", id, st)
	}
	return nil
}

// Plays returns the plays with the given status, oldest first.
func (s *Store) Plays(ctx context.Context, st Status) ([]Play, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, yt_track, artist, track, album, parsed, started_after, started_before, started_at,
			detected_at, min_sec, max_sec, verdict, status, inferred, sent_at, error
		FROM plays WHERE status = ? ORDER BY started_at, id`, st)
	if err != nil {
		return nil, fmt.Errorf("store: list plays: %w", err)
	}
	defer rows.Close()

	var plays []Play
	for rows.Next() {
		var (
			p                                 Play
			yt                                []byte
			artist, track, album, verdict, ex sql.NullString
			after, before, startedAt, detect  int64
			min, max, sentAt                  sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &yt, &artist, &track, &album, &p.Scrobble.Parsed, &after, &before, &startedAt,
			&detect, &min, &max, &verdict, &p.Status, &p.Inferred, &sentAt, &ex); err != nil {
			return nil, fmt.Errorf("store: list plays: %w", err)
		}
		if err := json.Unmarshal(yt, &p.Track); err != nil {
			return nil, fmt.Errorf("store: play %d: %w", p.ID, err)
		}
		p.Scrobble.Artist, p.Scrobble.Track, p.Scrobble.Album = artist.String, track.String, album.String
		p.Started = playtime.Window{After: time.UnixMilli(after), Before: time.UnixMilli(before)}
		p.StartedAt = time.UnixMilli(startedAt)
		p.DetectedAt = time.UnixMilli(detect)
		p.MinSec, p.MaxSec = nullInt(min), nullInt(max)
		p.Verdict = playtime.Verdict(verdict.String)
		if sentAt.Valid {
			t := time.UnixMilli(sentAt.Int64)
			p.SentAt = &t
		}
		p.Error = ex.String
		plays = append(plays, p)
	}
	return plays, rows.Err()
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
