package retrieval

import "testing"

func TestEffectiveHeatDecayTowardFloor(t *testing.T) {
	cases := []struct {
		name          string
		importance    float64
		heat          float64
		sessionsSince int64
		identity      bool
		wantFloor     bool // true when result should sit at/near the floor
		wantBounded   bool
	}{
		{"fresh warm memory barely decays", 0.4, 0.9, 1, false, false, true},
		{"long neglect decays toward floor", 0.4, 0.9, 500, false, true, true},
		{"critical floor never decays to zero", 1.0, 1.0, 100000, false, false, true},
		{"identity exempt from neglect", 0.8, 0.9, 100000, true, false, true},
		{"heat at floor stays", 0.4, 0.4, 1000, false, false, true},
		{"heat below floor stays (feedback)", 0.4, 0.1, 0, false, false, true},
	}
	for _, item := range cases {
		got := EffectiveHeat(item.importance, item.heat, item.sessionsSince, item.identity)
		if got < 0 || got > 1 {
			t.Fatalf("%s: EffectiveHeat=%v out of [0,1]", item.name, got)
		}
		if item.wantBounded {
			floor := item.importance
			if got < floor && item.heat >= floor {
				t.Fatalf("%s: heat fell below floor: %v < %v", item.name, got, floor)
			}
		}
		if item.wantFloor {
			floor := item.importance
			if got > floor+0.05 {
				t.Fatalf("%s: expected decay near floor %v, got %v", item.name, floor, got)
			}
		}
	}
}

func TestEffectiveHeatIdentityExemption(t *testing.T) {
	// Identity memory: no decay regardless of sessions.
	plain := EffectiveHeat(0.6, 0.9, 100000, false)
	identity := EffectiveHeat(0.6, 0.9, 100000, true)
	if identity <= plain {
		t.Fatalf("identity exemption failed: identity=%v plain=%v", identity, plain)
	}
	if identity != 0.9 {
		t.Fatalf("identity heat should stay at anchor 0.9, got %v", identity)
	}
}

func TestHeatGradeBands(t *testing.T) {
	cases := []struct {
		importance, heat float64
		want             string
	}{
		{1.0, 1.0, "hot"},
		{0.4, 0.75, "hot"},
		{0.4, 0.5, "warm"},
		{0.4, 0.2, "cold"},
		{0.2, 0.25, "cold"},
	}
	for _, item := range cases {
		if got := HeatGrade(item.importance, item.heat); got != item.want {
			t.Fatalf("HeatGrade(%v,%v)=%v want %v", item.importance, item.heat, got, item.want)
		}
	}
}

func TestIdentityKind(t *testing.T) {
	for _, kind := range []string{"USER_PREFERENCE", "PERSONAL_GOAL", "PERSONAL_CONSTRAINT"} {
		if !IdentityKind(kind) {
			t.Fatalf("IdentityKind(%q) should be true", kind)
		}
	}
	for _, kind := range []string{"PROJECT_DECISION", "LESSON", "DOCUMENT_FACT"} {
		if IdentityKind(kind) {
			t.Fatalf("IdentityKind(%q) should be false", kind)
		}
	}
}

func TestFeedbackRequestValidate(t *testing.T) {
	valid := FeedbackRequest{SessionID: "s", MemoryID: "m", Outcome: "helped"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid feedback rejected: %v", err)
	}
	for _, outcome := range []string{"helped", "misled"} {
		r := FeedbackRequest{SessionID: "s", MemoryID: "m", Outcome: outcome}
		if err := r.Validate(); err != nil {
			t.Fatalf("outcome %q rejected: %v", outcome, err)
		}
	}
	for _, r := range []FeedbackRequest{
		{SessionID: "", MemoryID: "m", Outcome: "helped"},
		{SessionID: "s", MemoryID: "", Outcome: "helped"},
		{SessionID: "s", MemoryID: "m", Outcome: "maybe"},
	} {
		if err := r.Validate(); err == nil {
			t.Fatalf("invalid feedback accepted: %+v", r)
		}
	}
}
