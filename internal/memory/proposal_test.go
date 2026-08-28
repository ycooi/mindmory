package memory

import "testing"

func TestRequestHashIsStableAndCoversMutationIdentity(t *testing.T) {
	identity := ProposalIdentity{ClientKey: "codex", SessionID: "s", MessageID: "m", Mutation: MutationRemember,
		ProposedKind: KindProjectDecision, Scope: ScopeProject, ProjectKey: "Mindmory", Subject: "retention", RequestEvidenceHash: "sha256:quote"}
	first := RequestHash(identity)
	if first != RequestHash(identity) || len(first) != 71 {
		t.Fatalf("unstable request hash %q", first)
	}
	identity.Subject = "other"
	if first == RequestHash(identity) {
		t.Fatal("changed proposal identity reused request hash")
	}
}
