package strutil

import (
	"reflect"
	"testing"
)

func TestSplitCommaList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "empty string returns nil",
			raw:  "",
			want: nil,
		},
		{
			name: "single element",
			raw:  "a",
			want: []string{"a"},
		},
		{
			name: "multiple elements",
			raw:  "a,b,c",
			want: []string{"a", "b", "c"},
		},
		{
			name: "trims surrounding whitespace",
			raw:  " a , b ,c ",
			want: []string{"a", "b", "c"},
		},
		{
			name: "skips empty elements between commas",
			raw:  "a,,b",
			want: []string{"a", "b"},
		},
		{
			name: "skips whitespace-only elements",
			raw:  "a, ,b",
			want: []string{"a", "b"},
		},
		{
			name: "only commas returns nil",
			raw:  ",,,",
			want: nil,
		},
		{
			name: "whitespace-only input returns nil",
			raw:  "   ",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitCommaList(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitCommaList(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}
