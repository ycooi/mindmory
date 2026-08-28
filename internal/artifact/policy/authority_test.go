package policy

import (
	"context"
	"testing"

	"mindmory.local/core/internal/artifact/ingest"
	"mindmory.local/core/internal/debugnode"
)

type recorder struct{ events []debugnode.Event }

func (r *recorder) Observe(_ context.Context, event debugnode.Event) {
	r.events = append(r.events, event)
}

func TestChannelOriginAssignmentAndApprovalPreservesOrigin(t *testing.T) {
	cases := map[ingest.Channel]OriginClass{
		ingest.ChannelHostUserAttachment: OriginUserProvided,
		ingest.ChannelAdminAuthored:      OriginUserAuthored,
		ingest.ChannelGeneratedArtifact:  OriginAgentGenerated,
		ingest.ChannelExternalImport:     OriginExternalSource,
		ingest.ChannelTrustedToolCapture: OriginTrustedTool,
		ingest.ChannelUnclassified:       OriginUnknown,
	}
	for channel, expected := range cases {
		authority, err := NewAuthority(channel)
		if err != nil {
			t.Fatal(err)
		}
		if authority.Origin() != expected || authority.Approval() != ApprovalUnreviewed {
			t.Fatalf("channel=%s authority=%+v", channel, authority)
		}
	}
	authority, err := NewAuthority(ingest.ChannelGeneratedArtifact)
	if err != nil {
		t.Fatal(err)
	}
	observer := &recorder{}
	updated, err := authority.ChangeApproval(context.Background(), ApprovalChange{ArtifactID: "artifact-1",
		State: ApprovalUserApproved, Reason: "reviewed by owner", Confirmed: true}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Origin() != OriginAgentGenerated || updated.Approval() != ApprovalUserApproved {
		t.Fatalf("approval changed provenance: origin=%s approval=%s", updated.Origin(), updated.Approval())
	}
	if authority.Approval() != ApprovalUnreviewed || len(observer.events) != 1 {
		t.Fatal("authority mutated in place or event missing")
	}
}

func TestApprovalRequiresGovernedAction(t *testing.T) {
	authority, err := NewAuthority(ingest.ChannelExternalImport)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []ApprovalChange{
		{ArtifactID: "artifact-1", State: ApprovalUserApproved, Confirmed: true},
		{ArtifactID: "artifact-1", State: ApprovalUserApproved, Reason: "ok"},
	} {
		if _, err := authority.ChangeApproval(context.Background(), change, nil); err == nil {
			t.Fatalf("unsafe approval accepted: %+v", change)
		}
	}
}

func TestUnknownChannelCannotBecomeUnknownOrigin(t *testing.T) {
	if _, err := NewAuthority(ingest.Channel("MODEL_SELECTED")); err == nil {
		t.Fatal("unknown channel was silently assigned an origin")
	}
}
