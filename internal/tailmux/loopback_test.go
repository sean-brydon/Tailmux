package tailmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackAllocationStableAndDistinct(t *testing.T) {
	cfg := loopbackConfig{}
	first := allocateLoopback(cfg, "personal/devl")
	cfg["personal/devl"] = loopbackBinding{Address: first}
	if allocateLoopback(cfg, "personal/devl") != first {
		t.Fatal("address changed")
	}
	// Reserve the candidate for another target to exercise collision resolution.
	candidate := allocateLoopback(cfg, "work/devl")
	cfg["occupied"] = loopbackBinding{Address: candidate}
	second := allocateLoopback(cfg, "work/devl")
	if first == second || second == candidate || !strings.HasPrefix(second, "127.77.") {
		t.Fatal("address collision", first, second)
	}
}
func TestNamedLoopbackRequiresExplicitMapping(t *testing.T) {
	dir := t.TempDir()
	cfg := loopbackConfig{"personal/devl": {Address: "127.77.1.2", Names: []string{"devl.test"}}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "loopbacks.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	address, err := namedLoopbackAddress(dir, "personal/devl", "devl.test")
	if err != nil || address != "127.77.1.2" {
		t.Fatal(address, err)
	}
	for _, test := range [][2]string{{"work/devl", "devl.test"}, {"personal/devl", "other.test"}, {"personal/devl", "devl.localhost"}} {
		if _, err := namedLoopbackAddress(dir, test[0], test[1]); err == nil {
			t.Fatal("accepted missing/reserved mapping", test)
		}
	}
}
