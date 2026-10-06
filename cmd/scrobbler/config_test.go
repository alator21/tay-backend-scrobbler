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
