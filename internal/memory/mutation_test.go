package memory

import (
	"testing"

	"mindmory.local/core/internal/archive"
	"mindmory.local/core/internal/artifact/policy"
)

func evidence(content string) archive.MessageEvidence {
	return archive.MessageEvidence{MessageID: "message-current", ClientID: "codex", SessionID: "session-1",
		Role: archive.RoleUser, Content: content, CurrentUserTurn: true, Sensitivity: policy.SensitivityNormal}
}

func activeTarget(id, subject, content string) *MutationTarget {
	return &MutationTarget{MemoryID: id, Kind: KindUserPreference, Subject: subject, Content: content,
		Lifecycle: LifecycleActive, Sensitivity: policy.SensitivityNormal}
}

func TestVerifiedCurrentUserMutationsApply(t *testing.T) {
	remember := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1",
		Subject: "architecture before implementation", MessageID: "message-current",
		EvidenceQuote: "I prefer architecture before implementation."}
	if got := VerifyMutation(remember, evidence(remember.EvidenceQuote), nil); got.Outcome != MutationApply {
		t.Fatalf("remember decision=%+v", got)
	}
	correct := MutationRequest{Kind: MutationCorrect, ClientID: "codex", SessionID: "session-1",
		MessageID: "message-current", TargetMemoryID: "memory-shared", Replacement: "single-user",
		EvidenceQuote: "Mindmory is now single-user instead of shared."}
	if got := VerifyMutation(correct, evidence(correct.EvidenceQuote),
		activeTarget("memory-shared", "Mindmory ownership", "Mindmory is shared")); got.Outcome != MutationApply {
		t.Fatalf("correct decision=%+v", got)
	}
	forget := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		TargetMemoryID: "memory-python", MessageID: "message-current",
		EvidenceQuote: "Forget my previous preference for Python."}
	if got := VerifyMutation(forget, evidence(forget.EvidenceQuote),
		activeTarget("memory-python", "preference for Python", "User prefers Python")); got.Outcome != MutationApply {
		t.Fatalf("forget decision=%+v", got)
	}
}

func TestUntrustedOrHistoricalMutationEvidenceStages(t *testing.T) {
	request := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		MessageID: "message-current", TargetMemoryID: "memory-python", EvidenceQuote: "Forget my previous preference for Python."}
	target := activeTarget("memory-python", "preference for Python", "User prefers Python")
	cases := []archive.MessageEvidence{
		{MessageID: "message-current", ClientID: "codex", SessionID: "session-1", Role: archive.RoleAssistant, Content: request.EvidenceQuote, CurrentUserTurn: true},
		{MessageID: "message-current", ClientID: "codex", SessionID: "session-1", Role: archive.RoleUser, Content: request.EvidenceQuote, CurrentUserTurn: false, Retrieved: true},
		{MessageID: "message-current", ClientID: "codex", SessionID: "session-1", Role: archive.RoleUser, Content: request.EvidenceQuote, CurrentUserTurn: true, InstructionLike: true},
		{MessageID: "message-current", ClientID: "other", SessionID: "session-1", Role: archive.RoleUser, Content: request.EvidenceQuote, CurrentUserTurn: true},
	}
	for _, item := range cases {
		if got := VerifyMutation(request, item, target); got.Outcome != MutationStaged {
			t.Fatalf("unsafe evidence applied: %+v", item)
		}
	}
}

func TestMutationBindsHydratedMessageAndTarget(t *testing.T) {
	request := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		MessageID: "historical-message", TargetMemoryID: "memory-python",
		EvidenceQuote: "Forget my previous preference for Python."}
	target := activeTarget("memory-python", "preference for Python", "User prefers Python")
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "CITED_MESSAGE_REQUIRED" {
		t.Fatalf("mismatched cited message result: %+v", got)
	}
	request.MessageID = "message-current"
	request.TargetMemoryID = "wrong-memory"
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "TARGET_MEMORY_MISMATCH" {
		t.Fatalf("wrong target result: %+v", got)
	}
	request.TargetMemoryID = target.MemoryID
	target.Lifecycle = LifecycleForgotten
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "TARGET_MEMORY_INACTIVE" {
		t.Fatalf("inactive target result: %+v", got)
	}
}

func TestCorrectionRequiresEvidenceBackedReplacementAndTarget(t *testing.T) {
	request := MutationRequest{Kind: MutationCorrect, ClientID: "codex", SessionID: "session-1",
		MessageID: "message-current", TargetMemoryID: "memory-language", Replacement: "Rust",
		EvidenceQuote: "Change my preferred language to Go instead."}
	target := activeTarget("memory-language", "preferred language", "User prefers Python")
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "REPLACEMENT_NOT_VERIFIED" {
		t.Fatalf("unsupported replacement result: %+v", got)
	}
	request.Replacement = "going"
	request.EvidenceQuote = "Change my preferred language to Go instead."
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "REPLACEMENT_NOT_VERIFIED" {
		t.Fatalf("replacement substring was treated as evidence: %+v", got)
	}
	request.Replacement = "Go"
	target.Subject = "unrelated travel destination"
	target.Content = "User plans to visit Paris"
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "TARGET_MEMORY_NOT_VERIFIED" {
		t.Fatalf("unrelated target result: %+v", got)
	}
}

func TestSensitiveTargetStagesForReview(t *testing.T) {
	request := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		MessageID: "message-current", TargetMemoryID: "memory-secret",
		EvidenceQuote: "Forget my previous secret project preference."}
	target := activeTarget("memory-secret", "secret project preference", "User has a secret project preference")
	target.Sensitivity = policy.SensitivitySecret
	if got := VerifyMutation(request, evidence(request.EvidenceQuote), target); got.Reason != "TARGET_REQUIRES_REVIEW" {
		t.Fatalf("sensitive target result: %+v", got)
	}
}

func TestChineseExplicitMutationCuesRemainDeterministic(t *testing.T) {
	for _, item := range []struct{ quote, subject string }{
		{"记住以后这个项目优先使用 Go。", "这个项目"},
		{"我的偏好是以后尽量使用 Go。", "我的偏好"},
	} {
		request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: item.quote, Subject: item.subject}
		if decision := VerifyMutation(request, evidence(item.quote), nil); decision.Outcome != MutationApply {
			t.Fatalf("remember %q staged: %+v", item.quote, decision)
		}
	}
	target := activeTarget("memory-1", "实现语言", "实现语言: Python")
	for _, quote := range []string{"把之前的实现语言改成 Go。", "更正一下，实现语言应该是 Go。"} {
		request := MutationRequest{Kind: MutationCorrect, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: quote, TargetMemoryID: target.MemoryID, Replacement: "Go"}
		if decision := VerifyMutation(request, evidence(quote), target); decision.Outcome != MutationApply {
			t.Fatalf("correct %q staged: %+v", quote, decision)
		}
	}
	forgetTarget := activeTarget("memory-2", "Python 偏好", "Python 偏好")
	for _, quote := range []string{"忘掉我之前关于 Python 的偏好。", "不用记之前那个 Python 的决定了。"} {
		request := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: quote, TargetMemoryID: forgetTarget.MemoryID}
		if decision := VerifyMutation(request, evidence(quote), forgetTarget); decision.Outcome != MutationApply {
			t.Fatalf("forget %q staged: %+v", quote, decision)
		}
	}
}

func TestChineseNonIntentAndContradictoryRememberStage(t *testing.T) {
	for _, quote := range []string{"不要记住这个测试内容。", "我们刚刚讨论了 Go。", "Go 可能不错。", "有人说最好记住 Go。"} {
		request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: quote, Subject: "Go"}
		decision := VerifyMutation(request, evidence(quote), nil)
		if decision.Outcome != MutationStaged || decision.Reason != "EXPLICIT_INTENT_NOT_VERIFIED" {
			t.Fatalf("quote %q unexpectedly authorized: %+v", quote, decision)
		}
	}
}

func TestInformationalInquiriesAreNeverRemembered(t *testing.T) {
	for _, quote := range []string{
		"I want to know the weather forecast.",
		"I want to ask about the Go project.",
		"I wonder whether Rust is better.",
		"I'd like to know the architecture decision.",
		"Can you tell me my previous preference?",
		"Do you remember the Go decision?",
		"我想知道这个项目用什么语言。",
		"还记得之前 Go 的决定吗。",
	} {
		request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: quote, Subject: "Go"}
		decision := VerifyMutation(request, evidence(quote), nil)
		if decision.Outcome != MutationStaged || decision.Reason != "EXPLICIT_INTENT_NOT_VERIFIED" {
			t.Fatalf("inquiry %q unexpectedly authorized: %+v", quote, decision)
		}
	}
}

func TestGenuineWantStillAuthorizesRemember(t *testing.T) {
	quote := "I want to use Go for the Mindmory MCP server."
	request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: quote, Subject: "MCP server"}
	if decision := VerifyMutation(request, evidence(quote), nil); decision.Outcome != MutationApply {
		t.Fatalf("genuine want staged: %+v", decision)
	}
}

func TestShortSubjectsVerifyOnlyAtWordBoundaries(t *testing.T) {
	apply := []struct{ quote, subject string }{
		{"I want to use Go for the MCP server.", "Go"},
		{"记住以后 Mindmory 的 MCP server 优先用 Go。", "Go"},
		{"记住，我的密码是 hunter2。", "密码"},
	}
	for _, item := range apply {
		request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: item.quote, Subject: item.subject}
		if decision := VerifyMutation(request, evidence(item.quote), nil); decision.Outcome != MutationApply {
			t.Fatalf("short subject %q with %q staged: %+v", item.subject, item.quote, decision)
		}
	}
	stage := []struct{ quote, subject string }{
		{"Remember that Google announced a new compiler.", "Go"}, // embedded, not a word
		{"Remember that I found a dog in the park.", "Go"},       // embedded backwards
		{"Remember that we are building a memory system.", "We"}, // function word
		{"Remember that it is important to decide.", "In"},       // function word
	}
	for _, item := range stage {
		request := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1", MessageID: "message-current", EvidenceQuote: item.quote, Subject: item.subject}
		if decision := VerifyMutation(request, evidence(item.quote), nil); decision.Outcome != MutationStaged || decision.Reason != "MEMORY_SUBJECT_NOT_VERIFIED" {
			t.Fatalf("short subject %q with %q unexpectedly verified: %+v", item.subject, item.quote, decision)
		}
	}
}

func TestReviewerMayOverrideOnlyIntentGate(t *testing.T) {
	// Review-authorized remember: no cue words needed, subject must still overlap the quote.
	remember := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1",
		Subject: "architecture before implementation", MessageID: "message-current",
		EvidenceQuote: "Write all architecture before any implementation."}
	if got := VerifyMutationForReview(remember, evidence(remember.EvidenceQuote), nil); got.Outcome != MutationApply {
		t.Fatalf("reviewed remember decision=%+v", got)
	}
	// The same proposal without explicit flag fails the cue gate.
	if got := VerifyMutation(remember, evidence(remember.EvidenceQuote), nil); got.Reason != "EXPLICIT_INTENT_NOT_VERIFIED" {
		t.Fatalf("non-explicit remember should stage: %+v", got)
	}
	// Reviewed correct and forget also bypass the cue gate but keep target checks.
	correct := MutationRequest{Kind: MutationCorrect, ClientID: "codex", SessionID: "session-1",
		MessageID: "message-current", TargetMemoryID: "memory-shared", Replacement: "single-user",
		EvidenceQuote: "Mindmory is now single-user instead of shared."}
	if got := VerifyMutationForReview(correct, evidence(correct.EvidenceQuote),
		activeTarget("memory-shared", "Mindmory ownership", "Mindmory is shared")); got.Outcome != MutationApply {
		t.Fatalf("reviewed correct decision=%+v", got)
	}
	forget := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		TargetMemoryID: "memory-python", MessageID: "message-current",
		EvidenceQuote: "Forget my previous preference for Python."}
	if got := VerifyMutationForReview(forget, evidence(forget.EvidenceQuote),
		activeTarget("memory-python", "preference for Python", "User prefers Python")); got.Outcome != MutationApply {
		t.Fatalf("reviewed forget decision=%+v", got)
	}
}

func TestReviewerCannotBypassDeterministicChecks(t *testing.T) {
	// Review does not forgive a missing exact quote or a non-overlapping subject.
	noQuote := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1",
		Subject: "unrelated topic", MessageID: "message-current", EvidenceQuote: "Never said this."}
	if got := VerifyMutationForReview(noQuote, evidence("The actual message text."), nil); got.Reason != "EXACT_EVIDENCE_QUOTE_REQUIRED" {
		t.Fatalf("missing exact quote should stage: %+v", got)
	}
	badSubject := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1",
		Subject: "quantum teleportation", MessageID: "message-current",
		EvidenceQuote: "Write all architecture before any implementation."}
	if got := VerifyMutationForReview(badSubject, evidence(badSubject.EvidenceQuote), nil); got.Reason != "MEMORY_SUBJECT_NOT_VERIFIED" {
		t.Fatalf("non-overlapping subject should stage: %+v", got)
	}
	// Reviewed forget still requires an active target.
	forget := MutationRequest{Kind: MutationForget, ClientID: "codex", SessionID: "session-1",
		TargetMemoryID: "memory-python", MessageID: "message-current",
		EvidenceQuote: "Forget my previous preference for Python."}
	if got := VerifyMutationForReview(forget, evidence(forget.EvidenceQuote), nil); got.Reason != "TARGET_MEMORY_REQUIRED" {
		t.Fatalf("missing target should stage: %+v", got)
	}
	secretEvidence := evidence("My password is hunter2.")
	secretEvidence.SecretLike = true
	secret := MutationRequest{Kind: MutationRemember, ClientID: "codex", SessionID: "session-1",
		Subject: "password", MessageID: "message-current", EvidenceQuote: "My password is hunter2."}
	if got := VerifyMutationForReview(secret, secretEvidence, nil); got.Class != GateSecurity || got.Outcome != MutationStaged {
		t.Fatalf("reviewer bypassed security gate: %+v", got)
	}
}

func TestRelaxedCJKSubjectOverlap(t *testing.T) {
	evidence := "请记住我的咖啡偏好:咖啡要加冰块,不加糖。"
	// Shortened/windowed subjects that a summarizer would naturally produce.
	for _, subj := range []string{"咖啡加冰块", "咖啡不加糖", "咖啡", "冰块不加糖"} {
		if !overlaps(subj, evidence) {
			t.Errorf("subject %q should overlap evidence %q", subj, evidence)
		}
	}
	// Unrelated subject must still fail — the relaxation must not be a
	// blank check.
	if overlaps("量子物理", evidence) {
		t.Error("unrelated subject must not overlap")
	}
	// English-only subject for Chinese evidence still fails (cross-language
	// stays a governance question, not auto-allowed).
	if overlaps("coffee with ice", evidence) {
		t.Error("cross-language subject must not auto-overlap")
	}
}
