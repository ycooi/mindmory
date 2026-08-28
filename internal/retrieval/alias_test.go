package retrieval

import (
	"slices"
	"testing"
)

func TestAliasExpanderExpandsKnownEntity(t *testing.T) {
	x := NewAliasExpander(nil)
	got := x.Expand("the warmth that waits on the near bank")
	if !slices.Contains(got, "余烬永温") {
		t.Fatalf("expected 余烬永温 in expansion, got %v", got)
	}
	if got[0] != "the warmth that waits on the near bank" {
		t.Fatalf("original query must be first, got %v", got)
	}
}

func TestAliasExpanderMultipleEntities(t *testing.T) {
	x := NewAliasExpander(nil)
	// A query can reference two entities at once; both canonicals appear.
	got := x.Expand("the ember and the fire of the ashes")
	if !slices.Contains(got, "余烬") {
		t.Fatalf("expected 余烬, got %v", got)
	}
	if !slices.Contains(got, "薪尽火传") {
		t.Fatalf("expected 薪尽火传, got %v", got)
	}
}

func TestAliasExpanderNoMatchIsIdentity(t *testing.T) {
	x := NewAliasExpander(nil)
	got := x.Expand("what is the weather like today")
	if len(got) != 1 || got[0] != "what is the weather like today" {
		t.Fatalf("unrelated query must pass through unchanged, got %v", got)
	}
}

func TestAliasExpanderCaseInsensitive(t *testing.T) {
	x := NewAliasExpander(nil)
	got := x.Expand("EMBER KEEPS THE WARMTH FOREVER")
	if !slices.Contains(got, "余烬永温") {
		t.Fatalf("case-insensitive match failed, got %v", got)
	}
}

func TestAliasExpanderContainedPhrase(t *testing.T) {
	x := NewAliasExpander(nil)
	// Alias embedded in a longer sentence still fires.
	got := x.Expand("please explain to me what the warmth that waits on the near bank means")
	if !slices.Contains(got, "余烬永温") {
		t.Fatalf("contained alias should fire, got %v", got)
	}
}

func TestAliasExpanderShortTokenNeverFiresAlone(t *testing.T) {
	x := NewAliasExpander(nil)
	// "ember" is a single-word alias (len 5 >= 5) but the token-subset rule
	// is deliberately conservative for short aliases: it should NOT fire on
	// "ember of truth" because 余烬永温 would be a wrong recovery.
	got := x.Expand("ember of truth")
	if slices.Contains(got, "余烬") {
		t.Fatalf("short alias fired on unrelated query: %v", got)
	}
}

func TestAliasExpanderEmptyAndNil(t *testing.T) {
	if got := NewAliasExpander([]AliasEntry{}).Expand("  "); len(got) != 1 {
		t.Fatalf("blank query must stay identity, got %v", got)
	}
	var nilX *AliasExpander
	if got := nilX.Expand("anything at all"); len(got) != 1 || got[0] != "anything at all" {
		t.Fatalf("nil expander must be identity, got %v", got)
	}
}

func TestDefaultAliasesNeverEmpty(t *testing.T) {
	if len(DefaultAliases()) == 0 {
		t.Fatal("built-in alias table must not be empty")
	}
	for _, e := range DefaultAliases() {
		if e.Canonical == "" {
			t.Fatal("alias entry with empty canonical")
		}
		if len(e.Aliases) == 0 {
			t.Fatalf("alias entry %q with no aliases", e.Canonical)
		}
	}
}

func TestAliasExpanderReverseDirection(t *testing.T) {
	x := NewAliasExpander(nil)
	// Chinese query expands to the English aliases that match stored subjects.
	got := x.Expand("示例用户")
	if !slices.Contains(got, "example person") {
		t.Fatalf("expected 'example person' expansion from 示例用户, got %v", got)
	}
	// 余烬永温 expands to its English paraphrases.
	got2 := x.Expand("余烬永温")
	if !slices.Contains(got2, "the warmth that waits on the near bank") {
		t.Fatalf("expected English alias expansion from 余烬永温, got %v", got2)
	}
	// 杭州 expands to hangzhou.
	got3 := x.Expand("杭州")
	if !slices.Contains(got3, "hangzhou") {
		t.Fatalf("expected hangzhou expansion, got %v", got3)
	}
	// Original query still first.
	if got[0] != "示例用户" {
		t.Fatalf("original query must be first, got %v", got)
	}
}
