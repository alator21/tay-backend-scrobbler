package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/alert"
	"github.com/alator21/tay-backend-scrobbler/internal/lastfm"
	"github.com/alator21/tay-backend-scrobbler/internal/notify"
	"github.com/alator21/tay-backend-scrobbler/internal/send"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
)

// config is read from environment variables, so secrets stay out of files
// and command lines.
type config struct {
	dataDir string // DATA_DIR: database, snapshot, alert state, raw responses

	cookie     string // YTM_COOKIE: Cookie header of a logged-in music.youtube.com request
	cookieFile string // YTM_COOKIE_FILE: used when YTM_COOKIE is empty; reloaded when it changes

	lastfmKey, lastfmSecret string // LASTFM_API_KEY, LASTFM_API_SECRET
	lastfmSession           string // LASTFM_SESSION_KEY, printed by `scrobbler auth`

	ntfyURL, ntfyToken string // NTFY_URL (topic URL), NTFY_TOKEN (optional)
	heartbeatURL       string // HEARTBEAT_URL: GET after every poll
}

func loadConfig() config {
	c := config{
		dataDir:       getenv("DATA_DIR", "data"),
		cookie:        os.Getenv("YTM_COOKIE"),
		lastfmKey:     os.Getenv("LASTFM_API_KEY"),
		lastfmSecret:  os.Getenv("LASTFM_API_SECRET"),
		lastfmSession: os.Getenv("LASTFM_SESSION_KEY"),
		ntfyURL:       os.Getenv("NTFY_URL"),
		ntfyToken:     os.Getenv("NTFY_TOKEN"),
		heartbeatURL:  os.Getenv("HEARTBEAT_URL"),
	}
	c.cookieFile = getenv("YTM_COOKIE_FILE", filepath.Join(c.dataDir, "cookie.txt"))
	return c
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func (c config) store() (*store.Store, error) {
	return store.Open(c.dataPath("scrobbler.db"))
}

// lastfm returns a Last.fm client, with the user's session if withSession.
func (c config) lastfm(withSession bool) (*lastfm.Client, error) {
	if c.lastfmKey == "" || c.lastfmSecret == "" {
		return nil, errors.New("LASTFM_API_KEY and LASTFM_API_SECRET must be set (create an API account at https://www.last.fm/api/account/create)")
	}
	client := &lastfm.Client{APIKey: c.lastfmKey, Secret: c.lastfmSecret, HTTP: &http.Client{Timeout: 30 * time.Second}}
	if withSession {
		if c.lastfmSession == "" {
			return nil, errors.New("LASTFM_SESSION_KEY must be set (run: scrobbler auth)")
		}
		client.SessionKey = c.lastfmSession
	}
	return client, nil
}

func (c config) sender(st *store.Store) (*send.Sender, error) {
	client, err := c.lastfm(true)
	if err != nil {
		return nil, err
	}
	return &send.Sender{Store: st, Lastfm: client, Now: time.Now, Retries: []time.Duration{30 * time.Second, 2 * time.Minute}}, nil
}

// alerter returns an Alerter keeping its state in the named file. Without
// NTFY_URL, alerts are only logged.
func (c config) alerter(stateFile string) (*alert.Alerter, error) {
	var sender alert.Sender
	if c.ntfyURL != "" {
		n, err := notify.FromURL(c.ntfyURL, c.ntfyToken)
		if err != nil {
			return nil, err
		}
		sender = n
	}
	if err := os.MkdirAll(c.dataDir, 0o700); err != nil {
		return nil, err
	}
	return alert.New(sender, c.dataPath(stateFile))
}
