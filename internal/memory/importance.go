package memory

import "strings"

// Importance deterministically derives a 0..1 declared grade from explicit
// intensity cues in the cited evidence. It is not model judgment: the same
// quote always yields the same value. The grade is the write-time commitment
// strength and the decay floor for heat (Stage 7A two-column design); it is
// never touched by usage. Five grades, approved 2026-08-20:
//
//	1.0 critical    — highest priority, must never forget
//	0.8 strong      — important / priority / should always
//	0.6 notable     — matters / remember to / 记住
//	0.4 default     — no intensity cue (routine note)
//	0.2 trivial     — explicitly low value, fades fast
//
// Existing memories written with the historical 3-grade values (0.5, 0.75,
// 1.0) remain legal members of the 5-grade set; no data migration is needed.
func Importance(quote string) float64 {
	lower := strings.ToLower(quote)
	for _, cue := range criticalImportanceCues {
		if strings.Contains(lower, cue) {
			return 1.0
		}
	}
	// Trivial cues are checked before strong/notable cues so negation forms
	// like "not important" / "不重要" win over the positive substring they
	// contain ("important" / "重要").
	for _, cue := range trivialImportanceCues {
		if strings.Contains(lower, cue) {
			return 0.2
		}
	}
	for _, cue := range strongImportanceCues {
		if strings.Contains(lower, cue) {
			return 0.8
		}
	}
	for _, cue := range notableImportanceCues {
		if strings.Contains(lower, cue) {
			return 0.6
		}
	}
	return 0.4
}

// criticalImportanceCues: must-never-forget intensity.
var criticalImportanceCues = []string{
	"very important", "crucially", "critical", "top priority", "highest priority",
	"must never", "absolutely must", "absolutely",
	"最重要", "非常重要", "极其重要", "关键", "重中之重", "一定要记住", "千万别忘", "务必",
}

// strongImportanceCues: clear priority, but not absolute.
var strongImportanceCues = []string{
	"important", "priority", "should always", "always remember",
	"重要", "优先", "务必记住",
}

// notableImportanceCues: worth remembering, no strong intensity.
var notableImportanceCues = []string{
	"matters", "remember to",
	"记住", "记得",
}

// trivialImportanceCues: explicitly low value.
var trivialImportanceCues = []string{
	"not important", "doesn't matter", "no need to remember", "trivial", "minor detail",
	"不重要", "无所谓", "不用记", "小事",
}
