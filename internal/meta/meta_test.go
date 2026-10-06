package meta

import (
	"testing"

	"github.com/alator21/tay-backend-scrobbler/internal/ytm"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name   string
		track  ytm.Track
		want   Scrobble
		wantOK bool
	}{
		{
			name:   "album track used as is",
			track:  ytm.Track{Title: "Careful", Artists: []string{"Paramore"}, Album: "Brand New Eyes", VideoType: "MUSIC_VIDEO_TYPE_ATV"},
			want:   Scrobble{Artist: "Paramore", Track: "Careful", Album: "Brand New Eyes"},
			wantOK: true,
		},
		{
			name:   "album track keeps version suffix",
			track:  ytm.Track{Title: "Help! (Remastered 2009)", Artists: []string{"The Beatles"}, Album: "Help!", VideoType: "MUSIC_VIDEO_TYPE_ATV"},
			want:   Scrobble{Artist: "The Beatles", Track: "Help! (Remastered 2009)", Album: "Help!"},
			wantOK: true,
		},
		{
			name:   "collaboration uses first artist",
			track:  ytm.Track{Title: "Song", Artists: []string{"A", "B"}, VideoType: "MUSIC_VIDEO_TYPE_ATV"},
			want:   Scrobble{Artist: "A", Track: "Song"},
			wantOK: true,
		},
		{
			name:   "official video without album",
			track:  ytm.Track{Title: "Thinking", Artists: []string{"Against The Current"}, VideoType: "MUSIC_VIDEO_TYPE_OMV"},
			want:   Scrobble{Artist: "Against The Current", Track: "Thinking"},
			wantOK: true,
		},
		{
			name:   "official video suffix stripped",
			track:  ytm.Track{Title: "Misery Business (Official Music Video) [HD]", Artists: []string{"Paramore"}, VideoType: "MUSIC_VIDEO_TYPE_OMV"},
			want:   Scrobble{Artist: "Paramore", Track: "Misery Business"},
			wantOK: true,
		},
		{
			name:   "upload with lyrics suffix",
			track:  ytm.Track{Title: "Paramore - Temporary (Lyrics)", Artists: []string{"Some Uploader"}, VideoType: "MUSIC_VIDEO_TYPE_UGC"},
			want:   Scrobble{Artist: "Paramore", Track: "Temporary", Parsed: true},
			wantOK: true,
		},
		{
			name:   "upload without suffix",
			track:  ytm.Track{Title: "Paramore - Another Day", Artists: []string{"Bloom"}, VideoType: "MUSIC_VIDEO_TYPE_UGC"},
			want:   Scrobble{Artist: "Paramore", Track: "Another Day", Parsed: true},
			wantOK: true,
		},
		{
			name:   "upload with en dash and feat kept",
			track:  ytm.Track{Title: "Artist – Song (feat. Other) [Official Audio]", Artists: []string{"Channel"}, VideoType: "MUSIC_VIDEO_TYPE_UGC"},
			want:   Scrobble{Artist: "Artist", Track: "Song (feat. Other)", Parsed: true},
			wantOK: true,
		},
		{
			name:   "upload without separator",
			track:  ytm.Track{Title: "my favourite song", Artists: []string{"Channel"}, VideoType: "MUSIC_VIDEO_TYPE_UGC"},
			wantOK: false,
		},
		{
			name:   "podcast episode",
			track:  ytm.Track{Title: "Episode 1", Artists: []string{"Show"}, VideoType: "MUSIC_VIDEO_TYPE_PODCAST_EPISODE"},
			wantOK: false,
		},
		{
			name:   "no artist",
			track:  ytm.Track{Title: "Song", VideoType: "MUSIC_VIDEO_TYPE_ATV"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Clean(tt.track)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
