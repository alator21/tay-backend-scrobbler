package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	for _, name := range []string{"DATA_DIR", "YTM_COOKIE_FILE", "POLL_INTERVAL", "IDLE_POLL_INTERVAL",
		"SEND_INTERVAL", "RAW_RETENTION", "SCROBBLE_UNSURE", "ARTIST_MODE"} {
		t.Setenv(name, "")
	}
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.dataDir != "data" || c.cookieFile != "data/cookie.txt" || c.pollInterval != 30*time.Second ||
		c.idlePollInterval != 3*time.Minute || c.sendInterval != 15*time.Minute || c.rawRetention != 14*24*time.Hour ||
		c.policy.SkipUnsure || c.policy.Meta.AllArtists {
		t.Errorf("defaults = %+v", c)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("DATA_DIR", "/data")
	t.Setenv("POLL_INTERVAL", "20s")
	t.Setenv("RAW_RETENTION", "0")
	t.Setenv("SCROBBLE_UNSURE", "false")
	t.Setenv("ARTIST_MODE", "all")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.cookieFile != "/data/cookie.txt" || c.pollInterval != 20*time.Second || c.rawRetention != 0 ||
		!c.policy.SkipUnsure || !c.policy.Meta.AllArtists {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "0")
	t.Setenv("SEND_INTERVAL", "soon")
	t.Setenv("RAW_RETENTION", "-1h")
	t.Setenv("SCROBBLE_UNSURE", "maybe")
	t.Setenv("ARTIST_MODE", "some")
	_, err := loadConfig()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, name := range []string{"POLL_INTERVAL", "SEND_INTERVAL", "RAW_RETENTION", "SCROBBLE_UNSURE", "ARTIST_MODE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error doesn't mention %s: %v", name, err)
		}
	}
}

func TestDescribeHidesSecrets(t *testing.T) {
	t.Setenv("YTM_COOKIE", "SID=cookie-secret")
	t.Setenv("LASTFM_API_KEY", "key-secret")
	t.Setenv("LASTFM_API_SECRET", "secret-secret")
	t.Setenv("LASTFM_SESSION_KEY", "")
	t.Setenv("NTFY_URL", "https://ntfy.sh/topic-secret")
	t.Setenv("NTFY_TOKEN", "token-secret")
	t.Setenv("HEARTBEAT_URL", "http://kuma:3001/api/push/push-secret?status=up")
	t.Setenv("SCROBBLE_UNSURE", "false")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c.describe(), "\n")
	if strings.Contains(got, "secret") {
		t.Errorf("describe leaks a secret:\n%s", got)
	}
	for _, want := range []string{"YTM_COOKIE=(set)", "LASTFM_SESSION_KEY=(not set)", "NTFY_URL=https://ntfy.sh/…",
		"HEARTBEAT_URL=http://kuma:3001/…", "SCROBBLE_UNSURE=false", "POLL_INTERVAL=30s"} {
		if !strings.Contains(got, want) {
			t.Errorf("describe lacks %q:\n%s", want, got)
		}
	}
}
