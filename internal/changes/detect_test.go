package changes

import (
	"reflect"
	"testing"

	"github.com/Easlkad/impact/internal/gitdiff"
	"github.com/Easlkad/impact/internal/model"
)

func rng(start, count int) gitdiff.Range { return gitdiff.Range{Start: start, Count: count} }

func TestOutside(t *testing.T) {
	// Functions on lines 5-10 and 15-20.
	decls := []decl{
		{loc: model.Declaration{StartLine: 5, EndLine: 10}},
		{loc: model.Declaration{StartLine: 15, EndLine: 20}},
	}
	tests := []struct {
		name string
		r    gitdiff.Range
		want []gitdiff.Range
	}{
		{"before all functions", rng(1, 3), []gitdiff.Range{rng(1, 3)}},
		{"inside a function", rng(6, 3), nil},
		{"last line of a function", rng(20, 1), nil},
		{"overlapping a function start", rng(3, 5), []gitdiff.Range{rng(3, 2)}},
		{"between two functions", rng(9, 8), []gitdiff.Range{rng(11, 4)}},
		{"spanning everything", rng(1, 25), []gitdiff.Range{rng(1, 4), rng(11, 4), rng(21, 5)}},
		{"after all functions", rng(21, 2), []gitdiff.Range{rng(21, 2)}},
		{"empty range", rng(12, 0), nil},
	}
	for _, tt := range tests {
		if got := outside(tt.r, decls); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: outside(%+v) = %+v, want %+v", tt.name, tt.r, got, tt.want)
		}
	}
}

func TestNonBlank(t *testing.T) {
	// Lines 10-15 of one side of a hunk; 11 and 14 are blank.
	text := []string{"var a = 1", "", "var b = 2", "// note", "\t ", "var c = 3"}
	got := nonBlank([]gitdiff.Range{rng(10, 6)}, 10, text)
	want := []gitdiff.Range{rng(10, 1), rng(12, 2), rng(15, 1)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nonBlank = %+v, want %+v", got, want)
	}
	if got := nonBlank([]gitdiff.Range{rng(11, 1)}, 10, text); got != nil {
		t.Errorf("a lone blank line gave %+v", got)
	}
}

func TestLineChangeString(t *testing.T) {
	if s := (LineChange{File: "a.go", Start: 3, End: 3}).String(); s != "a.go:3" {
		t.Errorf("single line = %q", s)
	}
	if s := (LineChange{File: "a.go", Start: 3, End: 7}).String(); s != "a.go:3-7" {
		t.Errorf("range = %q", s)
	}
}
