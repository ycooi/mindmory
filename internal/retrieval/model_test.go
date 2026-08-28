package retrieval

import (
	"testing"

	"mindmory.local/core/internal/artifact/policy"
	"mindmory.local/core/internal/memory"
)

func TestMemoryEligibilityEnforcesProjectLifecycleAndSensitivityBeforeRanking(t *testing.T) {
	session := SessionScope{ProjectKey: "Mindmory"}
	tests := []struct {
		name        string
		scope       memory.ScopeType
		project     string
		lifecycle   memory.Lifecycle
		sensitivity policy.Sensitivity
		historical  bool
		want        bool
	}{
		{"same project active", memory.ScopeProject, "Mindmory", memory.LifecycleActive, policy.SensitivityNormal, false, true},
		{"global active", memory.ScopeGlobal, "", memory.LifecycleActive, policy.SensitivityNormal, false, true},
		{"cross project", memory.ScopeProject, "FertAgent", memory.LifecycleActive, policy.SensitivityNormal, false, false},
		{"superseded current search", memory.ScopeProject, "Mindmory", memory.LifecycleSuperseded, policy.SensitivityNormal, false, false},
		{"superseded explicit history", memory.ScopeProject, "Mindmory", memory.LifecycleSuperseded, policy.SensitivityNormal, true, true},
		{"sensitive", memory.ScopeProject, "Mindmory", memory.LifecycleActive, policy.SensitivitySensitive, false, false},
		{"secret history", memory.ScopeGlobal, "", memory.LifecycleForgotten, policy.SensitivitySecret, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MemoryEligible(session, test.scope, test.project, test.lifecycle, test.sensitivity, test.historical); got != test.want {
				t.Fatalf("eligible=%t want=%t", got, test.want)
			}
		})
	}
}

func TestSearchRequestBounds(t *testing.T) {
	valid := SearchRequest{SessionID: "session", Query: "MCP 优先 Go"}
	if err := valid.Validate(); err != nil || valid.EffectiveLimit() != DefaultSearchLimit {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, invalid := range []SearchRequest{
		{SessionID: "session", Query: ""},
		{SessionID: "session", Query: string(make([]byte, MaximumQueryBytes+1))},
		{SessionID: "session", Query: "x", Limit: MaximumSearchLimit + 1},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("invalid request accepted: %+v", invalid)
		}
	}
}

func TestEstimatedTokens(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"empty", "", 0},
		{"short ascii", "go", 1},
		{"ascii four chars", "abcd", 1},
		{"ascii eight chars", "abcdefgh", 2},
		{"cjk single", "记", 1},
		{"cjk phrase", "记住以后这个项目优先使用Go", 13}, // 12 CJK runes + ceil(2/4)
		{"mixed", "用 Go 写", 3},               // 2 CJK + ceil(4/4)
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := EstimatedTokens(item.value); got != item.want {
				t.Fatalf("EstimatedTokens(%q)=%d want %d", item.value, got, item.want)
			}
		})
	}
}

func TestContextModeValidationAndDefaults(t *testing.T) {
	for _, mode := range []string{"", ReflexMode, ExplicitMode} {
		request := ContextRequest{SessionID: "00000000-0000-4000-8000-000000000001", Mode: mode}
		if err := request.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	if err := (ContextRequest{SessionID: "00000000-0000-4000-8000-000000000001", Mode: "bogus"}).Validate(); err == nil {
		t.Fatal("bogus mode accepted")
	}
	if (ContextRequest{}).EffectiveMode() != ExplicitMode {
		t.Fatal("default mode must remain explicit")
	}
	if (ContextRequest{Mode: ReflexMode}).EffectiveMode() != ReflexMode {
		t.Fatal("reflex mode not selected")
	}
}
