package retrieval

import (
	"testing"
)

func TestProjectCursorRoundTripAndIsolation(t *testing.T) {
	key := []byte("test-cursor-signing-key-at-least-32-bytes")
	v1, err := SignCursor(key, CursorPayload{Version: 1, SessionID: "session-a", ProjectKeyHash: ProjectHash("Mindmory"), Revision: 7})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := VerifyCursor(key, v1)
	if err != nil || payload.Version != 1 || payload.SessionID != "session-a" || payload.Revision != 7 {
		t.Fatalf("v1 round trip=%+v err=%v", payload, err)
	}
	v2, err := SignProjectCursor(key, "Mindmory", 7)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = VerifyCursor(key, v2)
	if err != nil || payload.Version != 2 || payload.SessionID != "" || payload.ProjectKeyHash != ProjectHash("Mindmory") || payload.Revision != 7 {
		t.Fatalf("v2 round trip=%+v err=%v", payload, err)
	}
	other, err := SignProjectCursor(key, "FertAgent", 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyCursor(key, other); err != nil {
		t.Fatalf("v2 other project verify=%v", err)
	}
	if _, err = VerifyCursor(key, "x"+v2[1:]); err == nil {
		t.Fatal("tampered v2 cursor accepted")
	}
	wrongKey := []byte("another-cursor-signing-key-at-least-32-bytes")
	if _, err = VerifyCursor(wrongKey, v2); err == nil {
		t.Fatal("v2 cursor accepted with wrong key")
	}
	if _, err = VerifyCursor(key, v1); err != nil {
		t.Fatalf("v1 re-verify=%v", err)
	}
}
