package retrieval

import (
	"slices"
	"testing"
)

func TestAliasOccurrenceBoundaries(t *testing.T) {
	entries := []AliasEntry{{Canonical: "青岚", Aliases: []string{"short mist name"}}, {Canonical: "青岚计划", Aliases: []string{"blue mist project"}}}
	for _, table := range [][]AliasEntry{entries, {entries[1], entries[0]}} {
		x := NewAliasExpander(table)
		for _, tc := range []struct {
			query        string
			want, absent []string
		}{
			{"请介绍青岚计划的进展", []string{"青岚计划"}, []string{"青岚", "blue mist project", "short mist name"}},
			{"青岚与青岚计划有何区别", []string{"青岚", "青岚计划"}, nil},
			{"青岚计划与青岚有何区别", []string{"青岚", "青岚计划"}, nil},
			{"青，岚计划", nil, []string{"青岚计划", "青岚"}},
			{"青岚计划", []string{"blue mist project"}, []string{"short mist name"}},
		} {
			got := x.Expand(tc.query)
			for _, s := range tc.want {
				if !slices.Contains(got, s) {
					t.Errorf("%q missing %q: %v", tc.query, s, got)
				}
			}
			for _, s := range tc.absent {
				if slices.Contains(got, s) {
					t.Errorf("%q unexpected %q: %v", tc.query, s, got)
				}
			}
		}
	}
}

func TestCJKAliasEmbedded(t *testing.T) {
	x := NewAliasExpander([]AliasEntry{{Canonical: "savings allocation", Aliases: []string{"储蓄如何分配"}}})
	for _, query := range []string{"储蓄如何分配", "请解释储蓄如何分配才合适", "Please explain 储蓄如何分配"} {
		if !slices.Contains(x.Expand(query), "savings allocation") {
			t.Fatal(query)
		}
	}
	if got := x.Expand("储蓄如何，分配"); len(got) != 1 {
		t.Fatal(got)
	}
}
