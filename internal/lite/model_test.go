package lite

import (
	"testing"
	"time"
)

func TestLinkedinOrderingConcrete(t *testing.T) {
	wr := RankKeyFor(MemoryRow{
		MemoryID: "war", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.5,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "PROJECT_DECISION",
	}, MatchResult{Class: MatchSubject, Strength: 8.0 / 78.0}, 0)
	yc := RankKeyFor(MemoryRow{
		MemoryID: "yc", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.6,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "USER_PREFERENCE",
	}, MatchResult{Class: MatchContent, Strength: 8.0 / 60.0}, 0)
	t.Logf("war class=%d strength=%.4f | yc class=%d strength=%.4f", wr.Class, wr.Strength, yc.Class, yc.Strength)
	t.Logf("lessKey(wr, yc) = %v (want true: class 4 > class 3)", lessKey(wr, yc))
	if !lessKey(wr, yc) {
		t.Fatal("war-room (class 4) must outrank example-person (class 3)")
	}
}

func TestClassifyRealRows(t *testing.T) {
	cases := []struct {
		subject, content, query string
		wantClass               MatchClass
	}{
		{"LinkedIn war-room confirmed plan: account managed by DeepSeek Harness effective 23 Aug 2026", "Remember that I confirm saving the LinkedIn findings", "linkedin", MatchSubject},
		{"Example Person 的中文名是示例用户", "记住：我的用户 Example Person 的中文名是示例用户，示例 LinkedIn 是 https://linkedin.example/example-person", "linkedin", MatchContent},
	}
	for _, c := range cases {
		got := ClassifyMatch(MemoryRow{Subject: c.subject, Content: c.content}, c.query)
		if got.Class != c.wantClass {
			t.Errorf("query=%q subject=%q → class=%d want=%d", c.query, c.subject[:20], got.Class, c.wantClass)
		}
	}
}

func TestIdentityCoreRanksBeforeProjectAtSameClass(t *testing.T) {
	// Same match class (Fuzzy), project record has HIGHER raw strength:
	// identity must still win because identity-core sorts before strength.
	identity := RankKeyFor(MemoryRow{
		MemoryID: "pref", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.4,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "USER_PREFERENCE", ScopeType: "GLOBAL",
	}, MatchResult{Class: MatchFuzzy, Strength: 0.467}, 0)
	project := RankKeyFor(MemoryRow{
		MemoryID: "doc", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.4,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "DOCUMENT_FACT",
	}, MatchResult{Class: MatchFuzzy, Strength: 0.53}, 0)
	if identity.IdentityCoreGlobal != 2 || project.IdentityCoreGlobal != 0 {
		t.Fatalf("identity flags: pref=%d doc=%d", identity.IdentityCoreGlobal, project.IdentityCoreGlobal)
	}
	if !lessKey(identity, project) {
		t.Fatal("identity-core memory must outrank project memory at same class despite lower strength")
	}
	// Higher class still beats identity: exact match on a project memory
	// must outrank fuzzy identity.
	exactProject := RankKeyFor(MemoryRow{
		MemoryID: "doc2", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.4,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "DOCUMENT_FACT",
	}, MatchResult{Class: MatchExact, Strength: 1.0}, 0)
	if !lessKey(exactProject, identity) {
		t.Fatal("exact project match must outrank fuzzy identity match")
	}
}

func TestMatchStrengthCJKSingleTokenIgnoresFieldLength(t *testing.T) {
	short := MatchStrength("双语宝宝", "我是双语宝宝")
	long := MatchStrength("双语宝宝", "我的中文名是余烬 我是中国人 我是双语宝宝")
	if short != long {
		t.Fatalf("single CJK token must not penalize longer field: short=%.4f long=%.4f", short, long)
	}
	if short < 0.8 {
		t.Fatalf("single CJK token full match should score high: %.4f", short)
	}
}

func TestFindDuplicateSubject(t *testing.T) {
	base := MemoryRow{MemoryID: "m1", Subject: "LinkedIn content compliance rules (war-room): no free unsolicited price offers", ScopeType: "GLOBAL", Lifecycle: "ACTIVE"}
	rows := []MemoryRow{base}
	// exact duplicate (same scope) -> detected
	if got := findDuplicateSubject(rows, "LinkedIn content compliance rules (war-room): no free unsolicited price offers", "GLOBAL"); got != "m1" {
		t.Fatalf("exact duplicate not detected: %q", got)
	}
	// punctuation/whitespace variant -> detected after normalization
	if got := findDuplicateSubject(rows, "LinkedIn content compliance rules (war-room):no free unsolicited price offers", "GLOBAL"); got != "m1" {
		t.Fatalf("punctuation variant not detected: %q", got)
	}
	// different scope -> not detected
	if got := findDuplicateSubject(rows, "LinkedIn content compliance rules (war-room): no free unsolicited price offers", "PROJECT"); got != "" {
		t.Fatalf("cross-scope should not dedupe: %q", got)
	}
	// genuinely different subject -> not detected
	if got := findDuplicateSubject(rows, "我的中文名是余烬 我是中国人 我是双语宝宝", "GLOBAL"); got != "" {
		t.Fatalf("distinct subject flagged as duplicate: %q", got)
	}
}

func TestNormalizeForDedupe(t *testing.T) {
	a := normalizeForDedupe("LinkedIn compliance (war-room): no free offers")
	b := normalizeForDedupe("linkedin compliance (war room) : no free offers")
	if a != b {
		t.Fatalf("normalization mismatch: %q vs %q", a, b)
	}
}

func TestPromoteImportanceLadder(t *testing.T) {
	cases := []struct{ from, want float64 }{
		{0.2, 0.4}, {0.4, 0.6}, {0.6, 0.8}, {0.8, 1.0}, {1.0, 1.0},
	}
	for _, c := range cases {
		if got := promoteImportance(c.from); got != c.want {
			t.Errorf("promoteImportance(%v) = %v, want %v", c.from, got, c.want)
		}
	}
}

func TestMixedTokenSubjectMatch(t *testing.T) {
	row := MemoryRow{Subject: "Example Person 的中文名是示例用户，LinkedIn 是 https://linkedin.example/example-person",
		Content: "记住：我的用户 Example Person 的中文名是示例用户", Kind: "USER_PREFERENCE", ScopeType: "GLOBAL", Lifecycle: "ACTIVE",
		Confidence: 1, Importance: 0.5, Activation: 1}
	m := ClassifyMatch(row, "示例用户 linkedin")
	if m.Class != MatchSubject {
		t.Fatalf("mixed query should be Subject match, got class=%d str=%.4f", m.Class, m.Strength)
	}
	// Query with a token NOT in the subject must not match.
	m2 := ClassifyMatch(row, "示例用户 fertilizer")
	if m2.Class != MatchNone && m2.Class != MatchFuzzy {
		t.Fatalf("absent token should not produce subject match, got class=%d", m2.Class)
	}
}

func TestCJKPrefixRecovery(t *testing.T) {
	// The index layer (SearchCandidates) truncates CJK prefixes so a query
	// with extra words ("薪尽火传的传") still finds "薪尽火传". The helper
	// that drives that — and the scoring fallback — is cjkPrefixes.
	prefixes := cjkPrefixes("薪尽火传的传")
	// 6 CJK runes -> prefixes from len-1 down to 2: 4 prefixes.
	if len(prefixes) != 4 {
		t.Fatalf("cjkPrefixes length = %d, want 4 (%v)", len(prefixes), prefixes)
	}
	if prefixes[0] != "薪尽火传的" || prefixes[1] != "薪尽火传" {
		t.Fatalf("prefixes = %v, want leading 薪尽火传的, 薪尽火传", prefixes)
	}
	// Short CJK query yields no prefixes.
	if p := cjkPrefixes("余烬"); len(p) != 0 {
		t.Fatalf("short query should have no prefixes, got %v", p)
	}
	// The scoring layer gates prefix usage on isAllCJK; the helper itself
	// may return the CJK run's prefixes for a mixed query, but the caller
	// decides. Verify the gate.
	if !isAllCJK("薪尽火传的传") {
		t.Fatal("all-CJK query must pass isAllCJK")
	}
	if isAllCJK("示例用户 linkedin") {
		t.Fatal("mixed query must fail isAllCJK")
	}
	if isAllCJK("") {
		t.Fatal("empty query must fail isAllCJK")
	}
}

func TestGlobalIdentityOutranksProjectScopedAtSameClass(t *testing.T) {
	// Same match class (Subject), same raw strength: GLOBAL identity
	// ("我是双语宝宝") must outrank PROJECT-scoped identity note
	// ("中文风格设定(双语宝宝)") — the note merely contains the term.
	global := RankKeyFor(MemoryRow{
		MemoryID: "g", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.5,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "USER_PREFERENCE", ScopeType: "GLOBAL",
	}, MatchResult{Class: MatchSubject, Strength: 0.891}, 0)
	proj := RankKeyFor(MemoryRow{
		MemoryID: "p", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.5,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "USER_PREFERENCE", ScopeType: "PROJECT",
	}, MatchResult{Class: MatchSubject, Strength: 0.940}, 0)
	if global.IdentityCoreGlobal != 2 || proj.IdentityCoreGlobal != 1 {
		t.Fatalf("flags: global=%d proj=%d", global.IdentityCoreGlobal, proj.IdentityCoreGlobal)
	}
	if !lessKey(global, proj) {
		t.Fatal("GLOBAL identity must outrank PROJECT-scoped note at same class even with lower strength")
	}
	// But a strictly higher class still wins (exact subject match).
	exactProj := RankKeyFor(MemoryRow{
		MemoryID: "ep", Lifecycle: "ACTIVE", Confidence: 1.0, Importance: 0.5,
		Activation: 1.0, UpdatedAt: time.Now(), Kind: "PROJECT_DECISION", ScopeType: "PROJECT",
	}, MatchResult{Class: MatchExact, Strength: 1.0}, 0)
	if !lessKey(exactProj, global) {
		t.Fatal("exact class match must outrank fuzzy identity")
	}
}
