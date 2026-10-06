// Package notify publishes messages to an ntfy topic.
//
// Messages are published as JSON to the server root rather than with
// headers, so titles can contain non-ASCII text.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Priorities, as ntfy defines them.
const (
	Low    = 2
	Normal = 3
	High   = 4
	Urgent = 5
)

// Message is one notification.
type Message struct {
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"` // emoji short codes, e.g. "warning"
}

// Client publishes to one topic.
type Client struct {
	Server string // e.g. https://ntfy.sh
	Topic  string
	Token  string // access token, for protected topics
	HTTP   *http.Client
}

// Send publishes m.
func (c *Client) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(struct {
		Topic string `json:"topic"`
		Message
	}{c.Topic, m})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Server, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		return fmt.Errorf("ntfy: HTTP %d: %s", res.StatusCode, bytes.TrimSpace(data))
	}
	return nil
}

// FromURL returns a Client for a topic URL such as https://ntfy.sh/mytopic,
// with an optional access token.
func FromURL(topicURL, token string) (*Client, error) {
	u, err := url.Parse(topicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("ntfy: %q is not a topic URL like https://ntfy.sh/mytopic", topicURL)
	}
	dir, topic := path.Split(strings.TrimRight(u.Path, "/"))
	if topic == "" {
		return nil, fmt.Errorf("ntfy: %q has no topic", topicURL)
	}
	u.Path, u.RawQuery, u.Fragment = dir, "", ""
	return &Client{Server: u.String(), Topic: topic, Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}}, nil
}
