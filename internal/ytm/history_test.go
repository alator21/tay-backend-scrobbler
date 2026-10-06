package ytm

import (
	"errors"
	"testing"
)

const sectionList = `"contents":{"singleColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":`

func TestParseHistoryLoggedOut(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"logged_in param", `{"responseContext":{"serviceTrackingParams":[{"service":"GFEEDBACK","params":[{"key":"logged_in","value":"0"}]}]},` +
			sectionList + `[]}}}}]}}}`},
		{"only a message", `{` + sectionList +
			`[{"itemSectionRenderer":{"contents":[{"messageRenderer":{"text":{"runs":[{"text":"Sign in to view your history"}]}}}]}}]}}}}]}}}`},
		{"no section list", `{"contents":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseHistory([]byte(tt.raw)); !errors.Is(err, ErrNoHistory) {
				t.Errorf("err = %v, want ErrNoHistory", err)
			}
		})
	}
}

func TestParseHistory(t *testing.T) {
	raw := `{"responseContext":{"serviceTrackingParams":[{"params":[{"key":"logged_in","value":"1"}]}]},` + sectionList +
		`[{"musicShelfRenderer":{"title":{"runs":[{"text":"Today"}]},"contents":[{"musicResponsiveListItemRenderer":{
			"playlistItemData":{"videoId":"EOCZUBWkLfQ"},
			"flexColumns":[
				{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Careful"}]}}},
				{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[
					{"text":"Paramore","navigationEndpoint":{"browseEndpoint":{"browseEndpointContextSupportedConfigs":{"browseEndpointContextMusicConfig":{"pageType":"MUSIC_PAGE_TYPE_ARTIST"}}}}}]}}},
				{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[
					{"text":"Brand New Eyes","navigationEndpoint":{"browseEndpoint":{"browseEndpointContextSupportedConfigs":{"browseEndpointContextMusicConfig":{"pageType":"MUSIC_PAGE_TYPE_ALBUM"}}}}}]}}}],
			"fixedColumns":[{"musicResponsiveListItemFixedColumnRenderer":{"text":{"runs":[{"text":"3:51"}]}}}]}}]}}]}}}}]}}}`
	tracks, err := ParseHistory([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := Track{VideoID: "EOCZUBWkLfQ", Title: "Careful", Artists: []string{"Paramore"}, Album: "Brand New Eyes", DurationSec: 231, Section: "Today"}
	if len(tracks) != 1 || tracks[0].Title != want.Title || tracks[0].Album != want.Album || tracks[0].DurationSec != want.DurationSec ||
		tracks[0].Section != want.Section || len(tracks[0].Artists) != 1 || tracks[0].Artists[0] != "Paramore" {
		t.Errorf("tracks = %+v", tracks)
	}
}
