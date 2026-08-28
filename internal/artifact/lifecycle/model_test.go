package lifecycle

import "testing"

func TestGeneratedOutputDefaultsAreServerOwned(t *testing.T) {
	tests := []struct {
		output    string
		role      Role
		retention RetentionClass
	}{{"PRIMARY_RESULT", RoleWorkProduct, RetentionTemporary}, {"SECONDARY_RESULT", RoleWorkProduct, RetentionTemporary}, {"FINAL_REPORT", RoleFinalOutput, RetentionRetained}, {"DIAGNOSTIC", RoleWorkProduct, RetentionSession}}
	for _, test := range tests {
		role, retention, err := Defaults(test.output)
		if err != nil || role != test.role || retention != test.retention {
			t.Fatalf("%s => %s/%s %v", test.output, role, retention, err)
		}
	}
	if _, _, err := Defaults("SCRATCH"); err == nil {
		t.Fatal("scratch was promoted into durable artifact authority")
	}
}
