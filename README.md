# tay-backend-scrobbler

Scrobbles your YouTube Music listening to Last.fm, from a server, with no
browser extension: it polls your YT Music history, works out which plays are
new and how long they ran, and scrobbles the ones Last.fm's rules count.
Single user. One static Go binary (SQLite built in), available as a small
multi-arch Docker image.

- Plays are scrobbled as soon as they end, with the start time as timestamp.
- Skips are detected and not scrobbled.
- Upload titles like `Artist - Title (Official Video)` are cleaned up.
- Optional alerts via [ntfy](https://ntfy.sh) (expired cookie, Last.fm access
  lost, …) and a heartbeat URL for uptime monitors.

## Quick start (Docker)

1. **Last.fm API account.** Create one at https://www.last.fm/api/account/create
   (the callback URL can stay empty) and note the API key and shared secret.
2. **YouTube Music cookie.** See [Getting the cookie](#getting-the-cookie).
3. **Configure.** Copy [`compose.yaml`](compose.yaml) into a directory and fill in
   `LASTFM_API_KEY`, `LASTFM_API_SECRET`, and either `YTM_COOKIE` or a
   `data/cookie.txt` file. The values go straight into its `environment:`
   block; if you'd rather keep secrets out of the compose file, use
   `env_file: .env` or `${VAR}` references instead.
4. **Authorise** with Last.fm, once:

   ```sh
   docker compose run --rm scrobbler auth
   ```

   Open the printed URL and allow access; it then prints a
   `LASTFM_SESSION_KEY=...` line. Add it to `compose.yaml`. The key doesn't expire.
5. **Run:**

   ```sh
   docker compose up -d
   docker compose logs -f
   ```

The image runs as uid 65532 and keeps its state in `/data`. With a bind mount,
make the directory writable for that uid, or set `user:` in `compose.yaml`.

Without Docker: `go install github.com/alator21/tay-backend-scrobbler/cmd/scrobbler@latest`,
set the same environment variables, and run `scrobbler run`.

## Configuration

All settings, including secrets, are environment variables.

| Variable | |
|---|---|
| `LASTFM_API_KEY`, `LASTFM_API_SECRET` | Required. Your Last.fm API account. |
| `LASTFM_SESSION_KEY` | Required to scrobble. Printed by `scrobbler auth`. |
| `YTM_COOKIE` | The YT Music `Cookie` header. Changing it needs a restart. |
| `YTM_COOKIE_FILE` | Used when `YTM_COOKIE` is empty. Default `$DATA_DIR/cookie.txt`. Re-read whenever the file changes, so an expired cookie can be replaced without a restart. |
| `DATA_DIR` | Database and state. Default `data` (`/data` in the image). |
| `NTFY_URL` | Optional. ntfy topic URL for alerts, e.g. `https://ntfy.sh/some-long-random-topic`. Without it, alerts are only logged. |
| `NTFY_TOKEN` | Optional. Access token for protected ntfy topics. |
| `HEARTBEAT_URL` | Optional. GET after every poll, for a dead-man's-switch monitor such as an Uptime Kuma push monitor or healthchecks.io. |
| `TZ` | Timezone for log timestamps. |

Optional tuning (defaults shown):

| Variable | |
|---|---|
| `POLL_INTERVAL=30s` | How often to poll while a song may be playing. Shorter means tighter start times and play lengths. |
| `IDLE_POLL_INTERVAL=3m` | How often to poll otherwise. |
| `SEND_INTERVAL=15m` | How often to retry pending plays. Plays are also sent as soon as they end. |
| `SCROBBLE_UNSURE=true` | Whether to scrobble plays whose length can't be told well enough to apply Last.fm's rule, typically after downtime. `false` marks them `skipped`. The last play of a session is scrobbled either way. |
| `ARTIST_MODE=first` | `first` scrobbles collaborations under their first artist; `all` joins them as `A & B`. |
| `RAW_RETENTION=336h` | How long to keep raw history responses (saved on fresh starts, resyncs and errors, for debugging; about 0.5 MB each). `0` doesn't save them. |

Durations use Go syntax (`30s`, `5m`, `336h`). `run` also takes `-interval`,
`-idle-interval` and `-send-interval` flags, which override the variables.
Invalid values stop the scrobbler at startup with a message naming them.

In a compose file, write any `$` in a value (cookies can contain one) as `$$`;
compose would otherwise treat it as a variable. `cookie.txt` needs no escaping.

## Getting the cookie

1. Open a **private/incognito** window and log in at https://music.youtube.com.
2. DevTools → Network, filter for `browse`, then click around (e.g. open Library).
3. Select a `browse` request → Request Headers → copy the full value of `cookie`.
4. Put it in `YTM_COOKIE`, or save it as `cookie.txt` in the data directory.
5. **Close the window without logging out.** Logging out invalidates the cookie.

A cookie from a private window isn't touched by your normal browsing, so it
lasts longer. When it does expire, the scrobbler alerts (if ntfy is set up) and
keeps retrying; replace `cookie.txt` and it carries on.

## Commands

```sh
scrobbler run              # poll and scrobble (the image's default)
scrobbler auth             # authorise with Last.fm, print LASTFM_SESSION_KEY
scrobbler history          # fetch and print the current history once
scrobbler list [status]    # plays by status (default pending)
scrobbler send [-dry-run]  # scrobble pending plays now
```

With Docker: `docker compose run --rm scrobbler list review`, etc.

Each play has a status: `playing` until it ends, then `pending` (to scrobble),
`skipped` (ran too short), or `review` (artist/title couldn't be worked out);
pending plays become `sent`, `rejected` (Last.fm ignored them; the reason is
shown) or `expired` (older than Last.fm's 14-day limit).

## Alerts

With `NTFY_URL` set, you get one notification when a problem starts and one
when it clears, never one per failed poll:

- **YT Music cookie expired** (urgent, right away).
- **Polling failing** for 30 minutes (network or YT Music errors).
- **Play needs review**: artist/title couldn't be worked out (low priority).
- **Last.fm access lost** (urgent): invalid session or API key.
- **Scrobbling failing** for 6 hours; plays stay pending meanwhile.
- **Plays not scrobbled**: Last.fm rejected some, or they expired.

ntfy.sh topics are public: anyone who knows the name can read them, so use a
long random one. A process that dies or hangs can't alert about itself; that's
what `HEARTBEAT_URL` is for.

## How it works

YT Music has no scrobbling API, so this works from the listening history
(`FEmusic_history`, the same request the web app makes), authenticated with a
browser cookie. The history is a list of the last 200 tracks, grouped into
Today / Yesterday / …, **without timestamps**. Everything else is inferred by
polling:

- An entry appears when a song **starts**. Polling every 30 s while something
  may be playing (3 min otherwise) gives each play a start window about 30 s
  wide; the scrobble timestamp is its middle.
- Replaying an older song moves it to the top, which counts as a new play.
- How long a play ran is bounded by when the next one started. With 30 s
  polling the bounds are about ±1 min, enough to apply Last.fm's rule (played
  for half its length or 4 minutes) and tell skips from full plays. Plays
  whose bounds straddle the threshold, typically after downtime, are
  scrobbled anyway and flagged as inferred (see `SCROBBLE_UNSURE`).
- The last play of a session has no next play; it's assumed to have played
  through once it must have ended.
- Album tracks have clean metadata. Official videos get suffixes like
  `(Official Video)` stripped. User uploads put the channel in the artist
  field and `Artist - Title` in the title, so artist and title are parsed out
  of the title; when that fails the play goes to `review` instead of being
  scrobbled wrong. Collaborations are scrobbled under the first artist (see
  `ARTIST_MODE`).

### Limitations

- **Playing the same song twice in a row is invisible**: the top entry doesn't
  change, and nothing in the history can tell.
- A pause makes a play look longer than it was, so a paused-then-skipped song
  can count as played. The `skip` verdict itself is reliable.
- Plays made while the scrobbler is down are scrobbled when it comes back, but
  with wide start windows: their timestamps are approximate and skips can't be
  told apart.
- Podcasts aren't scrobbled.

### Why a cookie

- OAuth (a Google Cloud TV client) logs in, but every YT Music endpoint
  rejects the token with `400 INVALID_ARGUMENT`; the plain YouTube history it
  can read doesn't include YT Music plays.
- A browser extension pushing cookies would share the live browser session.
- A headless browser is blocked from logging in, and is heavy and fragile.

An expired cookie still gets HTTP 200, with `logged_in: "0"` and a "Sign in to
view your history" message; that's detected and alerted on.

## Development

```sh
direnv allow    # or: nix develop — provides go, gopls, sqlite
go test ./...
go run ./cmd/scrobbler history    # uses ./data and data/cookie.txt by default
```

| | |
|---|---|
| `internal/ytm/` | YT Music client: cookie auth (SAPISIDHASH), history fetch and parser |
| `internal/diff/` | detects new plays between two history snapshots |
| `internal/playtime/` | bounds when plays started and how long they ran; Last.fm scrobble rule |
| `internal/meta/` | turns history entries into artist/track/album (parses upload titles) |
| `internal/poller/` | polls the history, detects plays, fills the store |
| `internal/store/` | SQLite store of plays and their status |
| `internal/lastfm/` | Last.fm client: desktop auth flow, signed batched `track.scrobble` |
| `internal/send/` | sends pending plays: batches, retries, expiry, per-play results |
| `internal/notify/`, `internal/alert/` | ntfy client; one alert when a problem starts, one when it clears |
| `cmd/scrobbler/` | the CLI; configuration in `config.go` |

The data directory also holds `events.jsonl` (one line per detected play,
ended play, session end or resync), `last.json` (the previous snapshot, so
restarts resume) and `raw/` (raw responses from fresh starts, resyncs and
errors, kept for `RAW_RETENTION`).

Releases: pushing a `v*.*.*` tag builds the multi-arch image and pushes it to
Docker Hub (see `.github/workflows/release.yml`).

## License

[MIT](LICENSE)
