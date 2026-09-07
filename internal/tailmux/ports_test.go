package tailmux

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSSListeners(t *testing.T) {
	got := parseSSListeners([]string{
		`LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:(("node",pid=12,fd=20))`,
		`LISTEN 0 4096 [::]:8080 [::]:*`,
		`bad line`,
	})
	if len(got) != 2 || got[0].Port != 3000 || got[0].Address != "127.0.0.1" || got[0].Process != "node" || got[1].Port != 8080 {
		t.Fatalf("unexpected listeners: %+v", got)
	}
}

func TestParseLsofListeners(t *testing.T) {
	got := parseLsofListeners([]string{
		"COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME",
		"node 12 sean 20u IPv4 1 0t0 TCP 127.0.0.1:3000 (LISTEN)",
	})
	if len(got) != 1 || got[0].Port != 3000 || got[0].Process != "node" {
		t.Fatalf("unexpected listeners: %+v", got)
	}
}

func TestWritePorts(t *testing.T) {
	var b bytes.Buffer
	if err := writePorts(&b, "lab/dev", []ListeningPort{{Port: 3000, Address: "127.0.0.1", Process: "node"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HOST", "lab/dev", "3000", "node"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %q in %s", want, b.String())
		}
	}
}

func TestBoolFlagsAcceptedAfterTarget(t *testing.T) {
	got, err := boolFlagsFirst([]string{"lab/dev", "--json"}, map[string]bool{"--json": true})
	if err != nil || strings.Join(got, " ") != "--json lab/dev" {
		t.Fatalf("unexpected ordering: %v %v", got, err)
	}
	if _, err := boolFlagsFirst([]string{"lab/dev", "--wat"}, map[string]bool{"--json": true}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	got, err = boolFlagsFirst([]string{"--all", "--json"}, map[string]bool{"--json": true, "--all": false})
	if err != nil || strings.Join(got, " ") != "--json --all" {
		t.Fatalf("dash positional lost: %v %v", got, err)
	}
}
