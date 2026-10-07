package diff

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func ids(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, " ")
}

func TestNewPlays(t *testing.T) {
	tests := []struct {
		name           string
		prev, curr     string
		want           int
		wantRemoved    string
		wantReappeared string
		wantErr        error
	}{
		{name: "no change", prev: "a b c", curr: "a b c", want: 0},
		{name: "one new", prev: "a b c", curr: "x a b c", want: 1},
		{name: "two new", prev: "a b c", curr: "y x a b c", want: 2},
		{name: "replay of older entry moves it to top", prev: "a b c", curr: "c a b", want: 1},
		{name: "new plus replay", prev: "a b c d", curr: "b x a c d", want: 2},
		{name: "replay of top entry is invisible", prev: "a b c", curr: "a b c", want: 0},
		{name: "list truncated at end", prev: "a b c d", curr: "x a b c", want: 1},
		{name: "replay of second entry", prev: "a b", curr: "b a", want: 1},
		{name: "empty prev", prev: "", curr: "a b", want: 2},
		{name: "unrelated lists", prev: "a b c", curr: "x y z", wantErr: ErrNoMatch},
		{name: "everything removed", prev: "a b c", curr: "x", wantErr: ErrNoMatch},
		{name: "entry removed from middle", prev: "a b c d", curr: "a c d", want: 0, wantRemoved: "b"},
		{name: "top entry removed", prev: "a b c", curr: "b c", want: 0, wantRemoved: "a"},
		{name: "new plays plus removal", prev: "a b c d e", curr: "y x a b d e", want: 2, wantRemoved: "c"},
		{name: "removal plus truncation", prev: "a b c d e", curr: "x a c d", want: 1, wantRemoved: "b"},
		{name: "removed and replayed", prev: "a b c d", curr: "c x a d", want: 2, wantRemoved: "b"},
		{name: "entry back in the middle", prev: "a c d", curr: "a b c d", want: 0, wantReappeared: "b"},
		{name: "block back in the middle", prev: "a d e", curr: "a b c d e", want: 0, wantReappeared: "b c"},
		{name: "new play plus entry back", prev: "a c d", curr: "x a b c d", want: 1, wantReappeared: "b"},
		{name: "new play, replay and entry back", prev: "a b d e", curr: "d x a b c e", want: 2, wantReappeared: "c"},
		{name: "entry back plus removal", prev: "a c d e", curr: "a b c e", want: 0, wantRemoved: "d", wantReappeared: "b"},
		{name: "longer list ending in unseen entries", prev: "a b", curr: "a b c d", want: 0},
		{name: "unseen entry on top is a new play", prev: "a b c", curr: "x a b c", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewPlays(ids(tt.prev), ids(tt.curr))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.New != tt.want {
				t.Errorf("New = %d, want %d", got.New, tt.want)
			}
			if !slices.Equal(got.Removed, ids(tt.wantRemoved)) {
				t.Errorf("Removed = %v, want %v", got.Removed, ids(tt.wantRemoved))
			}
			if !slices.Equal(got.Reappeared, ids(tt.wantReappeared)) {
				t.Errorf("Reappeared = %v, want %v", got.Reappeared, ids(tt.wantReappeared))
			}
		})
	}
}

// history returns n entry IDs, newest first.
func history(n int) []string {
	h := make([]string, n)
	for i := range h {
		h[i] = fmt.Sprintf("v%03d", i)
	}
	return h
}

// without returns h without the entries at positions [from, to).
func without(h []string, from, to int) []string {
	return slices.Concat(h[:from], h[to:])
}

// YT Music dropped a block of 5 old entries from a 200-entry response and
// listed them again in the next one. On 2026-10-07 this made 122 old entries
// count as new plays.
func TestBlockDroppedAndBack(t *testing.T) {
	full := history(200)
	short := without(full, 124, 129)

	got, err := NewPlays(full, short)
	if err != nil || got.New != 0 || !slices.Equal(got.Removed, full[124:129]) {
		t.Fatalf("dropped: got %+v, %v; want 0 new, %v removed", got, err, full[124:129])
	}

	got, err = NewPlays(short, full)
	if err != nil || got.New != 0 || got.Removed != nil || !slices.Equal(got.Reappeared, full[124:129]) {
		t.Errorf("back: got %+v, %v; want 0 new, %v reappeared", got, err, full[124:129])
	}
}

// An old entry missing from earlier responses came back at position 105
// along with one real play. On 2026-10-07 this made 106 plays out of one.
func TestEntryBackWithNewPlay(t *testing.T) {
	full := history(200)
	prev := without(full, 104, 105)
	curr := slices.Concat([]string{"new"}, full[:199])

	got, err := NewPlays(prev, curr)
	if err != nil || got.New != 1 || !slices.Equal(got.Reappeared, full[104:105]) {
		t.Errorf("got %+v, %v; want 1 new, %v reappeared", got, err, full[104:105])
	}
}
