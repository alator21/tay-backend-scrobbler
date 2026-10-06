// Command scrobbler scrobbles YouTube Music plays to Last.fm.
//
//	scrobbler run              poll the history and scrobble plays as they end
//	scrobbler auth             authorise with Last.fm and print the session key (once)
//	scrobbler history          fetch and print the history once
//	scrobbler send [-dry-run]  scrobble pending plays now
//	scrobbler list [status]    list plays with a status (default pending)
//
// Configuration, including all secrets, comes from environment variables;
// see config.go and the README.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alator21/tay-backend-scrobbler/internal/alert"
	"github.com/alator21/tay-backend-scrobbler/internal/lastfm"
	"github.com/alator21/tay-backend-scrobbler/internal/notify"
	"github.com/alator21/tay-backend-scrobbler/internal/poller"
	"github.com/alator21/tay-backend-scrobbler/internal/send"
	"github.com/alator21/tay-backend-scrobbler/internal/store"
)

const usage = `usage: scrobbler <command> [flags]

  run              poll the history and scrobble plays as they end
  auth             authorise with Last.fm and print the session key
  history          fetch and print the history once
  send [-dry-run]  scrobble pending plays now
  list [status]    list plays with a status (default pending)

Configuration comes from environment variables (see the README).`

func main() {
	flag.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	switch cmd, args := flag.Arg(0), flag.Args()[1:]; cmd {
	case "run":
		err = runCmd(ctx, cfg, args)
	case "auth":
		err = authCmd(ctx, cfg)
	case "history":
		err = historyCmd(ctx, cfg)
	case "send":
		err = sendCmd(ctx, cfg, args)
	case "list":
		err = listCmd(ctx, cfg, args)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func authCmd(ctx context.Context, cfg config) error {
	c, err := cfg.lastfm(false)
	if err != nil {
		return err
	}
	token, err := c.GetToken(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Open this URL and allow access (waiting up to 10 minutes):\n\n  %s\n\n", c.AuthURL(token))
	// Until the token is approved, getSession fails with error 14.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var s lastfm.Session
	for {
		s, err = c.GetSession(ctx, token)
		var apiErr *lastfm.Error
		if !errors.As(err, &apiErr) || apiErr.Code != lastfm.ErrTokenNotAuthorized {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("token not approved in time")
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("Authorised as %s. Set this in the environment (it doesn't expire):\n\n  LASTFM_SESSION_KEY=%s\n", s.Name, s.Key)
	return nil
}

func runCmd(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	interval := fs.Duration("interval", cfg.pollInterval, "poll interval while a song may be playing ($POLL_INTERVAL)")
	idleInterval := fs.Duration("idle-interval", cfg.idlePollInterval, "poll interval otherwise ($IDLE_POLL_INTERVAL)")
	sendInterval := fs.Duration("send-interval", cfg.sendInterval, "how often to retry pending plays; plays are also sent as soon as they end ($SEND_INTERVAL)")
	fs.Parse(args)

	st, err := cfg.store()
	if err != nil {
		return err
	}
	defer st.Close()
	snd, err := cfg.sender(st)
	if err != nil {
		return err
	}
	if cfg.ntfyURL == "" {
		log.Printf("NTFY_URL not set; alerts are only logged")
	}
	// Each goroutine gets its own Alerter (and state file).
	pollAlerts, err := cfg.alerter("alerts-poll.json")
	if err != nil {
		return err
	}
	sendAlerts, err := cfg.alerter("alerts-send.json")
	if err != nil {
		return err
	}

	// kick asks the sender to send now; a pending kick is enough.
	kick := make(chan struct{}, 1)
	p, err := poller.New(poller.Config{
		Cookie:       cfg.cookie,
		CookiePath:   cfg.cookieFile,
		Dir:          cfg.dataDir,
		Interval:     *interval,
		IdleInterval: *idleInterval,
		RawRetention: cfg.rawRetention,
		Store:        st,
		Alerts:       pollAlerts,
		Polled:       heartbeat(ctx, cfg.heartbeatURL),
		Pending: func() {
			select {
			case kick <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return err
	}

	// Sending runs on its own so Last.fm retries never delay a poll.
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(*sendInterval)
		defer tick.Stop()
		for {
			sendAndAlert(ctx, snd, sendAlerts)
			select {
			case <-ctx.Done():
				return
			case <-kick:
			case <-tick.C:
			}
		}
	}()

	log.Printf("running; data in %s", cfg.dataDir)
	p.Run(ctx)
	<-done
	return nil
}

func historyCmd(ctx context.Context, cfg config) error {
	p, err := poller.New(poller.Config{Cookie: cfg.cookie, CookiePath: cfg.cookieFile, Dir: cfg.dataDir, RawRetention: cfg.rawRetention})
	if err != nil {
		return err
	}
	tracks, err := p.History(ctx)
	if err != nil {
		return err
	}
	for i, t := range tracks {
		fmt.Printf("%3d  %-9s  %s\n", i, t.Section, poller.Describe(t))
	}
	return nil
}

func sendCmd(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be sent without sending")
	fs.Parse(args)

	if *dryRun {
		return list(ctx, cfg, store.Pending)
	}
	st, err := cfg.store()
	if err != nil {
		return err
	}
	defer st.Close()
	snd, err := cfg.sender(st)
	if err != nil {
		return err
	}
	alerts, err := cfg.alerter("alerts-send.json")
	if err != nil {
		return err
	}
	return sendAndAlert(ctx, snd, alerts)
}

// sendAndAlert sends the pending plays, and raises or clears alerts.
func sendAndAlert(ctx context.Context, snd *send.Sender, alerts *alert.Alerter) error {
	rep, err := snd.Send(ctx)
	if errors.Is(err, context.Canceled) {
		return err
	}
	if err != nil {
		log.Printf("ERROR %v", err)
	}
	if rep != (send.Report{}) {
		log.Printf("sent %d, rejected %d, expired %d", rep.Sent, rep.Rejected, rep.Expired)
	}

	var apiErr *lastfm.Error
	switch {
	case errors.As(err, &apiErr) && (apiErr.Code == 4 || apiErr.Code == 9 || apiErr.Code == 10 || apiErr.Code == 26):
		// Authentication failed, invalid session key, invalid or suspended API key.
		alerts.Fail(ctx, "auth", 0, notify.Message{
			Title:    "Last.fm access lost",
			Message:  err.Error() + "\n\nRun `scrobbler auth` and update LASTFM_SESSION_KEY (or the API key).",
			Priority: notify.Urgent,
			Tags:     []string{"key"},
		})
	case err != nil:
		// Plays keep until they are 14 days old, so only a lasting failure
		// is worth an alert.
		alerts.Fail(ctx, "send", 6*time.Hour, notify.Message{
			Title:    "Scrobbling failing",
			Message:  err.Error(),
			Priority: notify.High,
			Tags:     []string{"warning"},
		})
	default:
		alerts.OK(ctx, "auth", "Last.fm access restored")
		alerts.OK(ctx, "send", "Scrobbling recovered")
	}
	if rep.Rejected > 0 || rep.Expired > 0 {
		alerts.Notify(ctx, notify.Message{
			Title:    "Plays not scrobbled",
			Message:  fmt.Sprintf("%d rejected by Last.fm, %d expired.\n\nSee: scrobbler list rejected / list expired", rep.Rejected, rep.Expired),
			Priority: notify.Normal,
			Tags:     []string{"mag"},
		})
	}
	if rep.DailyLimit {
		log.Printf("daily scrobble limit reached; the rest stays pending")
	}
	return err
}

func listCmd(ctx context.Context, cfg config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)
	status := store.Pending
	if fs.NArg() > 0 {
		status = store.Status(fs.Arg(0))
	}
	return list(ctx, cfg, status)
}

// list prints the plays with the given status, oldest first.
func list(ctx context.Context, cfg config, status store.Status) error {
	st, err := cfg.store()
	if err != nil {
		return err
	}
	defer st.Close()
	plays, err := st.Plays(ctx, status)
	if err != nil {
		return err
	}
	for _, p := range plays {
		fmt.Printf("%s  ", p.StartedAt.Local().Format("2006-01-02 15:04:05"))
		if p.Scrobble.Artist != "" {
			fmt.Printf("%s – %s", p.Scrobble.Artist, p.Scrobble.Track)
			if p.Scrobble.Album != "" {
				fmt.Printf(" [%s]", p.Scrobble.Album)
			}
		} else {
			// No cleaned metadata: show what YT Music had.
			fmt.Printf("%q by %s (YT %s)", p.Track.Title, strings.Join(p.Track.Artists, ", "), p.Track.VideoID)
		}
		if p.Inferred {
			fmt.Printf("  (inferred: %s)", p.Verdict)
		}
		if status == store.Pending && time.Since(p.StartedAt) > send.MaxAge {
			fmt.Print("  (too old, will expire)")
		}
		if p.Error != "" {
			fmt.Printf("  (%s)", p.Error)
		}
		fmt.Println()
	}
	fmt.Printf("%d %s\n", len(plays), status)
	return nil
}

// heartbeat returns a function that pings url in the background, so a
// monitor can tell the scrobbler is alive. It logs only when pinging starts
// or stops working. With an empty url it does nothing.
func heartbeat(ctx context.Context, url string) func() {
	if url == "" {
		return nil
	}
	poke := make(chan struct{}, 1)
	go func() {
		hc := &http.Client{Timeout: 10 * time.Second}
		failing := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-poke:
			}
			err := ping(ctx, hc, url)
			switch {
			case err != nil && !failing && ctx.Err() == nil:
				log.Printf("heartbeat failing: %v", err)
				failing = true
			case err == nil && failing:
				log.Printf("heartbeat working again")
				failing = false
			}
		}
	}()
	return func() {
		select {
		case poke <- struct{}{}:
		default:
		}
	}
}

func ping(ctx context.Context, hc *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := hc.Do(req)
	if err != nil {
		// The URL often holds a token; keep it out of the logs.
		if u, ok := err.(*neturl.Error); ok {
			return u.Err
		}
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

// dataPath is a file in the data directory.
func (c config) dataPath(name string) string { return filepath.Join(c.dataDir, name) }
