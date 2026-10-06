package poller

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
