package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	c := &Client{Server: srv.URL + "/", Topic: "scrobbler", Token: "tk_x"}
	err := c.Send(context.Background(), Message{Title: "Needs review: Some Uploader – x", Message: "body", Priority: High, Tags: []string{"warning"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["topic"] != "scrobbler" || got["title"] != "Needs review: Some Uploader – x" || got["message"] != "body" ||
		got["priority"] != float64(High) || auth != "Bearer tk_x" {
		t.Errorf("got %v, auth %q", got, auth)
	}
}

func TestSendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":40301,"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	if err := (&Client{Server: srv.URL, Topic: "t"}).Send(context.Background(), Message{Message: "m"}); err == nil {
		t.Fatal("want error")
	}
}

func TestFromURL(t *testing.T) {
	tests := []struct {
		in, server, topic string
		bad               bool
	}{
		{in: "https://ntfy.sh/mytopic", server: "https://ntfy.sh/", topic: "mytopic"},
		{in: "https://ntfy.example.com/sub/path/alerts/", server: "https://ntfy.example.com/sub/path/", topic: "alerts"},
		{in: "https://ntfy.sh/", bad: true},
		{in: "mytopic", bad: true},
	}
	for _, tt := range tests {
		c, err := FromURL(tt.in, "tk")
		if tt.bad {
			if err == nil {
				t.Errorf("FromURL(%q): want error", tt.in)
			}
			continue
		}
		if err != nil || c.Server != tt.server || c.Topic != tt.topic || c.Token != "tk" {
			t.Errorf("FromURL(%q) = %+v, %v", tt.in, c, err)
		}
	}
}
