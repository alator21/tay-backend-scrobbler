package ytm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ErrNoHistory means the response had no history shelves. With a 200
// response this usually means the cookie is no longer logged in.
var ErrNoHistory = errors.New("ytm: no history in response")

// Track is one entry of the listening history, newest first.
type Track struct {
	VideoID     string   `json:"videoId"`
	Title       string   `json:"title"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album,omitempty"`
	DurationSec int      `json:"durationSec"`
	// VideoType is e.g. MUSIC_VIDEO_TYPE_ATV (audio track), _OMV (official
	// video), _UGC (user upload) or _PODCAST_EPISODE.
	VideoType string `json:"videoType,omitempty"`
	// Section is the shelf the entry was listed under ("Today", "Yesterday", ...).
	Section string `json:"section"`
}

// History fetches the listening history. The raw response is returned even
// when parsing fails, so it can be saved for debugging.
func (c *Client) History(ctx context.Context) ([]byte, []Track, error) {
	raw, err := c.Browse(ctx, "FEmusic_history")
	if err != nil {
		return nil, nil, err
	}
	tracks, err := ParseHistory(raw)
	return raw, tracks, err
}

// ParseHistory extracts tracks from a FEmusic_history browse response.
func ParseHistory(raw []byte) ([]Track, error) {
	if !gjson.ValidBytes(raw) {
		return nil, errors.New("ytm: history response is not valid JSON")
	}
	sections := gjson.GetBytes(raw, "contents.singleColumnBrowseResultsRenderer.tabs.0.tabRenderer.content.sectionListRenderer.contents")
	if !sections.Exists() {
		return nil, fmt.Errorf("%w: no section list", ErrNoHistory)
	}
	// A logged-out request still gets a 200 with a section list, holding only
	// a "Sign in to view your history" message.
	for _, p := range gjson.GetBytes(raw, "responseContext.serviceTrackingParams.#.params|@flatten").Array() {
		if p.Get("key").String() == "logged_in" && p.Get("value").String() == "0" {
			return nil, fmt.Errorf("%w: not logged in", ErrNoHistory)
		}
	}

	var tracks []Track
	shelves, message := 0, ""
	for _, section := range sections.Array() {
		shelf := section.Get("musicShelfRenderer")
		if !shelf.Exists() {
			if msg := section.Get("musicNotifierShelfRenderer.title.runs.0.text"); msg.Exists() {
				return nil, fmt.Errorf("%w: %s", ErrNoHistory, msg.String())
			}
			if msg := section.Get("itemSectionRenderer.contents.0.messageRenderer.text.runs.0.text"); msg.Exists() {
				message = msg.String()
			}
			continue
		}
		shelves++
		label := shelf.Get("title.runs.0.text").String()
		for _, item := range shelf.Get("contents").Array() {
			r := item.Get("musicResponsiveListItemRenderer")
			if !r.Exists() {
				continue
			}
			t := parseItem(r)
			if t.VideoID == "" {
				continue
			}
			t.Section = label
			tracks = append(tracks, t)
		}
	}
	if shelves == 0 {
		return nil, fmt.Errorf("%w: no history shelf (%s)", ErrNoHistory, message)
	}
	return tracks, nil
}

func parseItem(r gjson.Result) Track {
	watch := r.Get("overlay.musicItemThumbnailOverlayRenderer.content.musicPlayButtonRenderer.playNavigationEndpoint.watchEndpoint")
	t := Track{
		VideoID:   r.Get("playlistItemData.videoId").String(),
		VideoType: watch.Get("watchEndpointMusicSupportedConfigs.watchEndpointMusicConfig.musicVideoType").String(),
	}
	if t.VideoID == "" {
		t.VideoID = watch.Get("videoId").String()
	}

	flex := r.Get("flexColumns").Array()
	if len(flex) > 0 {
		t.Title = flex[0].Get("musicResponsiveListItemFlexColumnRenderer.text.runs.0.text").String()
	}
	// Artists and album are identified by the page type of their links rather
	// than by column position, which differs between songs and videos.
	for _, col := range flex[min(1, len(flex)):] {
		for _, run := range col.Get("musicResponsiveListItemFlexColumnRenderer.text.runs").Array() {
			pageType := run.Get("navigationEndpoint.browseEndpoint.browseEndpointContextSupportedConfigs.browseEndpointContextMusicConfig.pageType").String()
			switch pageType {
			case "MUSIC_PAGE_TYPE_ARTIST", "MUSIC_PAGE_TYPE_USER_CHANNEL":
				t.Artists = append(t.Artists, run.Get("text").String())
			case "MUSIC_PAGE_TYPE_ALBUM":
				t.Album = run.Get("text").String()
			}
		}
	}
	if len(t.Artists) == 0 && len(flex) > 1 {
		// Unlinked artist, common for uploads: take the first run of the second column.
		if a := flex[1].Get("musicResponsiveListItemFlexColumnRenderer.text.runs.0.text").String(); a != "" {
			t.Artists = []string{a}
		}
	}

	fixed := r.Get("fixedColumns.0.musicResponsiveListItemFixedColumnRenderer.text")
	dur := fixed.Get("runs.0.text").String()
	if dur == "" {
		dur = fixed.Get("simpleText").String()
	}
	t.DurationSec = parseDuration(dur)
	return t
}

// parseDuration converts "3:45" or "1:02:03" to seconds, returning 0 if unparseable.
func parseDuration(s string) int {
	if s == "" {
		return 0
	}
	total := 0
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}
