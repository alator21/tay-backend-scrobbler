package ytm

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CookieAuth authenticates with the cookies of a logged-in browser session.
type CookieAuth struct {
	cookie   string
	sapisid  string
	authUser string
}

// NewCookieAuth takes a raw Cookie header copied from a logged-in
// music.youtube.com request.
func NewCookieAuth(cookie string) (*CookieAuth, error) {
	cookie = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cookie), "cookie:"))
	sapisid := cookieValue(cookie, "__Secure-3PAPISID")
	if sapisid == "" {
		sapisid = cookieValue(cookie, "SAPISID")
	}
	if sapisid == "" {
		return nil, errors.New("ytm: cookie has no __Secure-3PAPISID or SAPISID")
	}
	return &CookieAuth{cookie: cookie, sapisid: sapisid, authUser: "0"}, nil
}

func cookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}

func (a *CookieAuth) apply(_ context.Context, req *http.Request, now time.Time) error {
	req.Header.Set("Cookie", a.cookie)
	req.Header.Set("X-Origin", origin)
	req.Header.Set("X-Goog-AuthUser", a.authUser)
	req.Header.Set("Authorization", sapisidHash(a.sapisid, now))
	return nil
}

// sapisidHash computes the SAPISIDHASH authorization the web client sends.
func sapisidHash(sapisid string, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	sum := sha1.Sum([]byte(ts + " " + sapisid + " " + origin))
	return "SAPISIDHASH " + ts + "_" + hex.EncodeToString(sum[:])
}
