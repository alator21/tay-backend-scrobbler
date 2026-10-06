package alert

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/notify"
)

type fake struct {
	sent []notify.Message
	err  error
}

func (f *fake) Send(_ context.Context, m notify.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func newAt(t *testing.T, f *fake, path string, now *time.Time) *Alerter {
	t.Helper()
	a, err := New(f, path)
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return *now }
	return a
}

func TestAlertOnceAndRecover(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "alerts.json")
	f := &fake{}
	a := newAt(t, f, path, &now)
	msg := notify.Message{Title: "fetch failing", Message: "boom"}

	a.Fail(ctx, "fetch", 30*time.Minute, msg)
	now = now.Add(20 * time.Minute)
	a.Fail(ctx, "fetch", 30*time.Minute, msg)
	if len(f.sent) != 0 {
		t.Fatalf("alerted before threshold: %v", f.sent)
	}

	// Restart: state comes back from the file.
	now = now.Add(15 * time.Minute)
	a = newAt(t, f, path, &now)
	a.Fail(ctx, "fetch", 30*time.Minute, msg)
	a.Fail(ctx, "fetch", 30*time.Minute, msg)
	if len(f.sent) != 1 || f.sent[0].Title != "fetch failing" {
		t.Fatalf("want one alert, got %v", f.sent)
	}

	a.OK(ctx, "fetch", "fetch recovered")
	a.OK(ctx, "fetch", "fetch recovered")
	if len(f.sent) != 2 || f.sent[1].Title != "fetch recovered" {
		t.Fatalf("want one recovery, got %v", f.sent)
	}
}

func TestImmediateAndUnnotifiedRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f := &fake{}
	a := newAt(t, f, filepath.Join(t.TempDir(), "alerts.json"), &now)

	a.Fail(ctx, "cookie", 0, notify.Message{Title: "cookie expired", Message: "x"})
	if len(f.sent) != 1 || f.sent[0].Message != "x" {
		t.Fatalf("want immediate alert without since-note, got %v", f.sent)
	}

	// A problem that cleared before its threshold sends nothing.
	a.Fail(ctx, "fetch", time.Hour, notify.Message{Title: "t", Message: "m"})
	a.OK(ctx, "fetch", "recovered")
	if len(f.sent) != 1 {
		t.Fatalf("unexpected alerts: %v", f.sent)
	}
}

func TestRetryWhenDeliveryFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f := &fake{err: errors.New("ntfy down")}
	a := newAt(t, f, filepath.Join(t.TempDir(), "alerts.json"), &now)

	a.Fail(ctx, "cookie", 0, notify.Message{Title: "cookie expired", Message: "x"})
	f.err = nil
	a.Fail(ctx, "cookie", 0, notify.Message{Title: "cookie expired", Message: "x"})
	if len(f.sent) != 1 {
		t.Fatalf("want alert delivered on retry, got %v", f.sent)
	}
}
