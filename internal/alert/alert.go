// Package alert turns recurring failures into one notification when they
// start and one when they clear, instead of one per failed attempt.
//
// State is kept in a JSON file, so a restart doesn't repeat an alert that
// was already sent. Each process should use its own file.
package alert

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/notify"
)

// Sender delivers notifications; *notify.Client implements it.
type Sender interface {
	Send(ctx context.Context, m notify.Message) error
}

type problem struct {
	Since    time.Time `json:"since"`
	Notified bool      `json:"notified"`
}

// Alerter tracks ongoing problems by key.
type Alerter struct {
	sender Sender // nil: log only
	path   string
	now    func() time.Time
	open   map[string]*problem
}

// New returns an Alerter that keeps its state in path. With a nil sender,
// notifications are only logged.
func New(sender Sender, path string) (*Alerter, error) {
	a := &Alerter{sender: sender, path: path, now: time.Now, open: map[string]*problem{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.open); err != nil {
		log.Printf("alert: ignoring unreadable state %s: %v", path, err)
		a.open = map[string]*problem{}
	}
	return a, nil
}

// Fail records that the problem key is happening. m is sent once the
// problem has lasted at least after (right away if zero), and not again
// until OK clears it.
func (a *Alerter) Fail(ctx context.Context, key string, after time.Duration, m notify.Message) {
	now := a.now()
	p := a.open[key]
	if p == nil {
		p = &problem{Since: now}
		a.open[key] = p
	}
	if !p.Notified && now.Sub(p.Since) >= after {
		if m.Message != "" && now.Sub(p.Since) > 0 {
			m.Message += "\n(since " + p.Since.Local().Format("Jan 2 15:04") + ")"
		}
		p.Notified = a.Notify(ctx, m)
	}
	a.save()
}

// OK records that the problem key is not happening. If an alert was sent for
// it, recovered is sent as a follow-up.
func (a *Alerter) OK(ctx context.Context, key string, recovered string) {
	p := a.open[key]
	if p == nil {
		return
	}
	delete(a.open, key)
	if p.Notified {
		a.Notify(ctx, notify.Message{Title: recovered, Message: "Down since " + p.Since.Local().Format("Jan 2 15:04"), Tags: []string{"white_check_mark"}})
	}
	a.save()
}

// Notify sends m now, without tracking. It reports whether m was delivered
// (always true without a sender, where it is only logged).
func (a *Alerter) Notify(ctx context.Context, m notify.Message) bool {
	log.Printf("ALERT %s: %s", m.Title, m.Message)
	if a.sender == nil {
		return true
	}
	if err := a.sender.Send(ctx, m); err != nil {
		log.Printf("alert: %v", err)
		return false
	}
	return true
}

func (a *Alerter) save() {
	data, err := json.MarshalIndent(a.open, "", "  ")
	if err == nil {
		err = os.WriteFile(a.path, data, 0o600)
	}
	if err != nil {
		log.Printf("alert: save state: %v", err)
	}
}
