package policy

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSensitivityInheritanceIsMonotonic(t *testing.T) {
	levels := []Sensitivity{SensitivityNormal, SensitivitySensitive, SensitivitySecret, SensitivityRestricted}
	for _, source := range levels {
		for _, detected := range levels {
			result := InheritSensitivity(source, detected)
			if result < source || result < detected {
				t.Fatalf("source=%s detected=%s result=%s", source, detected, result)
			}
		}
	}
}

func TestSensitivityUsesSymbolicJSON(t *testing.T) {
	encoded, err := json.Marshal(SensitivitySecret)
	if err != nil || string(encoded) != `"SECRET"` {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	var decoded Sensitivity
	if err = json.Unmarshal([]byte(`"RESTRICTED"`), &decoded); err != nil || decoded != SensitivityRestricted {
		t.Fatalf("decoded=%s err=%v", decoded, err)
	}
}

func TestAdminDowngradeRequiresReasonAndConfirmation(t *testing.T) {
	requests := []DowngradeRequest{
		{ResourceID: "artifact-1", Current: SensitivitySecret, Requested: SensitivityNormal, Confirmed: true},
		{ResourceID: "artifact-1", Current: SensitivitySecret, Requested: SensitivityNormal, Reason: "owner reviewed"},
		{ResourceID: "artifact-1", Current: SensitivityNormal, Requested: SensitivitySecret, Reason: "not a downgrade", Confirmed: true},
	}
	for _, request := range requests {
		if _, err := AuthorizeDowngrade(context.Background(), request, nil); err == nil {
			t.Fatalf("unsafe downgrade accepted: %+v", request)
		}
	}
	observer := &recorder{}
	result, err := AuthorizeDowngrade(context.Background(), DowngradeRequest{ResourceID: "artifact-1",
		Current: SensitivitySecret, Requested: SensitivitySensitive, Reason: "owner verified classification", Confirmed: true}, observer)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AuditRequired || !result.RecalculateRetrieval || !result.InvalidateSemanticProjection {
		t.Fatalf("downgrade effects incomplete: %+v", result)
	}
	if len(observer.events) != 1 || observer.events[0].Node != "SENSITIVITY.DOWNGRADE" {
		t.Fatalf("downgrade debug event missing: %+v", observer.events)
	}
}
