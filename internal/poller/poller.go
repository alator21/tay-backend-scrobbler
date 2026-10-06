// Package poller polls the YouTube Music history, works out new plays and
// how long they ran, and records them in the store.
//
// Besides the store, it writes to the data directory:
//
//	raw/<time>.json.gz  raw responses (gzipped), saved on baseline, resyncs and errors;
//	                    deleted after rawRetention
//	events.jsonl        one line per detected play, ended play, session end or resync
//	last.json           the previous snapshot, so restarts don't re-baseline
//
// Polling is adaptive: every Interval while the newest play may still be
// running, so start times stay precise, and every IdleInterval once it must
// have finished.
package poller

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/alert"
	"github.com/alator21/tay-backend-scrobbler/internal/diff"
	"github.com/alator21/tay-backend-scrobbler/internal/notify"
	"github.com/alator21/tay-backend-scrobbler/internal/playtime"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

type snapshot struct {
	PolledAt time.Time   `json:"polledAt"`
	Tracks   []ytm.Track `json:"tracks"`
	// TopStarted is when Tracks[0] started. It is unknown (nil) after a
	// baseline or resync, since the play wasn't seen appearing.
	TopStarted *playtime.Window `json:"topStarted,omitempty"`
	// SessionEnded means Tracks[0] got a session-end event, so the next play
	// doesn't end it a second time.
	SessionEnded bool `json:"sessionEnded,omitempty"`
}

// topMayBePlaying reports whether Tracks[0] may still be running at now,
// allowing activeGrace for pauses and gaps between songs. False if its start
// is unknown.
func (s *snapshot) topMayBePlaying(now time.Time) bool {
	if s.TopStarted == nil || len(s.Tracks) == 0 {
		return false
	}
	length := time.Duration(s.Tracks[0].DurationSec) * time.Second
	return now.Before(s.TopStarted.Before.Add(length + activeGrace))
}

type event struct {
	// "play", "ended", "session-end" or "resync". A session-end is written
	// for the newest play once it must have finished with nothing after it;
	// how long it ran is unknown, and the scrobbler will assume it played
	// through.
	Type       string           `json:"type"`
	DetectedAt time.Time        `json:"detectedAt"`
	PrevPollAt *time.Time       `json:"prevPollAt,omitempty"` // resync only
	Position   int              `json:"position,omitempty"`   // play only: index in the new snapshot, 0 = newest
	Track      *ytm.Track       `json:"track,omitempty"`
	Started    *playtime.Window `json:"started,omitempty"` // play, ended and session-end
	Played     *played          `json:"played,omitempty"`  // ended only
}

// played is how long a play ran, known once the next play shows up.
type played struct {
	MinSec  int              `json:"minSec"`
	MaxSec  int              `json:"maxSec"`
	Verdict playtime.Verdict `json:"verdict"`
}

// activeGrace is how long past the newest play's latest possible end polling
// stays fast, to cover pauses and gaps between songs.
const activeGrace = 2 * time.Minute

// Config configures a Poller.
type Config struct {
	// Cookie is the Cookie header of a logged-in music.youtube.com request.
	// If empty, it is read from CookiePath instead, and re-read whenever that
	// file changes, so an expired cookie can be replaced without a restart.
	Cookie       string
	CookiePath   string
	Dir          string // data directory
	Interval     time.Duration
	IdleInterval time.Duration
	Store        *store.Store
	Alerts       *alert.Alerter
	// Pending, if set, is called when a play becomes pending, i.e. ready to
	// scrobble. It must not block.
	Pending func()
	// Polled, if set, is called after every poll, successful or not, to show
	// the poller is alive. It must not block.
	Polled func()
}

// Poller polls the history. Its methods must not be called concurrently.
type Poller struct {
	cfg       Config
	client    *ytm.Client
	cookieMod time.Time // modification time of the loaded cookie file
	prev      *snapshot
}

// New loads the cookie and the previous snapshot, if there is one.
func New(cfg Config) (*Poller, error) {
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "raw"), 0o700); err != nil {
		return nil, err
	}
	p := &Poller{cfg: cfg}
	p.pruneRaw(time.Now())
	if err := p.loadCookie(); err != nil {
		return nil, err
	}
	if err := p.loadPrev(); err != nil {
		log.Printf("ignoring previous snapshot: %v", err)
	}
	return p, nil
}

// History fetches the history once, saving the raw response.
func (p *Poller) History(ctx context.Context) ([]ytm.Track, error) {
	raw, tracks, err := p.client.History(ctx)
	if raw != nil {
		p.saveRaw(time.Now(), raw)
	}
	return tracks, err
}

// Run polls until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	var wait time.Duration
	for {
		if err := p.loadCookie(); err != nil {
			log.Printf("ERROR %v", err)
		}
		p.report(ctx, p.poll(ctx, time.Now()))
		if p.cfg.Polled != nil {
			p.cfg.Polled()
		}
		if next := p.nextInterval(time.Now()); next != wait {
			log.Printf("polling every %s", next)
			wait = next
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// loadCookie creates the client, and re-creates it if the cookie file changed
// since it was last loaded.
func (p *Poller) loadCookie() error {
	if p.cfg.Cookie != "" {
		if p.client != nil {
			return nil
		}
		auth, err := ytm.NewCookieAuth(p.cfg.Cookie)
		if err != nil {
			return err
		}
		p.client = ytm.NewClient(auth)
		return nil
	}
	fi, err := os.Stat(p.cfg.CookiePath)
	if err != nil {
		return fmt.Errorf("read cookie: %w", err)
	}
	if p.client != nil && fi.ModTime().Equal(p.cookieMod) {
		return nil
	}
	cookie, err := os.ReadFile(p.cfg.CookiePath)
	if err != nil {
		return fmt.Errorf("read cookie: %w", err)
	}
	auth, err := ytm.NewCookieAuth(string(cookie))
	if err != nil {
		return err
	}
	if p.client != nil {
		log.Printf("cookie file changed; reloaded")
	}
	p.client, p.cookieMod = ytm.NewClient(auth), fi.ModTime()
	return nil
}

// report logs the outcome of a poll and raises or clears alerts. An expired
// cookie is alerted right away; other failures once they have lasted a while,
// to ride out network blips.
func (p *Poller) report(ctx context.Context, err error) {
	cookie := errors.Is(err, ytm.ErrAuth) || errors.Is(err, ytm.ErrNoHistory)
	switch {
	case cookie:
		log.Printf("ERROR %v (cookie expired?)", err)
		p.cfg.Alerts.Fail(ctx, "cookie", 0, notify.Message{
			Title:    "YT Music cookie expired",
			Message:  err.Error() + "\n\nGet a fresh cookie and update the cookie file (picked up on the next poll) or YTM_COOKIE (needs a restart).",
			Priority: notify.Urgent,
			Tags:     []string{"cookie"},
		})
	case err != nil:
		log.Printf("ERROR %v", err)
		p.cfg.Alerts.Fail(ctx, "poll", 30*time.Minute, notify.Message{
			Title:    "Scrobbler polling failing",
			Message:  err.Error(),
			Priority: notify.High,
			Tags:     []string{"warning"},
		})
	}
	if !cookie {
		p.cfg.Alerts.OK(ctx, "cookie", "YT Music cookie working again")
	}
	if err == nil {
		p.cfg.Alerts.OK(ctx, "poll", "Scrobbler polling recovered")
	}
}

func (p *Poller) poll(ctx context.Context, now time.Time) error {
	raw, tracks, err := p.client.History(ctx)
	fetched := time.Now()
	if err != nil {
		if raw != nil {
			p.saveRaw(now, raw)
		}
		return err
	}

	ids := videoIDs(tracks)
	if dups := duplicates(ids); len(dups) > 0 {
		log.Printf("note: %d video(s) listed more than once: %v", len(dups), dups)
	}

	if p.prev == nil {
		log.Printf("baseline: %d entries, newest: %s", len(tracks), describeFirst(tracks))
		p.saveRaw(now, raw)
		return p.setPrev(snapshot{PolledAt: now, Tracks: tracks})
	}

	n, err := diff.NewPlays(videoIDs(p.prev.Tracks), ids)
	next := snapshot{PolledAt: now, Tracks: tracks, TopStarted: p.prev.TopStarted, SessionEnded: p.prev.SessionEnded}
	switch {
	case err == nil && n == 0 && len(tracks) > 0:
		if t := tracks[0]; next.TopStarted != nil && !next.SessionEnded && !next.topMayBePlaying(fetched) {
			log.Printf("  session end: %s – %s, assuming it played through", strings.Join(t.Artists, ", "), t.Title)
			if err := p.endPlay(ctx, t, *next.TopStarted, now, store.Ending{Verdict: store.SessionEnd}); err != nil {
				return err
			}
			p.appendEvent(event{Type: "session-end", DetectedAt: now, Track: &t, Started: next.TopStarted})
			next.SessionEnded = true
		}
	case err != nil:
		log.Printf("WARN %v; re-baselining", err)
		p.saveRaw(now, raw)
		p.appendEvent(event{Type: "resync", DetectedAt: now, PrevPollAt: &p.prev.PolledAt})
		next.TopStarted = nil
	case n > 0:
		// Entries missing from the previous response started after that
		// request was sent, and before this response arrived.
		started := playtime.Window{After: p.prev.PolledAt, Before: fetched}
		starts := playtime.Starts(started, n) // oldest first
		var last *ytm.Track
		if len(p.prev.Tracks) > 0 {
			last = &p.prev.Tracks[0]
		}
		lastStarted := p.prev.TopStarted
		if p.prev.SessionEnded {
			lastStarted = nil
		}
		// Oldest first, matching the order they were played in. Each play
		// ends the one before it.
		for i := n - 1; i >= 0; i-- {
			t := tracks[i]
			if last != nil && lastStarted != nil {
				if err := p.ended(ctx, now, *last, *lastStarted, started); err != nil {
					return err
				}
			}
			log.Printf("+ play: %s", Describe(t))
			if err := p.cfg.Store.AddPlay(ctx, t, started, starts[n-1-i], now); err != nil {
				return err
			}
			p.appendEvent(event{Type: "play", DetectedAt: now, Position: i, Track: &t, Started: &started})
			last, lastStarted = &t, &started
		}
		next.TopStarted, next.SessionEnded = &started, false
	}
	log.Printf("poll ok: %d entries, %d new", len(tracks), n)
	return p.setPrev(next)
}

// ended records how long t, which started in the window started, ran before
// the next play started in the window next.
func (p *Poller) ended(ctx context.Context, now time.Time, t ytm.Track, started, next playtime.Window) error {
	min, max := playtime.Played(started, next)
	v := playtime.Judge(t.DurationSec, min, max)
	log.Printf("  ended: %s – %s, ran %s to %s of %s: %s", strings.Join(t.Artists, ", "), t.Title,
		clock(min), clock(max), clock(time.Duration(t.DurationSec)*time.Second), v)
	if err := p.endPlay(ctx, t, started, now, store.Ending{Verdict: v, Min: min, Max: max}); err != nil {
		return err
	}
	p.appendEvent(event{Type: "ended", DetectedAt: now, Track: &t, Started: &started,
		Played: &played{MinSec: int(min.Seconds()), MaxSec: int(max.Seconds()), Verdict: v}})
	return nil
}

// nextInterval polls quickly while the newest play may still be running, and
// slowly once it must have finished (or its start is unknown).
func (p *Poller) nextInterval(now time.Time) time.Duration {
	if p.prev != nil && p.prev.topMayBePlaying(now) {
		return p.cfg.Interval
	}
	return p.cfg.IdleInterval
}

func (p *Poller) loadPrev() error {
	data, err := os.ReadFile(filepath.Join(p.cfg.Dir, "last.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var s snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	p.prev = &s
	log.Printf("resuming from snapshot taken %s ago", time.Since(s.PolledAt).Round(time.Second))
	return nil
}

func (p *Poller) setPrev(s snapshot) error {
	p.prev = &s
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.cfg.Dir, "last.json"), data, 0o600)
}

func (p *Poller) saveRaw(now time.Time, raw []byte) {
	// Responses are several MB of JSON; gzip shrinks them about 10x.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	if err := zw.Close(); err != nil {
		log.Printf("save raw: %v", err)
		return
	}
	name := filepath.Join(p.cfg.Dir, "raw", now.UTC().Format("20060102T150405Z")+".json.gz")
	if err := os.WriteFile(name, buf.Bytes(), 0o600); err != nil {
		log.Printf("save raw: %v", err)
	}
	p.pruneRaw(now)
}

// rawRetention is how long raw responses are kept. They are only for
// debugging a resync or error, and each is about 0.5 MB.
const rawRetention = 14 * 24 * time.Hour

// pruneRaw deletes raw responses older than rawRetention.
func (p *Poller) pruneRaw(now time.Time) {
	dir := filepath.Join(p.cfg.Dir, "raw")
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("prune raw: %v", err)
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < rawRetention {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("prune raw: %v", err)
		}
	}
}

func (p *Poller) appendEvent(e event) {
	f, err := os.OpenFile(filepath.Join(p.cfg.Dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("append event: %v", err)
		return
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(e); err != nil {
		log.Printf("append event: %v", err)
	}
}

func videoIDs(tracks []ytm.Track) []string {
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		ids[i] = t.VideoID
	}
	return ids
}

func duplicates(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	var dups []string
	for _, id := range ids {
		if seen[id] {
			dups = append(dups, id)
		}
		seen[id] = true
	}
	return dups
}

func Describe(t ytm.Track) string {
	s := fmt.Sprintf("%s – %s", strings.Join(t.Artists, ", "), t.Title)
	if t.Album != "" {
		s += " [" + t.Album + "]"
	}
	return fmt.Sprintf("%s (%d:%02d, %s, %s)", s, t.DurationSec/60, t.DurationSec%60,
		strings.TrimPrefix(t.VideoType, "MUSIC_VIDEO_TYPE_"), t.VideoID)
}

func clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func describeFirst(tracks []ytm.Track) string {
	if len(tracks) == 0 {
		return "(none)"
	}
	return Describe(tracks[0])
}

// endPlay stores how t ended, and alerts if its metadata needs a manual fix
// before it can be scrobbled.
func (p *Poller) endPlay(ctx context.Context, t ytm.Track, started playtime.Window, now time.Time, e store.Ending) error {
	st, err := p.cfg.Store.EndPlay(ctx, t, started, now, e)
	if err == nil && st == store.Pending && p.cfg.Pending != nil {
		p.cfg.Pending()
	}
	if err == nil && st == store.Review {
		p.cfg.Alerts.Notify(ctx, notify.Message{
			Title:    "Play needs review",
			Message:  Describe(t) + "\n\nArtist and title couldn't be worked out, so it won't be scrobbled.",
			Priority: notify.Low,
			Tags:     []string{"mag"},
		})
	}
	return err
}
