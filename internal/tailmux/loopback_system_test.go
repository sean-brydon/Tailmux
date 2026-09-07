package tailmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateLoopbackHostsPreservesAndIsIdempotent(t *testing.T) {
	original := []byte("# system entries\n127.0.0.1 localhost\n::1 localhost\n")
	updated, changed, err := updateLoopbackHosts(original, "127.77.0.2", "dev.test")
	if err != nil || !changed {
		t.Fatalf("update failed: %t %v", changed, err)
	}
	if !strings.HasPrefix(string(updated), string(original)) || !strings.Contains(string(updated), "127.77.0.2\tdev.test\t# tailmux loopback dev.test") {
		t.Fatalf("existing content changed: %q", updated)
	}
	again, changed, err := updateLoopbackHosts(updated, "127.77.0.2", "dev.test")
	if err != nil || changed || string(again) != string(updated) {
		t.Fatalf("not idempotent: %t %v\n%s", changed, err, again)
	}
}

func TestLoopbackSystemSetupRollsBackNewAlias(t *testing.T) {
	hosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalEUID, originalOS, originalPath := loopbackEUID, loopbackOS, loopbackHostsPath
	originalOutput, originalRun, originalReplace := loopbackOutput, loopbackRun, loopbackReplace
	t.Cleanup(func() {
		loopbackEUID, loopbackOS, loopbackHostsPath = originalEUID, originalOS, originalPath
		loopbackOutput, loopbackRun, loopbackReplace = originalOutput, originalRun, originalReplace
	})
	loopbackEUID = func() int { return 0 }
	loopbackOS = "linux"
	loopbackHostsPath = hosts
	loopbackOutput = func(string, ...string) ([]byte, error) { return []byte("127.0.0.1/8"), nil }
	var commands []string
	loopbackRun = func(name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}
	loopbackReplace = func(string, []byte) error { return errors.New("write failed") }
	err := loopbackSystemSetup("127.77.0.2", "dev.test")
	if err == nil || len(commands) != 2 || !strings.Contains(commands[0], "address add") || !strings.Contains(commands[1], "address del") {
		t.Fatalf("alias was not rolled back: %v %#v", err, commands)
	}
}

func TestUpdateLoopbackHostsRejectsConflict(t *testing.T) {
	_, _, err := updateLoopbackHosts([]byte("127.88.0.2 dev.test # someone else\n"), "127.77.0.2", "dev.test")
	if err == nil || !strings.Contains(err.Error(), "already mapped") {
		t.Fatalf("conflict accepted: %v", err)
	}
	_, _, err = updateLoopbackHosts([]byte("127.77.0.2 dev.test\n127.88.0.2 dev.test\n"), "127.77.0.2", "dev.test")
	if err == nil {
		t.Fatal("conflict after equivalent mapping accepted")
	}
}

func TestValidateLoopbackSystem(t *testing.T) {
	valid := [][2]string{{"127.77.0.2", "dev.test"}, {"127.1.2.3", "box.example.internal"}}
	for _, pair := range valid {
		if err := validateLoopbackSystem(pair[0], pair[1]); err != nil {
			t.Errorf("rejected %v: %v", pair, err)
		}
	}
	invalid := [][2]string{{"127.0.0.1", "dev.test"}, {"10.0.0.1", "dev.test"}, {"127.1.2.3", "dev.localhost"}, {"127.1.2.3", "localhost"}, {"127.1.2.3", "UPPER.test"}, {"127.1.2.3", "single"}, {"127.1.2.3", "bad_.test"}}
	for _, pair := range invalid {
		if err := validateLoopbackSystem(pair[0], pair[1]); err == nil {
			t.Errorf("accepted %v", pair)
		}
	}
}
