// Package meta turns YT Music history entries into the artist/track/album
// that get scrobbled.
//
// Album tracks (ATV) have clean metadata and are used as is. Official videos
// (OMV) have the right artist and title but no album, and sometimes a
// "(Official Video)" style suffix. Uploads (UGC) put the uploading channel in
// the artist field and "Artist - Title (Lyrics)" in the title, so artist and
// title are parsed out of the title.
package meta

import (
	"regexp"
	"strings"

	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

// Scrobble is what gets sent to Last.fm for a play.
type Scrobble struct {
	Artist string `json:"artist"`
	Track  string `json:"track"`
	Album  string `json:"album,omitempty"`
	// Parsed means artist and track were parsed out of an upload's title
	// rather than taken from YT Music's metadata.
	Parsed bool `json:"parsed,omitempty"`
}

// noise matches a bracketed suffix that describes the video rather than the
// song, e.g. "(Official Music Video)", "[Lyrics]", "(Visualizer)".
var noise = regexp.MustCompile(`(?i)\s*[(\[][^()\[\]]*\b(official|video|audio|lyrics?|visuali[sz]er|hd|hq|4k|mv)\b[^()\[\]]*[)\]]`)

// separators split "Artist - Title" in upload titles.
var separators = []string{" - ", " – ", " — "}

// Clean returns what to scrobble for t. ok is false when there is no
// trustworthy artist and title, e.g. an upload whose title has no
// "Artist - Title" form, or a podcast episode.
func Clean(t ytm.Track) (s Scrobble, ok bool) {
	switch t.VideoType {
	case "MUSIC_VIDEO_TYPE_UGC":
		title := stripNoise(t.Title)
		for _, sep := range separators {
			artist, track, found := strings.Cut(title, sep)
			if found {
				s = Scrobble{Artist: strings.TrimSpace(artist), Track: strings.TrimSpace(track), Parsed: true}
				return s, s.Artist != "" && s.Track != ""
			}
		}
		return Scrobble{}, false
	case "MUSIC_VIDEO_TYPE_PODCAST_EPISODE":
		return Scrobble{}, false
	}
	if len(t.Artists) == 0 {
		return Scrobble{}, false
	}
	// Collaborations list several artists; Last.fm files the track under the
	// primary one.
	s = Scrobble{Artist: t.Artists[0], Track: t.Title, Album: t.Album}
	if t.VideoType == "MUSIC_VIDEO_TYPE_OMV" {
		s.Track = stripNoise(s.Track)
	}
	return s, s.Artist != "" && s.Track != ""
}

func stripNoise(title string) string {
	return strings.TrimSpace(noise.ReplaceAllString(title, ""))
}
