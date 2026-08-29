package dsh_test

import (
	"os/exec"
	"testing"
)

func TestPackagedCheckpointRelay(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to execute the packaged DeepSeek Harness plugin test")
	}
	command := exec.Command("node", "--test", "checkpoint-relay.test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("DeepSeek Harness relay test failed: %v\n%s", err, output)
	}
}
