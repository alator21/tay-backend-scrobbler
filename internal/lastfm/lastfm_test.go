package lastfm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	params := url.Values{"method": {"auth.getSession"}, "api_key": {"key"}, "token": {"tok"}, "format": {"json"}}
	sum := md5.Sum([]byte("api_keykeymethodauth.getSessiontokentoksecret"))
	if got, want := sign(params, "secret"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("sign = %s, want %s", got, want)
	}
}

// server answers every request with body, and records the last request's form.
func server(t *testing.T, status int, body string) (*Client, *url.Values) {
	t.Helper()
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.Form
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{APIKey: "key", Secret: "secret", SessionKey: "sk", URL: srv.URL}, &form
}

func TestScrobbleSingle(t *testing.T) {
	c, form := server(t, 200, `{"scrobbles":{"scrobble":{"artist":{"corrected":"0","#text":"Paramore"},
		"ignoredMessage":{"code":"0","#text":""},"track":{"corrected":"0","#text":"Careful"}},
		"@attr":{"ignored":0,"accepted":1}}}`)
	ts := time.Unix(1790000000, 0)
	res, err := c.Scrobble(context.Background(), []Scrobble{{Artist: "Paramore", Track: "Careful", Album: "Brand New Eyes", Timestamp: ts, DurationSec: 231}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].IgnoredCode != 0 {
		t.Errorf("results = %+v", res)
	}
	f := *form
	if f.Get("method") != "track.scrobble" || f.Get("artist[0]") != "Paramore" || f.Get("timestamp[0]") != "1790000000" ||
		f.Get("album[0]") != "Brand New Eyes" || f.Get("duration[0]") != "231" || f.Get("sk") != "sk" {
		t.Errorf("unexpected form: %v", f)
	}
	signed := url.Values{}
	for k, v := range f {
		signed[k] = v
	}
	if f.Get("api_sig") != sign(signed, "secret") {
		t.Error("bad signature")
	}
}

func TestScrobbleBatchIgnored(t *testing.T) {
	c, form := server(t, 200, `{"scrobbles":{"scrobble":[
		{"ignoredMessage":{"code":"0","#text":""}},
		{"ignoredMessage":{"code":"3","#text":"Timestamp too old"}}],
		"@attr":{"ignored":1,"accepted":1}}}`)
	res, err := c.Scrobble(context.Background(), []Scrobble{
		{Artist: "A", Track: "1", Timestamp: time.Unix(1, 0)},
		{Artist: "B", Track: "2", Timestamp: time.Unix(2, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].IgnoredCode != 0 || res[1].IgnoredCode != IgnoredTooOld || res[1].IgnoredMessage != "Timestamp too old" {
		t.Errorf("results = %+v", res)
	}
	if (*form).Get("album[0]") != "" || (*form).Get("duration[1]") != "" {
		t.Errorf("empty album/duration should be omitted: %v", *form)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		code      int
		temporary bool
	}{
		{"invalid session", 403, `{"error":9,"message":"Invalid session key - Please re-authenticate"}`, 9, false},
		{"rate limit", 429, `{"error":29,"message":"Rate limit exceeded"}`, 29, true},
		{"unavailable", 503, `{"error":16,"message":"There was a temporary error processing your request."}`, 16, true},
		{"bare 502", 502, `<html>Bad Gateway</html>`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := server(t, tt.status, tt.body)
			_, err := c.Scrobble(context.Background(), []Scrobble{{Artist: "A", Track: "1", Timestamp: time.Unix(1, 0)}})
			if err == nil {
				t.Fatal("want error")
			}
			var apiErr *Error
			if tt.code != 0 && (!errors.As(err, &apiErr) || apiErr.Code != tt.code) {
				t.Errorf("err = %v, want code %d", err, tt.code)
			}
			if IsTemporary(err) != tt.temporary {
				t.Errorf("IsTemporary(%v) = %v", err, !tt.temporary)
			}
		})
	}
}

func TestAuthFlow(t *testing.T) {
	c, form := server(t, 200, `{"token":"tok123"}`)
	tok, err := c.GetToken(context.Background())
	if err != nil || tok != "tok123" {
		t.Fatalf("GetToken = %q, %v", tok, err)
	}
	if (*form).Get("method") != "auth.getToken" || (*form).Get("api_sig") == "" {
		t.Errorf("unexpected form: %v", *form)
	}

	c2, _ := server(t, 200, `{"session":{"name":"someone","key":"sess","subscriber":0}}`)
	s, err := c2.GetSession(context.Background(), tok)
	if err != nil || s != (Session{Name: "someone", Key: "sess"}) {
		t.Fatalf("GetSession = %+v, %v", s, err)
	}
}
