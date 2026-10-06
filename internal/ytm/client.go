// Package ytm is a minimal client for the internal YouTube Music API
// (the "youtubei" endpoints used by music.youtube.com).
//
// Request details (headers, client context, visitor ID) mirror ytmusicapi,
// which is the best-maintained reference for what the API accepts.
package ytm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	origin    = "https://music.youtube.com"
	apiURL    = origin + "/youtubei/v1/"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:88.0) Gecko/20100101 Firefox/88.0"
)

// ErrAuth means YouTube rejected the credentials (expired cookie, revoked token, ...).
var ErrAuth = errors.New("ytm: authentication rejected")

// Auth adds credentials to an API request.
type Auth interface {
	apply(ctx context.Context, req *http.Request, now time.Time) error
}

type Client struct {
	http *http.Client
	auth Auth

	visitorOnce sync.Once
	visitorID   string
}

func NewClient(auth Auth) *Client {
	return &Client{
		http: &http.Client{Timeout: 30 * time.Second},
		auth: auth,
	}
}

func clientContext(now time.Time) map[string]any {
	return map[string]any{
		"client": map[string]any{
			"clientName":    "WEB_REMIX",
			"clientVersion": "1." + now.UTC().Format("20060102") + ".01.00",
			"hl":            "en",
		},
		"user": map[string]any{},
	}
}

var ytcfgRe = regexp.MustCompile(`ytcfg\.set\s*\(\s*({.+?})\s*\)\s*;`)

// visitor returns the anonymous visitor ID embedded in the YTM homepage.
// Failures yield "" and the header is omitted, as ytmusicapi does.
func (c *Client) visitor(ctx context.Context) string {
	c.visitorOnce.Do(func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", userAgent)
		// Without a consent cookie, EU visitors are redirected to consent.youtube.com.
		req.Header.Set("Cookie", "SOCS=CAI")
		resp, err := c.http.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return
		}
		m := ytcfgRe.FindSubmatch(page)
		if m == nil {
			return
		}
		var cfg struct {
			VisitorData string `json:"VISITOR_DATA"`
		}
		if json.Unmarshal(m[1], &cfg) == nil {
			c.visitorID = cfg.VisitorData
		}
	})
	return c.visitorID
}

// Browse calls the browse endpoint and returns the raw JSON response.
func (c *Client) Browse(ctx context.Context, browseID string) ([]byte, error) {
	now := time.Now()
	body, err := json.Marshal(map[string]any{
		"browseId": browseID,
		"context":  clientContext(now),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"browse?alt=json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", origin)
	if v := c.visitor(ctx); v != "" {
		req.Header.Set("X-Goog-Visitor-Id", v)
	}
	if err := c.auth.apply(ctx, req, now); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ytm: browse %s: %w", browseID, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("ytm: browse %s: read body: %w", browseID, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: HTTP %d: %.300s", ErrAuth, resp.StatusCode, data)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("ytm: browse %s: HTTP %d: %.300s", browseID, resp.StatusCode, data)
	}
	return data, nil
}
