package mcpserver

import "testing"

func TestErrorEnvelopeAllowsSafeMetadataAndRejectsContentBearingDetails(t *testing.T) {
	valid := ErrorEnvelope{Code: "INVALID_REQUEST", Message: "Request failed validation.",
		TraceID: "trace-1", Details: map[string]any{"reason_code": "UNKNOWN_FIELD", "count": 1}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Details["evidence_quote"] = "Forget my private preference"
	if err := valid.Validate(); err == nil {
		t.Fatal("content-bearing error details accepted")
	}
}
