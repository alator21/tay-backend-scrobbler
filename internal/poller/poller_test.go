package poller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/alert"
	"github.com/alator21/tay-backend-scrobbler/internal/notify"
	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

func TestPruneRaw(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for name, age := range map[string]time.Duration{"old.json.gz": 15 * 24 * time.Hour, "new.json.gz": 13 * 24 * time.Hour} {
		path := filepath.Join(raw, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	(&Poller{cfg: Config{Dir: dir, RawRetention: 14 * 24 * time.Hour}}).pruneRaw(now)

	entries, _ := os.ReadDir(raw)
	if len(entries) != 1 || entries[0].Name() != "new.json.gz" {
		t.Errorf("left %v, want only new.json.gz", entries)
	}
}

func TestRawRetentionZero(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	if err := os.MkdirAll(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(raw, "old.json.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Poller{cfg: Config{Dir: dir}}
	p.saveRaw(time.Now(), []byte(`{}`))
	p.pruneRaw(time.Now())
	if entries, _ := os.ReadDir(raw); len(entries) != 0 {
		t.Errorf("with zero retention, want no raw files, got %v", entries)
	}
}

type sentAlerts []string

func (s *sentAlerts) Send(_ context.Context, m notify.Message) error {
	*s = append(*s, m.Title)
	return nil
}

// An expired cookie is alerted on the first failed poll; an empty history
// in a logged-in response, which YouTube Music sends now and then for a
// single poll, is not.
func TestReportCookieAlert(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"logged out", fmt.Errorf("%w: not logged in", ytm.ErrNoHistory), 1},
		{"auth rejected", ytm.ErrAuth, 1},
		{"empty history", ytm.ErrEmptyHistory, 0},
		{"network", errors.New("dial tcp: lookup music.youtube.com"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent sentAlerts
			alerts, err := alert.New(&sent, filepath.Join(t.TempDir(), "alerts.json"))
			if err != nil {
				t.Fatal(err)
			}
			p := &Poller{cfg: Config{Alerts: alerts}}
			p.report(context.Background(), tt.err)
			if len(sent) != tt.want {
				t.Errorf("sent %v, want %d alert(s)", sent, tt.want)
			}
		})
	}
}
