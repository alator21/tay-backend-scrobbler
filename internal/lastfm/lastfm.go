// Package lastfm is a minimal Last.fm API client: the desktop auth flow and
// track.scrobble.
//
// Write calls are signed: the parameters sorted by name, concatenated as
// name+value, followed by the shared secret, and MD5-hashed.
package lastfm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const apiURL = "https://ws.audioscrobbler.com/2.0/"

// MaxBatch is the most scrobbles track.scrobble accepts in one call.
const MaxBatch = 50

// Error is an error returned by the API.
type Error struct {
	Code    int    `json:"error"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("lastfm: error %d: %s", e.Code, e.Message) }

// Temporary reports whether the call may succeed if retried later: the
// service is down or busy, or the rate limit was hit.
func (e *Error) Temporary() bool {
	switch e.Code {
	case 8, 11, 16, 29: // operation failed, service offline, temporarily unavailable, rate limit
		return true
	}
	return false
}

// Client calls the API as one application, and as one user once SessionKey
// is set.
type Client struct {
	APIKey     string
	Secret     string
	SessionKey string
	HTTP       *http.Client
	// URL overrides the API endpoint, for tests.
	URL string
}

// AuthURL is the page where the user approves the token from GetToken.
func (c *Client) AuthURL(token string) string {
	return "https://www.last.fm/api/auth/?" + url.Values{"api_key": {c.APIKey}, "token": {token}}.Encode()
}

// GetToken starts the desktop auth flow. The token must be approved at
// AuthURL within an hour, then exchanged with GetSession.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	var resp struct {
		Token string `json:"token"`
	}
	if err := c.call(ctx, http.MethodGet, url.Values{"method": {"auth.getToken"}}, &resp); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// Session is an authorised user. The key doesn't expire unless the user
// revokes access.
type Session struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// GetSession exchanges an approved token for a session.
func (c *Client) GetSession(ctx context.Context, token string) (Session, error) {
	var resp struct {
		Session Session `json:"session"`
	}
	err := c.call(ctx, http.MethodGet, url.Values{"method": {"auth.getSession"}, "token": {token}}, &resp)
	return resp.Session, err
}

// Scrobble is one play to scrobble.
type Scrobble struct {
	Artist      string
	Track       string
	Album       string
	Timestamp   time.Time // when the play started
	DurationSec int
}

// Result is what Last.fm did with one scrobble.
type Result struct {
	// IgnoredCode is 0 if the scrobble was accepted. Otherwise: 1 artist
	// ignored, 2 track ignored, 3 timestamp too old, 4 timestamp too new,
	// 5 daily scrobble limit exceeded.
	IgnoredCode    int
	IgnoredMessage string
}

const (
	IgnoredTooOld     = 3
	IgnoredDailyLimit = 5
)

// ErrTokenNotAuthorized is the error code GetSession returns until the user
// approves the token.
const ErrTokenNotAuthorized = 14

// Scrobble sends up to MaxBatch scrobbles. The results are in the same order.
func (c *Client) Scrobble(ctx context.Context, scrobbles []Scrobble) ([]Result, error) {
	if len(scrobbles) == 0 || len(scrobbles) > MaxBatch {
		return nil, fmt.Errorf("lastfm: %d scrobbles in one batch, want 1 to %d", len(scrobbles), MaxBatch)
	}
	params := url.Values{"method": {"track.scrobble"}, "sk": {c.SessionKey}}
	for i, s := range scrobbles {
		n := fmt.Sprintf("[%d]", i)
		params.Set("artist"+n, s.Artist)
		params.Set("track"+n, s.Track)
		params.Set("timestamp"+n, strconv.FormatInt(s.Timestamp.Unix(), 10))
		if s.Album != "" {
			params.Set("album"+n, s.Album)
		}
		if s.DurationSec > 0 {
			params.Set("duration"+n, strconv.Itoa(s.DurationSec))
		}
	}

	var resp struct {
		Scrobbles struct {
			// An object for a single scrobble, an array for several.
			Scrobble json.RawMessage `json:"scrobble"`
		} `json:"scrobbles"`
	}
	if err := c.call(ctx, http.MethodPost, params, &resp); err != nil {
		return nil, err
	}

	type item struct {
		IgnoredMessage struct {
			Code json.Number `json:"code"`
			Text string      `json:"#text"`
		} `json:"ignoredMessage"`
	}
	var items []item
	raw := resp.Scrobbles.Scrobble
	if len(raw) > 0 && raw[0] == '{' {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("lastfm: parse scrobble response: %w", err)
	}
	if len(items) != len(scrobbles) {
		return nil, fmt.Errorf("lastfm: sent %d scrobbles, got %d results", len(scrobbles), len(items))
	}
	results := make([]Result, len(items))
	for i, it := range items {
		code, _ := strconv.Atoi(it.IgnoredMessage.Code.String())
		results[i] = Result{IgnoredCode: code, IgnoredMessage: it.IgnoredMessage.Text}
	}
	return results, nil
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	params.Set("api_key", c.APIKey)
	params.Set("api_sig", sign(params, c.Secret))
	params.Set("format", "json")

	endpoint := apiURL
	if c.URL != "" {
		endpoint = c.URL
	}
	var req *http.Request
	var err error
	if method == http.MethodPost {
		req, err = http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(params.Encode()))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, endpoint+"?"+params.Encode(), nil)
	}
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("lastfm: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("lastfm: read response: %w", err)
	}

	// Errors come as {"error": n, "message": "..."}, often with a 4xx/5xx status.
	var apiErr Error
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Code != 0 {
		return &apiErr
	}
	if res.StatusCode != http.StatusOK {
		return &HTTPError{Status: res.StatusCode, Body: string(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("lastfm: parse response: %w", err)
	}
	return nil
}

// HTTPError is a non-200 response without an API error in it.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("lastfm: HTTP %d: %.200s", e.Status, e.Body)
}

// IsTemporary reports whether a failed call may succeed if retried later.
func IsTemporary(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Temporary()
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status >= 500 || httpErr.Status == http.StatusTooManyRequests
	}
	var urlErr *url.Error // network errors
	return errors.As(err, &urlErr) && !errors.Is(err, context.Canceled)
}

func sign(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "format" && k != "callback" && k != "api_sig" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params.Get(k))
	}
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
