package tailmux

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionTable(t *testing.T) {
	var b bytes.Buffer
	err := writeSessionRows(&b, "lab/worker", json.RawMessage(`{"sessions":[{"name":"agents","running":true,"socket_path":"/private"},{"name":"default","running":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := b.String()
	if !strings.Contains(s, "lab/worker\tagents\trunning") || !strings.Contains(s, "default\tstopped") || strings.Contains(s, "private") {
		t.Fatal(s)
	}
	if writeSessionRows(&b, "lab/worker", json.RawMessage(`{}`)) == nil {
		t.Fatal("missing sessions accepted")
	}
}
