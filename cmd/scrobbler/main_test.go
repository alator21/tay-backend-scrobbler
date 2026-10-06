package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeat(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/push/tok" || r.URL.Query().Get("status") != "up" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beat := heartbeat(ctx, srv.URL+"/api/push/tok?status=up")
	for i := 0; i < 3; i++ {
		beat()
		deadline := time.Now().Add(2 * time.Second)
		for hits.Load() < int32(i+1) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}

	if heartbeat(ctx, "") != nil {
		t.Error("empty URL should disable the heartbeat")
	}
}

func TestPingHidesURL(t *testing.T) {
	err := ping(context.Background(), &http.Client{Timeout: time.Second}, "http://127.0.0.1:1/api/push/secret-token")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("err = %v; want an error without the URL", err)
	}
}
