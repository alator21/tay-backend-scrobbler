// Package send scrobbles pending plays from the store to Last.fm.
package send

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/lastfm"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
)

// MaxAge is how old a play can be and still be sent. Last.fm ignores
// scrobbles older than 14 days; the hour is a margin for clock skew.
const MaxAge = 14*24*time.Hour - time.Hour

// Scrobbler is the part of the Last.fm client used here.
type Scrobbler interface {
	Scrobble(ctx context.Context, scrobbles []lastfm.Scrobble) ([]lastfm.Result, error)
}

// Sender sends pending plays.
type Sender struct {
	Store  *store.Store
	Lastfm Scrobbler
	// Retries are the waits before retrying a batch that failed with a
	// temporary error. Once they run out, Send returns the error and the
	// plays stay pending for the next run.
	Retries []time.Duration
	Now     func() time.Time
}

// Report counts what one Send did.
type Report struct {
	Sent, Rejected, Expired int
	// DailyLimit means Last.fm's daily scrobble limit was hit; the rest
	// stays pending.
	DailyLimit bool
}

// Send scrobbles all pending plays, oldest first.
func (s *Sender) Send(ctx context.Context) (Report, error) {
	var rep Report
	plays, err := s.Store.Plays(ctx, store.Pending)
	if err != nil {
		return rep, err
	}
	now := s.Now()

	var batch []store.Play
	for _, p := range plays {
		if now.Sub(p.StartedAt) > MaxAge {
			if err := s.Store.MarkFailed(ctx, p.ID, store.Expired, "older than 14 days"); err != nil {
				return rep, err
			}
			rep.Expired++
			continue
		}
		batch = append(batch, p)
	}

	for len(batch) > 0 && !rep.DailyLimit {
		n := min(len(batch), lastfm.MaxBatch)
		if err := s.sendBatch(ctx, batch[:n], &rep); err != nil {
			return rep, err
		}
		batch = batch[n:]
	}
	return rep, nil
}

func (s *Sender) sendBatch(ctx context.Context, plays []store.Play, rep *Report) error {
	scrobbles := make([]lastfm.Scrobble, len(plays))
	for i, p := range plays {
		scrobbles[i] = lastfm.Scrobble{
			Artist:      p.Scrobble.Artist,
			Track:       p.Scrobble.Track,
			Album:       p.Scrobble.Album,
			Timestamp:   p.StartedAt,
			DurationSec: p.Track.DurationSec,
		}
	}

	results, err := s.Lastfm.Scrobble(ctx, scrobbles)
	for attempt := 0; err != nil && lastfm.IsTemporary(err) && attempt < len(s.Retries); attempt++ {
		log.Printf("send: %v; retrying in %s", err, s.Retries[attempt])
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.Retries[attempt]):
		}
		results, err = s.Lastfm.Scrobble(ctx, scrobbles)
	}
	if err != nil {
		return fmt.Errorf("send %d plays: %w", len(plays), err)
	}

	sentAt := s.Now()
	for i, r := range results {
		id := plays[i].ID
		switch r.IgnoredCode {
		case 0:
			err = s.Store.MarkSent(ctx, id, sentAt)
			rep.Sent++
		case lastfm.IgnoredTooOld:
			err = s.Store.MarkFailed(ctx, id, store.Expired, r.IgnoredMessage)
			rep.Expired++
		case lastfm.IgnoredDailyLimit:
			rep.DailyLimit = true // stays pending
		default:
			err = s.Store.MarkFailed(ctx, id, store.Rejected, fmt.Sprintf("%d: %s", r.IgnoredCode, r.IgnoredMessage))
			rep.Rejected++
		}
		if err != nil {
			return err
		}
	}
	return nil
}
