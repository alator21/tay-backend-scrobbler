package diff

import (
	"errors"
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
		name        string
		prev, curr  string
		want        int
		wantRemoved string
		wantErr     error
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed, err := NewPlays(ids(tt.prev), ids(tt.curr))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got != tt.want {
				t.Errorf("NewPlays = %d, want %d", got, tt.want)
			}
			if !slices.Equal(removed, ids(tt.wantRemoved)) {
				t.Errorf("removed = %v, want %v", removed, ids(tt.wantRemoved))
			}
		})
	}
}
